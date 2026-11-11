package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// touch creates empty files in a temp dir and returns the dir.
func touch(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestParseCameraArg(t *testing.T) {
	dir := touch(t, "a=b.mp4")
	tests := []struct {
		arg, name, src string
		wantErr        bool
	}{
		{arg: "video.mp4", name: "video", src: "video.mp4"},
		{arg: "/x/Drone 1.mkv", name: "Drone-1", src: "/x/Drone 1.mkv"},
		{arg: "drone1=/x/a.mp4", name: "drone1", src: "/x/a.mp4"},
		{arg: "cam=a=b.mp4", name: "cam", src: "a=b.mp4"},
		// an existing file with '=' in its name is a path, not name=path
		{arg: filepath.Join(dir, "a=b.mp4"), name: "a-b", src: filepath.Join(dir, "a=b.mp4")},
		{arg: "bad name=a.mp4", wantErr: true},
		{arg: "cam=", wantErr: true},
		{arg: "", wantErr: true},
	}
	for _, tt := range tests {
		name, src, err := ParseCameraArg(tt.arg)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseCameraArg(%q): expected error", tt.arg)
			}
			continue
		}
		if err != nil || name != tt.name || src != tt.src {
			t.Errorf("ParseCameraArg(%q) = %q, %q, %v; want %q, %q", tt.arg, name, src, err, tt.name, tt.src)
		}
	}
}

func TestNameFromSource(t *testing.T) {
	for in, want := range map[string]string{
		"/a/b/video.mp4":       "video",
		"çekim 1.mp4":          "cekim-1",
		"Işık Gölü.mov":        "Isik-Golu",
		"日本.mp4":               "cam",
		"...mp4":               "cam",
		"/a/My.Clip.final.mov": "My.Clip.final",
		"-x-.mp4":              "x",
	} {
		if got := NameFromSource(in); got != want {
			t.Errorf("NameFromSource(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseBitrate(t *testing.T) {
	for in, want := range map[string]int64{"2M": 2_000_000, "800k": 800_000, "2.5M": 2_500_000, "1500000": 1_500_000} {
		got, err := ParseBitrate(in)
		if err != nil || got != want {
			t.Errorf("ParseBitrate(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "fast", "2G", "-1M", "0"} {
		if _, err := ParseBitrate(in); err == nil {
			t.Errorf("ParseBitrate(%q): expected error", in)
		}
	}
}

func TestBuildFromArgs(t *testing.T) {
	dir := touch(t, "a.mp4", "b.mp4")
	cfg, err := Build(nil, Overrides{}, BuiltinDefaults(), []string{
		filepath.Join(dir, "a.mp4"),
		"two=" + filepath.Join(dir, "b.mp4"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != DefaultPort || !cfg.UDP || cfg.UDPPort != DefaultUDPPort {
		t.Errorf("unexpected server defaults: %+v", cfg)
	}
	if len(cfg.Cameras) != 2 || cfg.Cameras[0].Name != "a" || cfg.Cameras[1].Name != "two" {
		t.Fatalf("unexpected cameras: %+v", cfg.Cameras)
	}
	c := cfg.Cameras[0]
	if !c.Loop || c.Codec != CodecAuto || !c.Audio || c.Preset != DefaultPreset || c.Tune != DefaultTune {
		t.Errorf("unexpected camera defaults: %+v", c)
	}
}

func TestBuildErrors(t *testing.T) {
	dir := touch(t, "a.mp4")
	a := filepath.Join(dir, "a.mp4")
	port := func(p int) *int { return &p }

	tests := map[string]struct {
		ov   Overrides
		d    func(*CameraDefaults)
		args []string
		want string
	}{
		"no cameras":   {want: "no video given"},
		"missing file": {args: []string{filepath.Join(dir, "nope.mp4")}, want: "not found"},
		"directory":    {args: []string{"x=" + dir}, want: "not a regular file"},
		"duplicate":    {args: []string{a, a}, want: `two cameras are named "a"`},
		"bad port":     {ov: Overrides{Port: port(70000)}, args: []string{a}, want: "invalid port"},
		"odd udp port": {ov: Overrides{UDPPort: port(8001)}, args: []string{a}, want: "even number"},
		"bad codec":    {d: func(d *CameraDefaults) { d.Codec = "vp9" }, args: []string{a}, want: "unknown codec"},
		"copy+bitrate": {d: func(d *CameraDefaults) { d.Codec, d.Bitrate = CodecCopy, "2M" }, args: []string{a}, want: "can't be used with codec copy"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			d := BuiltinDefaults()
			if tt.d != nil {
				tt.d(&d)
			}
			_, err := Build(nil, tt.ov, d, tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestOddUDPPortIgnoredWhenUDPOff(t *testing.T) {
	dir := touch(t, "a.mp4")
	off, p := false, 8001
	if _, err := Build(nil, Overrides{UDP: &off, UDPPort: &p}, BuiltinDefaults(), []string{filepath.Join(dir, "a.mp4")}); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFileAndPrecedence(t *testing.T) {
	dir := touch(t, "a.mp4", "b.mp4")
	yml := `
port: 9000
udp: false
cameras:
  - name: drone1
    source: a.mp4          # relative to the config file
    codec: h264
    bitrate: 2M
    ffmpeg_args: ["-vf", "scale=640:-2"]
  - source: b.mp4          # name comes from the file
    loop: false
    audio: false
`
	path := filepath.Join(dir, "cameras.yml")
	if err := os.WriteFile(path, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Command-line defaults apply where the file says nothing; the file wins
	// where it does. --port on the command line beats the file.
	d := BuiltinDefaults()
	d.Codec, d.Tune = CodecCopy, "film"
	cliPort := 7000
	cfg, err := Build(f, Overrides{Port: &cliPort}, d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 7000 || cfg.UDP {
		t.Errorf("server settings: port=%d udp=%v", cfg.Port, cfg.UDP)
	}
	c1, c2 := cfg.Cameras[0], cfg.Cameras[1]
	if c1.Name != "drone1" || c1.Source != filepath.Join(dir, "a.mp4") || c1.Codec != CodecH264 ||
		c1.Bitrate != "2M" || c1.Tune != "film" || len(c1.ExtraArgs) != 2 || !c1.Loop {
		t.Errorf("camera 1: %+v", c1)
	}
	if c2.Name != "b" || c2.Codec != CodecCopy || c2.Loop || c2.Audio {
		t.Errorf("camera 2: %+v", c2)
	}
}

func TestLoadFileRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yml")
	os.WriteFile(path, []byte("cameras:\n  - name: x\n    sorce: a.mp4\n"), 0o644)
	if _, err := LoadFile(path); err == nil || !strings.Contains(err.Error(), "sorce") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}
