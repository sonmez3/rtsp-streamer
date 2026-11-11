package camera

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sonmez3/rtsp-streamer/internal/logx"
)

const (
	minBackoff = 1 * time.Second
	maxBackoff = 30 * time.Second
	// A run that lasted this long counts as healthy and resets the backoff.
	stableAfter = 30 * time.Second
	// How long ffmpeg gets to exit after SIGINT before it is killed.
	stopTimeout = 5 * time.Second
)

// Runner keeps one ffmpeg process alive for a camera.
type Runner struct {
	Name    string
	FFmpeg  string
	Args    []string
	Loop    bool
	Verbose bool
	Log     *logx.Logger
	// Secret is hidden from logged ffmpeg output (the publish password is
	// part of the URL ffmpeg prints in some errors).
	Secret string
}

// Run starts ffmpeg and restarts it, with a growing delay, whenever it exits
// unexpectedly. It returns when ctx is cancelled, or when the video has ended
// and looping is off.
func (r *Runner) Run(ctx context.Context) {
	backoff := minBackoff
	for {
		start := time.Now()
		lastLine, err := r.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil && !r.Loop {
			r.Log.Infof(r.Name, "video finished")
			return
		}

		if time.Since(start) > stableAfter {
			backoff = minBackoff
		}
		reason := "exited"
		if err != nil {
			reason = err.Error()
		}
		if lastLine != "" {
			reason += ": " + lastLine
		}
		r.Log.Warnf(r.Name, "ffmpeg stopped (%s), restarting in %s", reason, backoff)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// runOnce runs ffmpeg until it exits and returns its last output line.
func (r *Runner) runOnce(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, r.FFmpeg, r.Args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// Own process group: Ctrl+C in the terminal reaches only us, and we
		// stop ffmpeg ourselves in a controlled way.
		Setpgid: true,
		// If we die without cleaning up (e.g. SIGKILL), take ffmpeg with us.
		Pdeathsig: syscall.SIGKILL,
	}
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = stopTimeout

	var (
		mu   sync.Mutex
		last string
	)
	out := logx.NewLineWriter(func(line string) {
		if r.Secret != "" {
			line = strings.ReplaceAll(line, r.Secret, "***")
		}
		mu.Lock()
		last = line
		mu.Unlock()
		if r.Verbose {
			r.Log.Infof(r.Name, "ffmpeg: %s", line)
		} else {
			r.Log.Warnf(r.Name, "ffmpeg: %s", line)
		}
	})
	// A non-*os.File writer makes exec copy the output in its own goroutine and
	// wait for that copy in Wait, so ffmpeg's last lines are never lost.
	cmd.Stdout, cmd.Stderr = out, out

	err := cmd.Run()
	out.Flush()

	mu.Lock()
	defer mu.Unlock()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return last, errors.New(exitErr.ProcessState.String())
	}
	return last, err
}
