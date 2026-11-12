package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/sonmez3/rtsp-streamer/internal/app"
	"github.com/sonmez3/rtsp-streamer/internal/camera"
	"github.com/sonmez3/rtsp-streamer/internal/config"
	"github.com/sonmez3/rtsp-streamer/internal/logx"
)

// fakeCtl stands in for the engine: cameras "run" as soon as they start.
type fakeCtl struct {
	mu      sync.Mutex
	running map[string]bool
	fail    map[string]error
	started []string
	stopped []string
}

func newFakeCtl() *fakeCtl {
	return &fakeCtl{running: map[string]bool{}, fail: map[string]error{}}
}

func (f *fakeCtl) StartCamera(_ context.Context, cam config.Camera, _ *camera.MediaInfo) (camera.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail[cam.Name]; err != nil {
		return camera.Plan{}, err
	}
	if f.running[cam.Name] {
		return camera.Plan{}, fmt.Errorf("%s is already running", cam.Name)
	}
	f.running[cam.Name] = true
	f.started = append(f.started, cam.Name)
	return camera.Plan{Video: "h264 copy", Audio: "none"}, nil
}

func (f *fakeCtl) StopCamera(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.running[name] {
		delete(f.running, name)
		f.stopped = append(f.stopped, name)
	}
}

func (f *fakeCtl) Status(name string) app.CameraStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running[name] {
		return app.CameraStatus{}
	}
	return app.CameraStatus{Running: true, Live: true, Viewers: 2}
}

type harness struct {
	t    *testing.T
	m    *Model
	ctl  *fakeCtl
	dir  string // holds the test videos
	save string // where the camera list is saved
}

func newHarness(t *testing.T, file *config.File) *harness {
	t.Helper()
	dir := t.TempDir()
	h := &harness{t: t, ctl: newFakeCtl(), dir: dir, save: filepath.Join(t.TempDir(), "cameras.yml")}
	h.m = New(Options{
		Ctl:      h.ctl,
		Log:      logx.Discard(),
		Version:  "test",
		Host:     "10.0.0.5",
		Port:     8554,
		UDP:      true,
		File:     file,
		SavePath: h.save,
		Defaults: config.BuiltinDefaults(),
		Dir:      dir,
	})
	h.send(tea.WindowSizeMsg{Width: 120, Height: 30})
	return h
}

// video creates an (empty) file in the test folder and returns its path.
func (h *harness) video(name string) string {
	p := filepath.Join(h.dir, name)
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		h.t.Fatal(err)
	}
	return p
}

// send delivers msg and then everything the resulting commands produce,
// the way Bubble Tea would. Ticks are dropped so tests don't wait.
func (h *harness) send(msg tea.Msg) {
	h.t.Helper()
	_, cmd := h.m.Update(msg)
	h.run(cmd)
}

func (h *harness) run(cmd tea.Cmd) {
	h.t.Helper()
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case nil, tickMsg:
	case tea.BatchMsg:
		for _, c := range msg {
			h.run(c)
		}
	default:
		h.send(msg)
	}
}

func (h *harness) press(keys ...string) {
	h.t.Helper()
	for _, k := range keys {
		h.send(keyMsg(k))
	}
}

func (h *harness) typeText(s string) { h.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}) }

func keyMsg(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

func (h *harness) view() string { return h.m.View() }

func (h *harness) saved() *config.File {
	h.t.Helper()
	f, err := config.LoadFile(h.save)
	if err != nil {
		h.t.Fatalf("reading the saved list: %v", err)
	}
	return f
}

func (h *harness) wantView(parts ...string) {
	h.t.Helper()
	v := h.view()
	for _, p := range parts {
		if !strings.Contains(v, p) {
			h.t.Errorf("screen is missing %q:\n%s", p, v)
		}
	}
}

func TestAddCameraWithTheFileBrowser(t *testing.T) {
	h := newHarness(t, nil)
	src := h.video("drone 1.mp4")
	h.video("notes.txt")
	h.wantView("No cameras yet")

	// "a" opens the file browser in the start folder; the video is first.
	h.press("a")
	if h.m.mode != modePicker {
		t.Fatalf("mode %v, want the file browser", h.m.mode)
	}
	h.wantView("Choose a video", "drone 1.mp4", "notes.txt")
	h.press("enter")
	if h.m.mode != modeForm || h.m.form.video.Value() != src {
		t.Fatalf("after choosing: mode %v, video %q", h.m.mode, h.m.form.video.Value())
	}
	// The name comes from the file until one is typed.
	h.wantView("rtsp://10.0.0.5:8554/drone-1")
	h.typeText("kam1")
	h.wantView("rtsp://10.0.0.5:8554/kam1")

	h.press("enter")
	if h.m.mode != modeList {
		t.Fatalf("mode %v after saving the form", h.m.mode)
	}
	if !slices.Equal(h.ctl.started, []string{"kam1"}) {
		t.Errorf("started %v", h.ctl.started)
	}
	h.wantView("● live", "kam1", "rtsp://10.0.0.5:8554/kam1", "drone 1.mp4 · video h264 copy", "1 of 1 live")

	f := h.saved()
	if len(f.Cameras) != 1 || f.Cameras[0].Name != "kam1" || f.Cameras[0].Source != src || f.Cameras[0].Enabled != nil {
		t.Errorf("saved %+v", f.Cameras)
	}
}

func TestBrowserRefusesFilesThatAreNotVideos(t *testing.T) {
	h := newHarness(t, nil)
	h.video("notes.txt")
	h.press("a", "enter")
	if h.m.mode != modePicker {
		t.Fatalf("mode %v; choosing a text file should keep the browser open", h.m.mode)
	}
	h.wantView("notes.txt doesn't look like a video file")
	h.press("esc")
	if h.m.mode != modeForm {
		t.Fatalf("esc in the browser: mode %v, want the form", h.m.mode)
	}
	h.press("esc")
	if h.m.mode != modeList || len(h.m.items) != 0 {
		t.Fatalf("esc in the form: mode %v, %d cameras", h.m.mode, len(h.m.items))
	}
}

func TestFormChecksWhatWasTyped(t *testing.T) {
	h := newHarness(t, nil)
	a := h.video("a.mp4")

	add := func(path, name string) {
		h.press("a", "esc") // skip the browser, type the path
		h.typeText(path)
		h.press("tab")
		h.typeText(name)
		h.press("enter")
	}
	add(a, "kam1")
	if len(h.m.items) != 1 {
		t.Fatalf("%d cameras", len(h.m.items))
	}

	add(a, "kam1")
	if h.m.mode != modeForm || !strings.Contains(h.m.form.err, `already called "kam1"`) {
		t.Fatalf("duplicate name: mode %v, err %q", h.m.mode, h.m.form.err)
	}
	h.press("esc")

	add(filepath.Join(h.dir, "missing.mp4"), "kam2")
	if h.m.mode != modeForm || !strings.Contains(h.m.form.err, "video file not found") {
		t.Fatalf("missing file: err %q", h.m.form.err)
	}
	h.press("esc")

	add(a, "bad name")
	if h.m.mode != modeForm || !strings.Contains(h.m.form.err, "invalid camera name") {
		t.Fatalf("bad name: err %q", h.m.form.err)
	}
	h.press("esc")

	// Codec copy can't be combined with a bitrate.
	h.press("a", "esc")
	h.typeText(a)
	h.press("tab")
	h.typeText("kam3")
	h.press("tab", "tab", "tab", "space") // Codec: auto -> copy
	h.press("tab")
	h.typeText("2M")
	h.press("enter")
	if h.m.mode != modeForm || !strings.Contains(h.m.form.err, "can't be used with codec copy") {
		t.Fatalf("copy + bitrate: err %q", h.m.form.err)
	}
}

func TestStopStartDeleteAreSaved(t *testing.T) {
	h := newHarness(t, nil)
	a, b := h.video("a.mp4"), h.video("b.mp4")
	h.press("a", "esc")
	h.typeText(a)
	h.press("enter", "enter") // Video -> Name, then save with the name from the file
	h.press("a", "esc")
	h.typeText(b)
	h.press("ctrl+s")
	if len(h.m.items) != 2 || h.m.cursor != 1 {
		t.Fatalf("%d cameras, cursor %d", len(h.m.items), h.m.cursor)
	}

	h.press("up", "space") // stop "a"
	if h.m.items[0].state != stateStopped || h.ctl.running["a"] {
		t.Fatalf("a: state %v, running %v", h.m.items[0].state, h.ctl.running["a"])
	}
	h.wantView("■ stopped")
	if e := h.saved().Cameras[0].Enabled; e == nil || *e {
		t.Errorf("a should be saved as stopped, got %v", e)
	}

	h.press("space") // start it again
	if h.m.items[0].state != stateRunning || h.saved().Cameras[0].Enabled != nil {
		t.Errorf("a: state %v", h.m.items[0].state)
	}

	h.press("d")
	h.wantView("Delete a? y = yes, n = no")
	h.press("n")
	if len(h.m.items) != 2 {
		t.Fatal("n should keep the camera")
	}
	h.press("d", "y")
	if len(h.m.items) != 1 || h.m.items[0].cam.Name != "b" || h.ctl.running["a"] {
		t.Fatalf("after delete: %d cameras, running %v", len(h.m.items), h.ctl.running)
	}
	if f := h.saved(); len(f.Cameras) != 1 || f.Cameras[0].Name != "b" {
		t.Errorf("saved %+v", f.Cameras)
	}
}

func TestSavedListIsRestored(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.mp4")
	os.WriteFile(a, nil, 0o644)
	port := 9554
	file := &config.File{
		Port: &port,
		Cameras: []config.FileCamera{
			{Name: "on", Source: a},
			{Name: "off", Source: a, Enabled: new(false)},
		},
	}
	h := newHarness(t, file)
	h.run(h.m.Init())

	if !slices.Equal(h.ctl.started, []string{"on"}) {
		t.Errorf("started %v, want only the enabled camera", h.ctl.started)
	}
	h.wantView("● live", "■ stopped")

	// Saving keeps the server settings that were in the file.
	h.press("down", "space")
	f := h.saved()
	if f.Port == nil || *f.Port != 9554 || len(f.Cameras) != 2 || f.Cameras[1].Enabled != nil {
		t.Errorf("saved port %v, cameras %+v", f.Port, f.Cameras)
	}
}

func TestStartErrorIsShown(t *testing.T) {
	h := newHarness(t, nil)
	src := h.video("broken.mp4")
	h.ctl.fail["broken"] = errors.New("can't read video file: the recording was cut off")
	h.press("a", "esc")
	h.typeText(src)
	h.press("ctrl+s")

	it := h.m.items[0]
	if it.state != stateFailed || !it.enabled {
		t.Fatalf("state %v, enabled %v", it.state, it.enabled)
	}
	h.wantView("✗ failed", "the recording was cut off")

	// Fixing the problem and pressing space again starts it.
	delete(h.ctl.fail, "broken")
	h.press("space")
	if it.state != stateRunning {
		t.Errorf("state %v after retry", it.state)
	}
}

func TestEditingARunningCameraRestartsIt(t *testing.T) {
	h := newHarness(t, nil)
	a := h.video("a.mp4")
	h.press("a", "esc")
	h.typeText(a)
	h.press("tab")
	h.typeText("kam1")
	h.press("enter")

	h.press("e")
	if h.m.mode != modeForm || h.m.form.focus != fieldName {
		t.Fatalf("e: mode %v", h.m.mode)
	}
	h.press("ctrl+u") // clear the name
	h.typeText("kam2")
	h.press("enter")

	if !slices.Equal(h.ctl.stopped, []string{"kam1"}) || !slices.Equal(h.ctl.started, []string{"kam1", "kam2"}) {
		t.Errorf("stopped %v, started %v", h.ctl.stopped, h.ctl.started)
	}
	h.wantView("rtsp://10.0.0.5:8554/kam2")

	// Saving without changes doesn't restart it.
	h.press("e", "enter")
	if len(h.ctl.started) != 2 {
		t.Errorf("an unchanged camera was restarted: %v", h.ctl.started)
	}
}

func TestScreenFitsTheTerminal(t *testing.T) {
	h := newHarness(t, nil)
	a := h.video("a.mp4")
	for i := range 15 {
		h.press("a", "esc")
		h.typeText(a)
		h.press("tab")
		h.typeText(fmt.Sprintf("cam%d", i))
		h.press("enter")
	}
	for i := range 100 {
		h.send(logMsg(fmt.Sprintf("12:00:00 INF [cam1] line %d", i)))
	}
	for _, size := range [][2]int{{120, 30}, {80, 24}, {60, 12}, {40, 8}} {
		h.send(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, open := range []string{"", "e", "a"} {
			h.m.mode = modeList
			if open != "" {
				h.press(open)
			}
			lines := strings.Split(h.view(), "\n")
			if len(lines) > size[1] {
				t.Errorf("%dx%d (%q): %d lines", size[0], size[1], open, len(lines))
			}
			for _, l := range lines {
				if w := ansi.StringWidth(l); w > size[0] {
					t.Errorf("%dx%d (%q): line is %d wide: %q", size[0], size[1], open, w, l)
				}
			}
		}
	}
	// The newest log lines are the ones shown.
	h.m.mode = modeList
	h.send(tea.WindowSizeMsg{Width: 120, Height: 30})
	h.wantView("line 99")
}

func TestLogSinkNeverBlocks(t *testing.T) {
	s := NewLogSink()
	done := make(chan struct{})
	go func() {
		// Nobody reads: the writes must still return.
		for i := range 5000 {
			fmt.Fprintf(s, "line %d\n", i)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writing to the sink blocked")
	}
	var out strings.Builder
	s.Detach(&out)
	fmt.Fprintln(s, "after")
	if !strings.Contains(out.String(), "line 0\n") || !strings.HasSuffix(out.String(), "after\n") {
		t.Errorf("detached output: %.200q", out.String())
	}
}
