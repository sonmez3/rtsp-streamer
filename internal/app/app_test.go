package app

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sonmez3/rtsp-streamer/internal/config"
	"github.com/sonmez3/rtsp-streamer/internal/logx"
)

// These tests run the real ffmpeg. They are skipped when it isn't installed.

func requireFFmpeg(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
}

func makeVideo(t *testing.T, path string, seconds int, args ...string) {
	t.Helper()
	base := []string{"-v", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25",
		"-f", "lavfi", "-i", "sine", "-t", fmt.Sprint(seconds)}
	cmd := exec.Command("ffmpeg", append(append(base, args...), "-y", path)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("making test video: %v\n%s", err, out)
	}
}

func testCam(name, src string, loop bool) config.Camera {
	return config.Camera{
		Name: name, Source: src, Loop: loop, Codec: config.CodecAuto,
		Preset: config.DefaultPreset, Tune: config.DefaultTune, Audio: true,
	}
}

// freeUDPPair finds an even port p where p and p+1 are both free for UDP.
func freeUDPPair(t *testing.T) int {
	t.Helper()
	for p := 20000 + 2*(os.Getpid()%5000); p < 65000; p += 2 {
		a, err := net.ListenPacket("udp", fmt.Sprintf(":%d", p))
		if err != nil {
			continue
		}
		b, err := net.ListenPacket("udp", fmt.Sprintf(":%d", p+1))
		a.Close()
		if err != nil {
			continue
		}
		b.Close()
		return p
	}
	t.Fatal("no free UDP port pair")
	return 0
}

// probe reads a stream with ffprobe and returns its codec names.
func probe(t *testing.T, url, transport string) string {
	t.Helper()
	out, err := exec.Command("timeout", "15", "ffprobe", "-v", "error", "-rtsp_transport", transport,
		"-show_entries", "stream=codec_name", "-of", "csv=p=0", url).Output()
	if err != nil {
		t.Errorf("ffprobe %s over %s: %v", url, transport, err)
	}
	return strings.Join(strings.Fields(string(out)), " ")
}

type result struct {
	err error
	log string
}

// start runs the app in the background and waits until it is ready.
func start(t *testing.T, cfg *config.Config) (port int, stop func() result) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var buf bytes.Buffer
	ready := make(chan int, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Config:  cfg,
			Version: "test",
			Log:     logx.New(&buf, false),
			OnReady: func(p int) { ready <- p },
		})
	}()

	select {
	case port = <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("app exited early: %v\n%s", err, buf.String())
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("app not ready after 30s")
	}

	stopped := false
	var res result
	return port, func() result {
		if stopped {
			return res
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			res = result{err: err, log: buf.String()}
		case <-time.After(20 * time.Second):
			t.Fatal("app did not stop within 20s")
		}
		return res
	}
}

func TestTwoCamerasOverTCPAndUDP(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	a := filepath.Join(dir, "a.mp4")
	b := filepath.Join(dir, "b.mkv")
	makeVideo(t, a, 3, "-c:v", "libx264", "-c:a", "aac") // sent as is
	makeVideo(t, b, 3, "-c:v", "mpeg4", "-an")           // re-encoded, no audio

	cfg := &config.Config{
		UDP: true, UDPPort: freeUDPPair(t),
		Cameras: []config.Camera{testCam("one", a, true), testCam("two", b, true)},
	}
	port, stop := start(t, cfg)
	defer stop()

	base := fmt.Sprintf("rtsp://127.0.0.1:%d/", port)
	for _, tr := range []string{"tcp", "udp"} {
		if got := probe(t, base+"one", tr); got != "h264 aac" {
			t.Errorf("one over %s: got %q, want h264 aac", tr, got)
		}
		if got := probe(t, base+"two", tr); got != "h264" {
			t.Errorf("two over %s: got %q, want h264", tr, got)
		}
	}

	// Unknown paths are refused.
	if err := exec.Command("timeout", "10", "ffprobe", "-v", "quiet", base+"nope").Run(); err == nil {
		t.Error("unknown path should fail")
	}
	// Nobody but our own ffmpeg may publish.
	pub := exec.Command("timeout", "10", "ffmpeg", "-v", "error", "-re", "-i", a, "-c", "copy",
		"-f", "rtsp", "-rtsp_transport", "tcp", base+"one")
	if out, err := pub.CombinedOutput(); err == nil || !strings.Contains(string(out), "401") {
		t.Errorf("publishing without the password should get 401, got %v: %s", err, out)
	}

	res := stop()
	if res.err != nil {
		t.Errorf("Run returned %v", res.err)
	}
	for _, want := range []string{"one  rtsp://", "video h264 copy", "video mpeg4 → h264", "connected over UDP", "connected over TCP"} {
		if !strings.Contains(res.log, want) {
			t.Errorf("log is missing %q:\n%s", want, res.log)
		}
	}
	if strings.Contains(res.log, "ffmpeg stopped") || strings.Contains(res.log, "ERR") {
		t.Errorf("unexpected errors in log:\n%s", res.log)
	}
}

func TestNoLoopEndsByItself(t *testing.T) {
	requireFFmpeg(t)
	a := filepath.Join(t.TempDir(), "a.mp4")
	makeVideo(t, a, 2, "-c:v", "libx264", "-c:a", "aac")

	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), Options{
			Config: &config.Config{Cameras: []config.Camera{testCam("once", a, false)}},
			Log:    logx.New(&buf, false),
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("app did not stop after the video ended")
	}
	if !strings.Contains(buf.String(), "all videos finished") {
		t.Errorf("log:\n%s", buf.String())
	}
}

func TestBadVideoFailsBeforeStarting(t *testing.T) {
	requireFFmpeg(t)
	bad := filepath.Join(t.TempDir(), "bad.mp4")
	os.WriteFile(bad, []byte("not a video"), 0o644)
	err := Run(context.Background(), Options{
		Config: &config.Config{Cameras: []config.Camera{testCam("bad", bad, true)}},
		Log:    logx.Discard(),
	})
	if err == nil || !strings.Contains(err.Error(), "can't read video file") {
		t.Fatalf("got %v", err)
	}
}
