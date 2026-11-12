// Package tui is the interactive terminal UI: a live list of cameras that can
// be added, edited, started, stopped and deleted while the server runs.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sonmez3/rtsp-streamer/internal/app"
	"github.com/sonmez3/rtsp-streamer/internal/camera"
	"github.com/sonmez3/rtsp-streamer/internal/config"
	"github.com/sonmez3/rtsp-streamer/internal/logx"
)

// Controller is the part of the engine the UI uses.
type Controller interface {
	StartCamera(ctx context.Context, cam config.Camera, info *camera.MediaInfo) (camera.Plan, error)
	StopCamera(name string)
	Status(name string) app.CameraStatus
}

type Options struct {
	Ctl     Controller
	Log     *logx.Logger // the engine's logger; the UI reports start errors with it
	Version string
	Host    string // address shown in stream URLs
	Port    int
	UDP     bool

	// The saved camera list. Its server settings are kept when it is saved.
	File     *config.File
	SavePath string // where the list is saved; empty = don't save
	// Settings for new cameras (from the command-line flags).
	Defaults config.CameraDefaults
	// Folder the file browser starts in.
	Dir string
}

// Run shows the UI until the user quits or ctx is cancelled.
func Run(ctx context.Context, o Options, sink *LogSink) error {
	p := tea.NewProgram(New(o), tea.WithAltScreen(), tea.WithContext(ctx))
	stop := make(chan struct{})
	go sink.forward(p.Send, stop)
	_, err := p.Run()
	close(stop)
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}

// LogSink receives the engine's log while the UI runs and hands each line to
// the UI. Lines written before the UI starts are kept until it does. Writing
// never blocks, so a slow screen can't stall ffmpeg.
type LogSink struct {
	lw *logx.LineWriter

	mu  sync.Mutex
	ch  chan string // lines on their way to the UI
	out io.Writer   // set by Detach
}

func NewLogSink() *LogSink {
	s := &LogSink{ch: make(chan string, 1000)}
	s.lw = logx.NewLineWriter(s.emit)
	return s
}

func (s *LogSink) Write(p []byte) (int, error) { return s.lw.Write(p) }

func (s *LogSink) emit(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.out != nil {
		fmt.Fprintln(s.out, line)
		return
	}
	select {
	case s.ch <- line:
	default: // the UI is far behind; losing a line beats blocking
	}
}

func (s *LogSink) forward(send func(tea.Msg), stop <-chan struct{}) {
	for {
		select {
		case line := <-s.ch:
			send(logMsg(line))
		case <-stop:
			return
		}
	}
}

// Detach sends later lines to w, for use after the UI has exited. Lines the
// UI never got are written to w first.
func (s *LogSink) Detach(w io.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out = w
	for {
		select {
		case line := <-s.ch:
			fmt.Fprintln(w, line)
		default:
			return
		}
	}
}
