package camera

import (
	"slices"
	"strings"
	"testing"

	"github.com/sonmez3/rtsp-streamer/internal/config"
)

func cam(mod func(*config.Camera)) config.Camera {
	c := config.Camera{
		Name: "cam", Source: "/v/a.mp4", Loop: true, Codec: config.CodecAuto,
		Preset: "ultrafast", Tune: "zerolatency", Audio: true,
	}
	if mod != nil {
		mod(&c)
	}
	return c
}

var h264aac = MediaInfo{VideoIndex: 0, VideoCodec: "h264", AudioIndex: 1, AudioCodec: "aac"}

const url = "rtsp://u:p@127.0.0.1:8554/cam"

func joined(p Plan) string { return strings.Join(p.Args, " ") }

func TestPlanCopiesH264(t *testing.T) {
	p, err := BuildPlan(cam(nil), h264aac, url, false)
	if err != nil {
		t.Fatal(err)
	}
	want := "-hide_banner -nostdin -nostats -loglevel warning -re -stream_loop -1 -i /v/a.mp4 " +
		"-map 0:0 -c:v copy -map 0:1 -c:a copy -f rtsp -rtsp_transport tcp " + url
	if got := joined(p); got != want {
		t.Errorf("args:\n got %s\nwant %s", got, want)
	}
	if p.Video != "h264 copy" || p.Audio != "aac copy" {
		t.Errorf("labels: %q %q", p.Video, p.Audio)
	}
}

func TestPlanEncodesWhenNeeded(t *testing.T) {
	vp9 := MediaInfo{VideoIndex: 2, VideoCodec: "vp9", AudioIndex: 0, AudioCodec: "mp3"}
	cases := map[string]struct {
		c    config.Camera
		info MediaInfo
	}{
		"non-copyable source": {cam(nil), vp9},
		"forced h264":         {cam(func(c *config.Camera) { c.Codec = config.CodecH264 }), h264aac},
		"bitrate":             {cam(func(c *config.Camera) { c.Bitrate = "2M" }), h264aac},
		"video filter arg":    {cam(func(c *config.Camera) { c.ExtraArgs = []string{"-vf", "scale=640:-2"} }), h264aac},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := BuildPlan(tc.c, tc.info, url, false)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(joined(p), "-c:v libx264 -preset ultrafast -tune zerolatency") {
				t.Errorf("expected x264 encode, got %s", joined(p))
			}
			if !strings.HasSuffix(p.Video, "→ h264") {
				t.Errorf("label %q", p.Video)
			}
		})
	}
}

func TestPlanBitrate(t *testing.T) {
	p, _ := BuildPlan(cam(func(c *config.Camera) { c.Bitrate = "2M" }), h264aac, url, false)
	if !strings.Contains(joined(p), "-b:v 2M -maxrate 2M -bufsize 4000000") {
		t.Errorf("args: %s", joined(p))
	}
}

func TestPlanCopyRejectsVP9(t *testing.T) {
	_, err := BuildPlan(cam(func(c *config.Camera) { c.Codec = config.CodecCopy }), MediaInfo{VideoCodec: "vp9", AudioIndex: -1}, url, false)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPlanAudio(t *testing.T) {
	noAudio := MediaInfo{VideoCodec: "h264", AudioIndex: -1}
	mp3 := MediaInfo{VideoCodec: "h264", AudioIndex: 1, AudioCodec: "mp3"}

	p, _ := BuildPlan(cam(nil), noAudio, url, false)
	if p.Audio != "none" || !slices.Contains(p.Args, "-an") {
		t.Errorf("no audio: %q %v", p.Audio, p.Args)
	}
	p, _ = BuildPlan(cam(func(c *config.Camera) { c.Audio = false }), h264aac, url, false)
	if p.Audio != "off" || !slices.Contains(p.Args, "-an") {
		t.Errorf("audio off: %q %v", p.Audio, p.Args)
	}
	p, _ = BuildPlan(cam(nil), mp3, url, false)
	if p.Audio != "mp3 → aac" || !strings.Contains(joined(p), "-map 0:1 -c:a aac") {
		t.Errorf("mp3: %q %v", p.Audio, p.Args)
	}
}

func TestPlanLoopAndVerbose(t *testing.T) {
	p, _ := BuildPlan(cam(func(c *config.Camera) { c.Loop = false }), h264aac, url, true)
	if slices.Contains(p.Args, "-stream_loop") {
		t.Error("no-loop should not use -stream_loop")
	}
	if !strings.Contains(joined(p), "-loglevel info") {
		t.Error("verbose should use -loglevel info")
	}
}

func TestPlanExtraArgsBeforeOutput(t *testing.T) {
	p, _ := BuildPlan(cam(func(c *config.Camera) { c.ExtraArgs = []string{"-metadata", "title=x"} }), h264aac, url, false)
	if !strings.HasSuffix(joined(p), "-metadata title=x -f rtsp -rtsp_transport tcp "+url) {
		t.Errorf("args: %s", joined(p))
	}
}

func TestSplitArgs(t *testing.T) {
	tests := map[string][]string{
		"-b:v 2M -maxrate 2M":               {"-b:v", "2M", "-maxrate", "2M"},
		`  -vf "drawtext=text='hi there'" `: {"-vf", "drawtext=text='hi there'"},
		`-metadata 'title=a b'`:             {"-metadata", "title=a b"},
		`a\ b c`:                            {"a b", "c"},
		`""`:                                {""},
		"":                                  nil,
	}
	for in, want := range tests {
		got, err := SplitArgs(in)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("SplitArgs(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{`"open`, `x\`} {
		if _, err := SplitArgs(in); err == nil {
			t.Errorf("SplitArgs(%q): expected error", in)
		}
	}
}

func TestParseProbe(t *testing.T) {
	data := `{"streams":[
		{"index":0,"codec_type":"video","codec_name":"mjpeg","width":300,"height":300,"disposition":{"attached_pic":1}},
		{"index":1,"codec_type":"audio","codec_name":"aac","disposition":{"attached_pic":0}},
		{"index":2,"codec_type":"video","codec_name":"hevc","width":1920,"height":1080,"disposition":{"attached_pic":0}},
		{"index":3,"codec_type":"subtitle","codec_name":"mov_text","disposition":{"attached_pic":0}}
	]}`
	info, err := parseProbe([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := MediaInfo{VideoIndex: 2, VideoCodec: "hevc", Width: 1920, Height: 1080, AudioIndex: 1, AudioCodec: "aac"}
	if info != want {
		t.Errorf("got %+v, want %+v", info, want)
	}

	if _, err := parseProbe([]byte(`{"streams":[{"index":0,"codec_type":"audio","codec_name":"aac"}]}`)); err == nil {
		t.Error("expected error for audio-only file")
	}
}

func TestProbeError(t *testing.T) {
	stderr := "[mov @ 0x1] moov atom not found\n/v/a.mp4: Invalid data found when processing input\n"
	if got := probeError(stderr, "/v/a.mp4", nil); got != "Invalid data found when processing input" {
		t.Errorf("got %q", got)
	}
}
