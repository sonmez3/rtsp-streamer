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
	once      bool // plays its video once; the publisher leaving is expected
	stream    *gortsplib.ServerStream
	publisher *gortsplib.ServerSession
	viewers   int
	live      chan struct{} // closed the first time the path goes live
	wasLive   bool
	removed   bool // taken away with RemovePath; stay quiet about it
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

// AddPath makes a camera's stream available at /name. once means the camera
// plays its video once, so its publisher leaving is expected.
func (s *Server) AddPath(name string, once bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.paths[name]; ok {
		return fmt.Errorf("a stream named %q already exists", name)
	}
	s.paths[name] = &path{name: name, once: once, live: make(chan struct{})}
	return nil
}

// RemovePath takes a camera's stream away. New viewers get "not found" right
// away. The publisher and the current viewers are disconnected when the
// publisher leaves, so stop its ffmpeg next. Nothing is logged for either.
func (s *Server) RemovePath(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.paths[name]; ok {
		p.removed = true
		delete(s.paths, name)
	}
}

// Live returns a channel that is closed the first time the camera goes live,
// or nil if there is no such camera.
func (s *Server) Live(name string) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.paths[name]; ok {
		return p.live
	}
	return nil
}

// IsLive reports whether the camera is being published right now.
func (s *Server) IsLive(name string) bool {
	live, _ := s.Status(name)
	return live
}

// Status reports whether the camera is being published right now and how
// many viewers are watching it.
func (s *Server) Status(name string) (live bool, viewers int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.paths[name]
	if !ok {
		return false, 0
	}
	return p.stream != nil, p.viewers
}

func (s *Server) lookup(rawPath string) *path {
	return s.paths[strings.Trim(rawPath, "/")]
}

func (s *Server) names() string {
	if len(s.paths) == 0 {
		return "none"
	}
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
		case s.closing || p.removed:
		case p.once:
			s.opts.Log.Infof(p.name, "stream ended")
		default:
			s.opts.Log.Warnf(p.name, "stream offline")
		}
	}
	if info.viewer {
		p.viewers--
		if !s.closing && !p.removed {
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
