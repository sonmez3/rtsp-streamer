package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"sync"

	"github.com/sonmez3/rtsp-streamer/internal/camera"
	"github.com/sonmez3/rtsp-streamer/internal/config"
	"github.com/sonmez3/rtsp-streamer/internal/logx"
	"github.com/sonmez3/rtsp-streamer/internal/server"
)

// EngineOptions are the settings an Engine runs with.
type EngineOptions struct {
	Bind    string // listen address, empty = all interfaces
	Port    int    // RTSP port; 0 picks a free one (tests)
	UDP     bool   // let viewers use RTSP over UDP
	UDPPort int    // RTP port for UDP viewers, RTCP is UDPPort+1
	FFmpeg  string // ffmpeg binary; empty = look up in PATH
	Verbose bool
	Log     *logx.Logger
}

// Engine runs the RTSP server and starts and stops cameras on it while it
// runs. Both the command line and the interactive UI use it.
type Engine struct {
	o               EngineOptions
	ffmpeg, ffprobe string
	pass            string // password our ffmpeg processes publish with
	srv             *server.Server

	ctx    context.Context // parent of every camera's runner
	cancel context.CancelFunc

	mu        sync.Mutex
	cams      map[string]*running
	closed    bool
	closeOnce sync.Once
}

type running struct {
	cancel context.CancelFunc
	done   chan struct{} // closed when the runner returns
}

// CameraStatus is what the engine knows about a camera right now.
type CameraStatus struct {
	Running  bool // started and not stopped
	Live     bool // ffmpeg is publishing it right now
	Viewers  int
	Finished bool // a camera that plays once has ended
}

const publishUser = "publisher"

// NewEngine finds ffmpeg and prepares the engine. Call Listen next.
func NewEngine(o EngineOptions) (*Engine, error) {
	ffmpeg, ffprobe, err := findFFmpeg(o.FFmpeg)
	if err != nil {
		return nil, err
	}
	pass, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{
		o:       o,
		ffmpeg:  ffmpeg,
		ffprobe: ffprobe,
		pass:    pass,
		ctx:     ctx,
		cancel:  cancel,
		cams:    make(map[string]*running),
	}, nil
}

// Probe reads a camera's video file.
func (e *Engine) Probe(ctx context.Context, cam config.Camera) (camera.MediaInfo, error) {
	return camera.Probe(ctx, e.ffprobe, cam.Source)
}

// Listen opens the RTSP server's ports.
func (e *Engine) Listen() error {
	e.srv = server.New(server.Options{
		Bind:        e.o.Bind,
		Port:        e.o.Port,
		UDP:         e.o.UDP,
		UDPPort:     e.o.UDPPort,
		PublishUser: publishUser,
		PublishPass: e.pass,
		Log:         e.o.Log,
	})
	if err := e.srv.Start(); err != nil {
		e.srv = nil
		return startError(err, e.o)
	}
	return nil
}

// Port is the TCP port the server listens on.
func (e *Engine) Port() int { return e.srv.Port() }

// Host is the address shown to people in stream URLs.
func (e *Engine) Host() string { return displayHost(e.o.Bind) }

// URL is the address viewers open to watch the camera called name.
func (e *Engine) URL(name string) string {
	return fmt.Sprintf("rtsp://%s/%s", net.JoinHostPort(e.Host(), strconv.Itoa(e.Port())), name)
}

// StartCamera starts streaming cam. When info is nil the video is read first.
// It returns how the camera is sent.
func (e *Engine) StartCamera(ctx context.Context, cam config.Camera, info *camera.MediaInfo) (camera.Plan, error) {
	if info == nil {
		i, err := e.Probe(ctx, cam)
		if err != nil {
			return camera.Plan{}, err
		}
		info = &i
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return camera.Plan{}, errors.New("shutting down")
	}
	if _, ok := e.cams[cam.Name]; ok {
		return camera.Plan{}, fmt.Errorf("a camera named %q is already running", cam.Name)
	}

	publishHost := "127.0.0.1"
	if ip := net.ParseIP(e.o.Bind); ip != nil && !ip.IsUnspecified() {
		publishHost = e.o.Bind
	}
	u := url.URL{
		Scheme: "rtsp",
		User:   url.UserPassword(publishUser, e.pass),
		Host:   net.JoinHostPort(publishHost, strconv.Itoa(e.Port())),
		Path:   "/" + cam.Name,
	}
	plan, err := camera.BuildPlan(cam, *info, u.String(), e.o.Verbose)
	if err != nil {
		return camera.Plan{}, err
	}
	if err := e.srv.AddPath(cam.Name, !cam.Loop); err != nil {
		return camera.Plan{}, err
	}

	runner := &camera.Runner{
		Name:    cam.Name,
		FFmpeg:  e.ffmpeg,
		Args:    plan.Args,
		Loop:    cam.Loop,
		Verbose: e.o.Verbose,
		Log:     e.o.Log,
		Secret:  e.pass,
	}
	ctx, cancel := context.WithCancel(e.ctx)
	r := &running{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		runner.Run(ctx)
	}()
	e.cams[cam.Name] = r
	return plan, nil
}

// StopCamera stops a camera and takes its stream away. It returns once its
// ffmpeg has exited, so the name can be used again right away.
func (e *Engine) StopCamera(name string) {
	e.mu.Lock()
	r, ok := e.cams[name]
	delete(e.cams, name)
	e.mu.Unlock()
	if !ok {
		return
	}
	e.srv.RemovePath(name)
	r.cancel()
	<-r.done
}

// Status reports a camera's state.
func (e *Engine) Status(name string) CameraStatus {
	e.mu.Lock()
	r, ok := e.cams[name]
	e.mu.Unlock()
	if !ok {
		return CameraStatus{}
	}
	st := CameraStatus{Running: true}
	select {
	case <-r.done:
		st.Finished = true
	default:
	}
	st.Live, st.Viewers = e.srv.Status(name)
	return st
}

// Done returns a channel that is closed when the camera's runner ends: when
// a camera that plays once reaches the end, or when it is stopped. It is nil
// for a camera that isn't running.
func (e *Engine) Done(name string) <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r, ok := e.cams[name]; ok {
		return r.done
	}
	return nil
}

// Close stops every camera and then the server.
func (e *Engine) Close() {
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.closed = true
		cams := make([]*running, 0, len(e.cams))
		for _, r := range e.cams {
			cams = append(cams, r)
		}
		e.mu.Unlock()

		// Stop ffmpeg first so it can say goodbye to the server.
		if e.srv != nil {
			e.srv.BeginShutdown()
		}
		e.cancel()
		for _, r := range cams {
			<-r.done
		}
		if e.srv != nil {
			e.srv.Close()
		}
	})
}
