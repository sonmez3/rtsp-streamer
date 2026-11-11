package camera

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/sonmez3/rtsp-streamer/internal/config"
)

// Plan is what will be sent for one camera and the ffmpeg command that does it.
type Plan struct {
	Video string   // "h264 copy", "hevc copy", "vp9 → h264"
	Audio string   // "aac copy", "mp3 → aac", "none"
	Args  []string // ffmpeg arguments (without the binary)
}

// Video codecs that RTSP viewers widely support and that can be sent without
// re-encoding.
var copyableVideo = map[string]bool{"h264": true, "hevc": true}

// Audio codecs that can be sent over RTSP without re-encoding.
var copyableAudio = map[string]bool{"aac": true, "opus": true, "pcm_alaw": true, "pcm_mulaw": true}

// Extra ffmpeg arguments that only work when the video is re-encoded. When
// one of them is present, codec "auto" encodes instead of copying.
var encodeOnlyArgs = []string{"-vf", "-filter:v", "-filter_complex", "-s", "-r", "-b:v", "-crf", "-g", "-pix_fmt"}

// BuildPlan decides how to send cam and builds the ffmpeg arguments that
// publish it to publishURL.
func BuildPlan(cam config.Camera, info MediaInfo, publishURL string, verbose bool) (Plan, error) {
	var p Plan

	logLevel := "warning"
	if verbose {
		logLevel = "info"
	}
	args := []string{
		"-hide_banner",
		"-nostdin", // never read the terminal; several ffmpegs would fight over it
		"-nostats", // no progress lines
		"-loglevel", logLevel,
		"-re", // read at the video's own speed, like a live camera
	}
	if cam.Loop {
		args = append(args, "-stream_loop", "-1")
	}
	args = append(args, "-i", cam.Source)

	// Only send the chosen streams; files often carry subtitles or data tracks
	// that RTSP can't carry.
	args = append(args, "-map", "0:"+strconv.Itoa(info.VideoIndex))

	encode, err := shouldEncode(cam, info)
	if err != nil {
		return Plan{}, err
	}
	if encode {
		p.Video = info.VideoCodec + " → h264"
		args = append(args,
			"-c:v", "libx264",
			"-preset", cam.Preset,
			"-tune", cam.Tune,
			"-pix_fmt", "yuv420p",
			// A keyframe every 2 seconds, so new viewers get a picture quickly.
			"-force_key_frames", "expr:gte(t,n_forced*2)",
		)
		if cam.Bitrate != "" {
			bps, err := config.ParseBitrate(cam.Bitrate)
			if err != nil {
				return Plan{}, err
			}
			args = append(args,
				"-b:v", cam.Bitrate,
				"-maxrate", cam.Bitrate,
				"-bufsize", strconv.FormatInt(bps*2, 10),
			)
		}
	} else {
		p.Video = info.VideoCodec + " copy"
		args = append(args, "-c:v", "copy")
	}

	switch {
	case !cam.Audio:
		p.Audio = "off"
		args = append(args, "-an")
	case info.AudioIndex < 0:
		p.Audio = "none"
		args = append(args, "-an")
	case copyableAudio[info.AudioCodec]:
		p.Audio = info.AudioCodec + " copy"
		args = append(args, "-map", "0:"+strconv.Itoa(info.AudioIndex), "-c:a", "copy")
	default:
		p.Audio = info.AudioCodec + " → aac"
		args = append(args, "-map", "0:"+strconv.Itoa(info.AudioIndex), "-c:a", "aac", "-b:a", "128k")
	}

	args = append(args, cam.ExtraArgs...)
	args = append(args, "-f", "rtsp", "-rtsp_transport", "tcp", publishURL)
	p.Args = args
	return p, nil
}

func shouldEncode(cam config.Camera, info MediaInfo) (bool, error) {
	switch cam.Codec {
	case config.CodecH264:
		return true, nil
	case config.CodecCopy:
		if !copyableVideo[info.VideoCodec] {
			return false, fmt.Errorf("video is %s; codec copy only works with h264 or h265 sources (use codec auto or h264)", info.VideoCodec)
		}
		return false, nil
	default: // auto
		if !copyableVideo[info.VideoCodec] || cam.Bitrate != "" {
			return true, nil
		}
		for _, a := range cam.ExtraArgs {
			if slices.Contains(encodeOnlyArgs, a) {
				return true, nil
			}
		}
		return false, nil
	}
}

// SplitArgs splits a command line the way a shell would: on spaces, keeping
// quoted parts together. Only plain quoting is supported, no variables.
func SplitArgs(s string) ([]string, error) {
	var (
		args    []string
		cur     strings.Builder
		inArg   bool
		quote   rune
		escaped bool
	)
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped, inArg = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inArg = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed quote in %q", s)
	}
	if escaped {
		return nil, fmt.Errorf("trailing backslash in %q", s)
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}
