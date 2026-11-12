package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sonmez3/rtsp-streamer/internal/logx"
)

func newTestEngine(t *testing.T) (*Engine, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	e, err := NewEngine(EngineOptions{Log: logx.New(&buf, false)})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e, &buf
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestEngineStartsAndStopsCamerasWhileRunning(t *testing.T) {
	requireFFmpeg(t)
	a := filepath.Join(t.TempDir(), "a.mp4")
	makeVideo(t, a, 3, "-c:v", "libx264", "-c:a", "aac")

	e, log := newTestEngine(t)
	ctx := context.Background()
	base := fmt.Sprintf("rtsp://127.0.0.1:%d/", e.Port())

	plan, err := e.StartCamera(ctx, testCam("one", a, true), nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Video != "h264 copy" {
		t.Errorf("plan.Video = %q", plan.Video)
	}
	waitFor(t, "one to go live", func() bool { return e.Status("one").Live })
	if got := probe(t, base+"one", "tcp"); got != "h264 aac" {
		t.Errorf("one: got %q", got)
	}

	if _, err := e.StartCamera(ctx, testCam("one", a, true), nil); err == nil {
		t.Error("starting a second camera called one should fail")
	}

	// Stopping takes the stream away at once, and the name can be used again.
	e.StopCamera("one")
	if st := e.Status("one"); st.Running || st.Live {
		t.Errorf("status after stop: %+v", st)
	}
	if err := exec.Command("timeout", "10", "ffprobe", "-v", "quiet", base+"one").Run(); err == nil {
		t.Error("a stopped camera should not be reachable")
	}
	if _, err := e.StartCamera(ctx, testCam("one", a, true), nil); err != nil {
		t.Fatalf("starting one again: %v", err)
	}
	if _, err := e.StartCamera(ctx, testCam("two", a, true), nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both to go live", func() bool { return e.Status("one").Live && e.Status("two").Live })
	if got := probe(t, base+"two", "tcp"); got != "h264 aac" {
		t.Errorf("two: got %q", got)
	}

	e.Close()
	for _, bad := range []string{"stream offline", "ffmpeg stopped", "ERR"} {
		if strings.Contains(log.String(), bad) {
			t.Errorf("log should not contain %q:\n%s", bad, log.String())
		}
	}
}

func TestEngineReportsFinishedCameras(t *testing.T) {
	requireFFmpeg(t)
	a := filepath.Join(t.TempDir(), "a.mp4")
	makeVideo(t, a, 1, "-c:v", "libx264", "-c:a", "aac")

	e, _ := newTestEngine(t)
	if _, err := e.StartCamera(context.Background(), testCam("once", a, false), nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.Done("once"):
	case <-time.After(20 * time.Second):
		t.Fatal("the camera did not finish")
	}
	if st := e.Status("once"); !st.Running || !st.Finished || st.Live {
		t.Errorf("status: %+v", st)
	}
}

func TestEngineRejectsBadVideo(t *testing.T) {
	requireFFmpeg(t)
	bad := filepath.Join(t.TempDir(), "bad.mp4")
	makeVideo(t, bad, 1, "-c:v", "libx264", "-an")
	// Cut the file in half, like a recording that was interrupted: its index
	// (the moov box, written last) is gone.
	fi, err := os.Stat(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(bad, fi.Size()/2); err != nil {
		t.Fatal(err)
	}

	e, _ := newTestEngine(t)
	_, err = e.StartCamera(context.Background(), testCam("bad", bad, true), nil)
	if err == nil || !strings.Contains(err.Error(), "recording was cut off") {
		t.Fatalf("got %v", err)
	}
	if e.Status("bad").Running {
		t.Error("a camera that failed to start should not be running")
	}
}
