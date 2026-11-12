package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCLI(t *testing.T) {
	// Without videos, a terminal gets the interactive UI; tests have none.
	isInteractive = func() bool { return false }

	tests := []struct {
		args     []string
		code     int
		stdout   string
		stderr   string
		noStderr bool
	}{
		{args: []string{"--version"}, code: 0, stdout: "rtsp-streamer "},
		{args: []string{"-h"}, code: 0, stdout: "Usage:"},
		{args: []string{}, code: 2, stderr: "no video given"},
		{args: []string{"--nope"}, code: 2, stderr: "unknown flag: --nope"},
		{args: []string{"--port", "abc", "a.mp4"}, code: 2, stderr: "invalid argument"},
		{args: []string{"missing.mp4"}, code: 2, stderr: "video file not found"},
		{args: []string{"--ffmpeg-args", `"open`, "a.mp4"}, code: 2, stderr: "unclosed quote"},
		{args: []string{"-c", "/does/not/exist.yml"}, code: 2, stderr: "config file"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run(tt.args, &out, &errOut)
			if code != tt.code {
				t.Errorf("exit code %d, want %d (stderr: %s)", code, tt.code, errOut.String())
			}
			if tt.stdout != "" && !strings.Contains(out.String(), tt.stdout) {
				t.Errorf("stdout %q does not contain %q", out.String(), tt.stdout)
			}
			if tt.stderr != "" && !strings.Contains(errOut.String(), tt.stderr) {
				t.Errorf("stderr %q does not contain %q", errOut.String(), tt.stderr)
			}
		})
	}
}
