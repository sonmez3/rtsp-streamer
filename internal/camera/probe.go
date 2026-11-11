package camera

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// MediaInfo describes the first video and audio stream of a file.
type MediaInfo struct {
	VideoIndex int    // absolute stream index, used with -map
	VideoCodec string // ffprobe codec name: "h264", "hevc", "vp9", ...
	Width      int
	Height     int
	AudioIndex int    // -1 when the file has no audio
	AudioCodec string // empty when the file has no audio
}

// Probe runs ffprobe on path.
func Probe(ctx context.Context, ffprobe, path string) (MediaInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffprobe,
		"-v", "error",
		"-show_entries", "stream=index,codec_type,codec_name,width,height:stream_disposition=attached_pic",
		"-of", "json",
		path,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return MediaInfo{}, fmt.Errorf("can't read video file: %s", probeError(stderr.String(), path, err))
	}
	return parseProbe(stdout.Bytes())
}

func parseProbe(data []byte) (MediaInfo, error) {
	var out struct {
		Streams []struct {
			Index       int            `json:"index"`
			CodecType   string         `json:"codec_type"`
			CodecName   string         `json:"codec_name"`
			Width       int            `json:"width"`
			Height      int            `json:"height"`
			Disposition map[string]int `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return MediaInfo{}, fmt.Errorf("unexpected ffprobe output: %w", err)
	}
	info := MediaInfo{AudioIndex: -1}
	for _, s := range out.Streams {
		switch s.CodecType {
		case "video":
			// Skip cover art (an embedded still image), it is not the video.
			if info.VideoCodec != "" || s.Disposition["attached_pic"] == 1 {
				continue
			}
			info.VideoIndex, info.VideoCodec = s.Index, s.CodecName
			info.Width, info.Height = s.Width, s.Height
		case "audio":
			if info.AudioCodec == "" {
				info.AudioIndex, info.AudioCodec = s.Index, s.CodecName
			}
		}
	}
	if info.VideoCodec == "" {
		return MediaInfo{}, errors.New("file has no video stream")
	}
	return info, nil
}

// probeError keeps the last line of ffprobe's output, which says what went
// wrong, without the file path ffprobe puts in front of it.
func probeError(stderr, path string, err error) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	msg := strings.TrimSpace(lines[len(lines)-1])
	msg = strings.TrimPrefix(msg, path+": ")
	if msg == "" {
		return err.Error()
	}
	return msg
}
