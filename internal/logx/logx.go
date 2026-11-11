// Package logx is a small line-oriented logger shared by the server, the
// camera runners and the CLI. All output goes through one mutex so lines from
// different goroutines never interleave.
package logx

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	cyan   = "\033[36m"
)

type Logger struct {
	mu    sync.Mutex
	out   io.Writer
	color bool
	now   func() time.Time
}

// New returns a logger writing to out. Colors are used only when color is true.
func New(out io.Writer, color bool) *Logger {
	return &Logger{out: out, color: color, now: time.Now}
}

// Discard returns a logger that drops everything (handy in tests).
func Discard() *Logger { return New(io.Discard, false) }

// ColorEnabled reports whether f is a terminal and NO_COLOR is not set.
func ColorEnabled(f *os.File) bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func (l *Logger) Infof(tag, format string, args ...any)  { l.log("INF", green, tag, format, args...) }
func (l *Logger) Warnf(tag, format string, args ...any)  { l.log("WRN", yellow, tag, format, args...) }
func (l *Logger) Errorf(tag, format string, args ...any) { l.log("ERR", red, tag, format, args...) }

// Printf writes text as is, without timestamp or level.
func (l *Logger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.out, format, args...)
}

// Style wraps s in the given ANSI style when colors are enabled.
func (l *Logger) Style(style, s string) string {
	if !l.color {
		return s
	}
	var code string
	switch style {
	case "bold":
		code = bold
	case "dim":
		code = dim
	case "green":
		code = green
	case "yellow":
		code = yellow
	case "red":
		code = red
	case "cyan":
		code = cyan
	default:
		return s
	}
	return code + s + reset
}

func (l *Logger) log(level, levelColor, tag, format string, args ...any) {
	ts := l.now().Format("15:04:05")
	msg := fmt.Sprintf(format, args...)

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.color {
		if tag != "" {
			tag = cyan + "[" + tag + "]" + reset + " "
		}
		fmt.Fprintf(l.out, "%s%s%s %s%s%s %s%s\n", dim, ts, reset, levelColor, level, reset, tag, msg)
		return
	}
	if tag != "" {
		tag = "[" + tag + "] "
	}
	fmt.Fprintf(l.out, "%s %s %s%s\n", ts, level, tag, msg)
}

// LineWriter is an io.Writer that cuts its input into lines and hands each
// non-empty line to emit. Both '\n' and '\r' end a line, because ffmpeg ends
// progress updates with '\r'. Lines longer than maxLine are emitted in pieces,
// so a writer that never sends a newline can't make the buffer grow forever.
type LineWriter struct {
	mu   sync.Mutex
	buf  []byte
	emit func(string)
}

const maxLine = 4096

func NewLineWriter(emit func(string)) *LineWriter {
	return &LineWriter{emit: emit}
}

func (w *LineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range p {
		if b == '\n' || b == '\r' {
			w.flushLocked()
			continue
		}
		w.buf = append(w.buf, b)
		if len(w.buf) >= maxLine {
			w.flushLocked()
		}
	}
	return len(p), nil
}

// Flush emits whatever is left in the buffer.
func (w *LineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushLocked()
}

func (w *LineWriter) flushLocked() {
	if len(w.buf) == 0 {
		return
	}
	line := string(w.buf)
	w.buf = w.buf[:0]
	w.emit(line)
}
