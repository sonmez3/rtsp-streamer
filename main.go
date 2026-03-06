package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	colorReset  = "\033[0m"
	colorCyan   = "\033[36m"
	colorYellow = "\033[33m"
	colorGreen  = "\033[32m"
	colorRed    = "\033[31m"
	colorBold   = "\033[1m"
)

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, " ") }
func (m *multiFlag) Set(value string) error {
	*m = append(*m, value)
	return nil
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage:
  streamer -v <video> [options]

Description:
  Starts MediaMTX (RTSP server) and FFmpeg together.
  MediaMTX is started first, then FFmpeg begins streaming after 1 second.
  Press Ctrl+C to stop both processes cleanly.

Options:
  -v <file>          (required) Path to the video file to stream
  --host <addr>      RTSP server address            (default: 127.0.0.1)
  --port <port>      RTSP port                      (default: 8554)
  --path <path>      Stream path                    (default: /test)
  --preset <preset>  FFmpeg x264 preset             (default: ultrafast)
  --tune <tune>      FFmpeg x264 tune               (default: zerolatency)
  --loop             Loop the video indefinitely    (default: true)
  --config <file>    Path to mediamtx.yml           (default: ~/mediamtx.yml)
  --ffmpeg-extra     Extra FFmpeg argument, repeatable
  -h                 Show this help

Examples:
  # Stream a local file with defaults
  streamer -v /home/user/video.mp4

  # Custom RTSP path and host
  streamer -v video.mp4 --host 192.168.1.10 --port 8554 --path /drone1

  # Disable looping
  streamer -v video.mp4 --loop=false

  # Extra FFmpeg args
  streamer -v video.mp4 --ffmpeg-extra "-b:v 2M" --ffmpeg-extra "-maxrate 2M"

Stream URL will be printed on startup, e.g.:
  rtsp://127.0.0.1:8554/test

`)
}

func main() {
	var (
		video   string
		host    string
		port    int
		path    string
		preset  string
		tune    string
		loop    bool
		config  string
		extras  multiFlag
		help    bool
	)

	defaultConfig := filepath.Join(homeDir(), "mediamtx.yml")

	flag.StringVar(&video, "v", "", "Path to video file (required)")
	flag.StringVar(&host, "host", "127.0.0.1", "RTSP server address")
	flag.IntVar(&port, "port", 8554, "RTSP port")
	flag.StringVar(&path, "path", "/test", "Stream path")
	flag.StringVar(&preset, "preset", "ultrafast", "FFmpeg x264 preset")
	flag.StringVar(&tune, "tune", "zerolatency", "FFmpeg x264 tune")
	flag.BoolVar(&loop, "loop", true, "Loop video indefinitely")
	flag.StringVar(&config, "config", defaultConfig, "Path to mediamtx.yml")
	flag.Var(&extras, "ffmpeg-extra", "Extra FFmpeg argument (repeatable)")
	flag.BoolVar(&help, "h", false, "Show help")

	flag.Usage = func() { printUsage() }
	flag.Parse()

	if help {
		printUsage()
		os.Exit(0)
	}

	if video == "" {
		fmt.Fprintf(os.Stderr, "%s[ERROR]%s -v is required\n\n", colorRed, colorReset)
		printUsage()
		os.Exit(1)
	}

	video = expandHome(video)
	config = expandHome(config)

	if _, err := os.Stat(video); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "%s[ERROR]%s Video file not found: %s\n", colorRed, colorReset, video)
		os.Exit(1)
	}

	if _, err := exec.LookPath("mediamtx"); err != nil {
		fmt.Fprintf(os.Stderr, "%s[ERROR]%s 'mediamtx' not found in PATH. Install it and ensure it is in your PATH.\n", colorRed, colorReset)
		os.Exit(1)
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		fmt.Fprintf(os.Stderr, "%s[ERROR]%s 'ffmpeg' not found in PATH. Install it and ensure it is in your PATH.\n", colorRed, colorReset)
		os.Exit(1)
	}

	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	rtspURL := fmt.Sprintf("rtsp://%s:%d%s", host, port, path)

	fmt.Printf("\n%s%s=== streamer ===%s\n\n", colorBold, colorCyan, colorReset)
	fmt.Printf("  %sVideo:%s   %s\n", colorBold, colorReset, video)
	fmt.Printf("  %sRTSP URL:%s %s%s%s\n", colorBold, colorReset, colorGreen, rtspURL, colorReset)
	fmt.Printf("  %sPreset:%s  %s\n", colorBold, colorReset, preset)
	fmt.Printf("  %sTune:%s    %s\n", colorBold, colorReset, tune)
	fmt.Printf("  %sLoop:%s    %v\n", colorBold, colorReset, loop)
	fmt.Printf("  %sConfig:%s  %s\n", colorBold, colorReset, config)
	if len(extras) > 0 {
		fmt.Printf("  %sExtras:%s  %s\n", colorBold, colorReset, strings.Join(extras, " "))
	}
	fmt.Println()

	mtxCmd := exec.Command("mediamtx", config)

	ffArgs := []string{"-re"}
	if loop {
		ffArgs = append(ffArgs, "-stream_loop", "-1")
	}
	ffArgs = append(ffArgs, "-i", video)
	ffArgs = append(ffArgs, "-c:v", "libx264", "-preset", preset, "-tune", tune)
	ffArgs = append(ffArgs, extras...)
	ffArgs = append(ffArgs, "-f", "rtsp", rtspURL)
	ffCmd := exec.Command("ffmpeg", ffArgs...)

	if err := pipeOutput(mtxCmd, colorYellow+"[MTX]"+colorReset); err != nil {
		fmt.Fprintf(os.Stderr, "%s[ERROR]%s Failed to pipe MediaMTX output: %v\n", colorRed, colorReset, err)
		os.Exit(1)
	}

	fmt.Printf("%s[INFO]%s Starting MediaMTX...\n", colorCyan, colorReset)
	if err := mtxCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "%s[ERROR]%s Failed to start mediamtx: %v\n", colorRed, colorReset, err)
		os.Exit(1)
	}
	fmt.Printf("%s[INFO]%s MediaMTX started (PID %d)\n", colorCyan, colorReset, mtxCmd.Process.Pid)

	time.Sleep(1 * time.Second)

	if err := pipeOutput(ffCmd, colorGreen+"[FF] "+colorReset); err != nil {
		fmt.Fprintf(os.Stderr, "%s[ERROR]%s Failed to pipe FFmpeg output: %v\n", colorRed, colorReset, err)
		mtxCmd.Process.Signal(syscall.SIGTERM)
		os.Exit(1)
	}

	fmt.Printf("%s[INFO]%s Starting FFmpeg...\n", colorCyan, colorReset)
	if err := ffCmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "%s[ERROR]%s Failed to start ffmpeg: %v\n", colorRed, colorReset, err)
		mtxCmd.Process.Signal(syscall.SIGTERM)
		os.Exit(1)
	}
	fmt.Printf("%s[INFO]%s FFmpeg started (PID %d)\n", colorCyan, colorReset, ffCmd.Process.Pid)

	fmt.Printf("\n%s%s Stream active: %s%s\n", colorBold, colorGreen, rtspURL, colorReset)
	fmt.Printf("%s[INFO]%s Press Ctrl+C to stop.\n\n", colorCyan, colorReset)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	mtxDone := make(chan error, 1)
	ffDone := make(chan error, 1)

	go func() { mtxDone <- mtxCmd.Wait() }()
	go func() { ffDone <- ffCmd.Wait() }()

	select {
	case <-sigCh:
		fmt.Printf("\n%s[INFO]%s Caught signal, shutting down...\n", colorCyan, colorReset)
	case err := <-mtxDone:
		fmt.Printf("%s[WARN]%s MediaMTX exited unexpectedly: %v\n", colorYellow, colorReset, err)
	case err := <-ffDone:
		fmt.Printf("%s[WARN]%s FFmpeg exited unexpectedly: %v\n", colorYellow, colorReset, err)
	}

	shutdown("FFmpeg", ffCmd, ffDone)
	shutdown("MediaMTX", mtxCmd, mtxDone)

	fmt.Printf("%s[INFO]%s Done.\n", colorCyan, colorReset)
}

func pipeOutput(cmd *exec.Cmd, prefix string) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	go streamLines(stdout, prefix)
	go streamLines(stderr, prefix)
	return nil
}

func streamLines(r io.Reader, prefix string) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fmt.Printf("%s %s\n", prefix, scanner.Text())
	}
}

func shutdown(name string, cmd *exec.Cmd, done <-chan error) {
	if cmd.Process == nil {
		return
	}
	fmt.Printf("%s[INFO]%s Stopping %s (PID %d)...\n", colorCyan, colorReset, name, cmd.Process.Pid)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		return
	}
	select {
	case <-done:
		fmt.Printf("%s[INFO]%s %s stopped.\n", colorCyan, colorReset, name)
	case <-time.After(5 * time.Second):
		fmt.Printf("%s[WARN]%s %s did not stop in time, killing.\n", colorYellow, colorReset, name)
		cmd.Process.Kill()
	}
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), p[2:])
	}
	return p
}
