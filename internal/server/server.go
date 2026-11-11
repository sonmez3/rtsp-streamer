// Package server is the embedded RTSP server. Each camera gets one path. Our
// own ffmpeg processes publish to it (with a per-run password), and viewers
// read from it over TCP or UDP.
package server

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/liberrors"
	"github.com/pion/rtp"

	"github.com/sonmez3/rtsp-streamer/internal/logx"
)

type Options struct {
	Bind    string // empty = all interfaces
	Port    int    // 0 picks a free port (tests)
	UDP     bool   // let viewers use RTSP over UDP
	UDPPort int    // RTP port for UDP viewers, RTCP is UDPPort+1

	Names []string // camera names; the only paths that exist
	// Cameras that play once; their publisher leaving is expected.
	Once map[string]bool

	// Credentials our ffmpeg processes use to publish. Nobody else may publish.
	PublishUser string
	PublishPass string

	Log *logx.Logger
}

type Server struct {
	opts Options
	rs   *gortsplib.Server

	mu      sync.Mutex
	paths   map[string]*path
	closing bool
}

type path struct {
	name      string
	stream    *gortsplib.ServerStream
	publisher *gortsplib.ServerSession
	viewers   int
	live      chan struct{} // closed the first time the path goes live
	wasLive   bool
}

// Stored on each gortsplib session so we know what it is when it closes.
type sessionInfo struct {
	path      *path
	publisher bool
	viewer    bool
	addr      string
}

func New(opts Options) *Server {
	s := &Server{opts: opts, paths: make(map[string]*path)}
	for _, n := range opts.Names {
		s.paths[n] = &path{name: n, live: make(chan struct{})}
	}
	s.rs = &gortsplib.Server{
		Handler:        &handler{s},
		RTSPAddress:    net.JoinHostPort(opts.Bind, strconv.Itoa(opts.Port)),
		WriteQueueSize: 512,
	}
	if opts.UDP {
		s.rs.UDPRTPAddress = net.JoinHostPort(opts.Bind, strconv.Itoa(opts.UDPPort))
		s.rs.UDPRTCPAddress = net.JoinHostPort(opts.Bind, strconv.Itoa(opts.UDPPort+1))
	}
	return s
}

// Start opens the listeners.
func (s *Server) Start() error { return s.rs.Start() }

// Port returns the TCP port the server listens on.
func (s *Server) Port() int {
	return s.rs.NetListener().Addr().(*net.TCPAddr).Port
}

// BeginShutdown silences "offline" messages while publishers are stopped on
// purpose.
func (s *Server) BeginShutdown() {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
}

// Close stops the server and disconnects everyone.
func (s *Server) Close() {
	s.BeginShutdown()
	s.rs.Close()
}

// Live returns a channel that is closed the first time the camera goes live.
func (s *Server) Live(name string) <-chan struct{} {
	return s.paths[name].live
}

// IsLive reports whether the camera is being published right now.
func (s *Server) IsLive(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paths[name].stream != nil
}

func (s *Server) lookup(rawPath string) *path {
	return s.paths[strings.Trim(rawPath, "/")]
}

func (s *Server) names() string {
	n := make([]string, 0, len(s.paths))
	for k := range s.paths {
		n = append(n, k)
	}
	sort.Strings(n)
	return strings.Join(n, ", ")
}

func remoteHost(c *gortsplib.ServerConn) string {
	addr := c.NetConn().RemoteAddr().String()
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// handler implements gortsplib's callbacks. It is a separate type so these
// methods don't show up on Server.
type handler struct{ s *Server }

func (h *handler) OnSessionClose(ctx *gortsplib.ServerHandlerOnSessionCloseCtx) {
	info, _ := ctx.Session.UserData().(*sessionInfo)
	if info == nil {
		return
	}
	s := h.s
	s.mu.Lock()
	defer s.mu.Unlock()
	p := info.path

	if info.publisher && p.publisher == ctx.Session {
		p.stream.Close() // disconnects the viewers too
		p.stream, p.publisher = nil, nil
		switch {
		case s.closing:
		case s.opts.Once[p.name]:
			s.opts.Log.Infof(p.name, "stream ended")
		default:
			s.opts.Log.Warnf(p.name, "stream offline")
		}
	}
	if info.viewer {
		p.viewers--
		if !s.closing {
			s.opts.Log.Infof(p.name, "viewer %s left (%d watching)", info.addr, p.viewers)
		}
	}
}

func (h *handler) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	s := h.s
	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.lookup(ctx.Path)
	if p == nil {
		s.opts.Log.Warnf("", "%s asked for unknown stream %q (available: %s)", remoteHost(ctx.Conn), ctx.Path, s.names())
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	if p.stream == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	return &base.Response{StatusCode: base.StatusOK}, p.stream, nil
}

func (h *handler) OnAnnounce(ctx *gortsplib.ServerHandlerOnAnnounceCtx) (*base.Response, error) {
	s := h.s
	if !ctx.Conn.VerifyCredentials(ctx.Request, s.opts.PublishUser, s.opts.PublishPass) {
		// ffmpeg's first try comes without credentials; this makes it retry with them.
		return &base.Response{StatusCode: base.StatusUnauthorized}, liberrors.ErrServerAuth{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.lookup(ctx.Path)
	if p == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, fmt.Errorf("unknown path %q", ctx.Path)
	}

	// A restarted ffmpeg replaces the old publisher.
	if p.stream != nil {
		p.stream.Close()
		p.publisher.Close()
	}

	st := &gortsplib.ServerStream{Server: s.rs, Desc: ctx.Description}
	if err := st.Initialize(); err != nil {
		return &base.Response{StatusCode: base.StatusBadRequest}, err
	}
	p.stream, p.publisher = st, ctx.Session
	ctx.Session.SetUserData(&sessionInfo{path: p, publisher: true})
	return &base.Response{StatusCode: base.StatusOK}, nil
}

func (h *handler) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	// Publishers call SETUP too; they don't need a stream back.
	if ctx.Session.State() == gortsplib.ServerSessionStatePreRecord {
		return &base.Response{StatusCode: base.StatusOK}, nil, nil
	}

	s := h.s
	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.lookup(ctx.Path)
	if p == nil || p.stream == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	return &base.Response{StatusCode: base.StatusOK}, p.stream, nil
}

func (h *handler) OnPlay(ctx *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	s := h.s
	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.lookup(ctx.Path)
	if p == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil
	}
	// PLAY can come again after PAUSE; count each viewer once.
	if ctx.Session.UserData() == nil {
		info := &sessionInfo{path: p, viewer: true, addr: remoteHost(ctx.Conn)}
		ctx.Session.SetUserData(info)
		p.viewers++
		proto := "?"
		if t := ctx.Session.Transport(); t != nil {
			proto = t.Protocol.String()
		}
		s.opts.Log.Infof(p.name, "viewer %s connected over %s (%d watching)", info.addr, proto, p.viewers)
	}
	return &base.Response{StatusCode: base.StatusOK}, nil
}

func (h *handler) OnRecord(ctx *gortsplib.ServerHandlerOnRecordCtx) (*base.Response, error) {
	s := h.s
	s.mu.Lock()
	defer s.mu.Unlock()

	info, _ := ctx.Session.UserData().(*sessionInfo)
	if info == nil || info.path.publisher != ctx.Session {
		return &base.Response{StatusCode: base.StatusBadRequest}, fmt.Errorf("RECORD without ANNOUNCE")
	}
	p := info.path
	st := p.stream
	ctx.Session.OnPacketRTPAny(func(medi *description.Media, _ format.Format, pkt *rtp.Packet) {
		_ = st.WritePacketRTP(medi, pkt) // fails only after the stream is closed
	})

	if p.wasLive {
		s.opts.Log.Infof(p.name, "stream back online")
	} else {
		p.wasLive = true
		close(p.live)
	}
	return &base.Response{StatusCode: base.StatusOK}, nil
}
