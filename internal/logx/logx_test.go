package logx

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLineWriterSplitsOnCRAndLF(t *testing.T) {
	var got []string
	w := NewLineWriter(func(s string) { got = append(got, s) })

	// ffmpeg progress lines end with '\r'; writes can split lines anywhere.
	w.Write([]byte("frame=1\rframe=2\r"))
	w.Write([]byte("Input #0\nhal"))
	w.Write([]byte("f line\r\n\n"))
	w.Write([]byte("tail"))
	w.Flush()

	want := []string{"frame=1", "frame=2", "Input #0", "half line", "tail"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLineWriterCapsLongLines(t *testing.T) {
	var got []string
	w := NewLineWriter(func(s string) { got = append(got, s) })
	w.Write(bytes.Repeat([]byte("x"), maxLine*2+10))
	w.Flush()
	if len(got) != 3 || len(got[0]) != maxLine || len(got[2]) != 10 {
		t.Errorf("got %d pieces", len(got))
	}
}

func TestLoggerFormat(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, false)
	l.now = func() time.Time { return time.Date(2026, 1, 1, 12, 34, 56, 0, time.UTC) }
	l.Infof("cam1", "hello %d", 1)
	l.Warnf("", "no tag")
	want := "12:34:56 INF [cam1] hello 1\n12:34:56 WRN no tag\n"
	if buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
	if strings.Contains(l.Style("bold", "x"), "\033") {
		t.Error("no color expected")
	}
}
