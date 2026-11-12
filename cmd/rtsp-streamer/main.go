// Command rtsp-streamer streams video files as RTSP cameras.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/pflag"

	"github.com/sonmez3/rtsp-streamer/internal/app"
	"github.com/sonmez3/rtsp-streamer/internal/camera"
	"github.com/sonmez3/rtsp-streamer/internal/config"
	"github.com/sonmez3/rtsp-streamer/internal/logx"
	"github.com/sonmez3/rtsp-streamer/internal/tui"
)

// Set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `rtsp-streamer streams video files as RTSP cameras.
Each video becomes its own stream: rtsp://<this-machine>:8554/<name>

Usage:
  rtsp-streamer [options]                      # interactive UI
  rtsp-streamer [options] [name=]video ...
  rtsp-streamer -c cameras.yml [options]

Without videos, rtsp-streamer opens an interactive UI where you pick videos
and start and stop streams. It remembers its cameras in
~/.config/rtsp-streamer/cameras.yml.

Examples:
  rtsp-streamer video.mp4                      # rtsp://<ip>:8554/video
  rtsp-streamer drone1=a.mp4 drone2=b.mp4      # two cameras
  rtsp-streamer -c cameras.yml                 # cameras from a file
  rtsp-streamer cam=a.mkv --codec h264 --bitrate 2M

Server:
  -c, --config FILE       Read cameras and settings from a YAML file
  -p, --port N            RTSP port (default 8554)
      --bind ADDR         Listen only on this address (default: all)
      --udp-port N        UDP ports for viewers: RTP N, RTCP N+1 (default 8000)
      --no-udp            Viewers may only use TCP

Cameras (apply to every camera unless the config file says otherwise):
      --codec MODE        auto: send H.264/H.265 as is, convert others (default)
                          copy: never re-encode
                          h264: always re-encode to H.264
      --bitrate RATE      H.264 bitrate, e.g. 800k or 2M (re-encodes)
      --preset NAME       x264 preset (default ultrafast)
      --tune NAME         x264 tune (default zerolatency)
      --no-loop           Play each video once instead of looping
      --no-audio          Drop the audio track
      --ffmpeg-args ARGS  Extra ffmpeg output arguments, e.g. "-vf scale=1280:-2"

Other:
      --ffmpeg PATH       ffmpeg binary to use (default: from PATH)
  -v, --verbose           Show all ffmpeg messages
      --version           Print the version
  -h, --help              Show this help
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(argv []string, stdout, stderr io.Writer) int {
	fs := pflag.NewFlagSet("rtsp-streamer", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	var (
		configPath  string
		port        int
		bind        string
		udpPort     int
		noUDP       bool
		codec       string
		bitrate     string
		preset      string
		tune        string
		noLoop      bool
		noAudio     bool
		ffmpegArgs  []string
		ffmpegPath  string
		verbose     bool
		showVersion bool
		help        bool
	)
	fs.StringVarP(&configPath, "config", "c", "", "")
	fs.IntVarP(&port, "port", "p", config.DefaultPort, "")
	fs.StringVar(&bind, "bind", "", "")
	fs.IntVar(&udpPort, "udp-port", config.DefaultUDPPort, "")
	fs.BoolVar(&noUDP, "no-udp", false, "")
	fs.StringVar(&codec, "codec", string(config.CodecAuto), "")
	fs.StringVar(&bitrate, "bitrate", "", "")
	fs.StringVar(&preset, "preset", config.DefaultPreset, "")
	fs.StringVar(&tune, "tune", config.DefaultTune, "")
	fs.BoolVar(&noLoop, "no-loop", false, "")
	fs.BoolVar(&noAudio, "no-audio", false, "")
	fs.StringArrayVar(&ffmpegArgs, "ffmpeg-args", nil, "")
	fs.StringVar(&ffmpegPath, "ffmpeg", "", "")
	fs.BoolVarP(&verbose, "verbose", "v", false, "")
	fs.BoolVar(&showVersion, "version", false, "")
	fs.BoolVarP(&help, "help", "h", false, "")

	if err := fs.Parse(argv); err != nil {
		fmt.Fprintf(stderr, "error: %v\nRun 'rtsp-streamer --help' for usage.\n", err)
		return 2
	}
	if help {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if showVersion {
		fmt.Fprintf(stdout, "rtsp-streamer %s\n", buildVersion())
		return 0
	}

	var file *config.File
	if configPath != "" {
		f, err := config.LoadFile(configPath)
		if err != nil {
			fmt.Fprintf(stderr, "error: config file: %v\n", err)
			return 2
		}
		file = f
	}

	var ov config.Overrides
	if fs.Changed("port") {
		ov.Port = &port
	}
	if fs.Changed("bind") {
		ov.Bind = &bind
	}
	if fs.Changed("udp-port") {
		ov.UDPPort = &udpPort
	}
	if fs.Changed("no-udp") {
		udp := !noUDP
		ov.UDP = &udp
	}

	defaults := config.CameraDefaults{
		Loop:    !noLoop,
		Codec:   config.Codec(codec),
		Preset:  preset,
		Tune:    tune,
		Bitrate: bitrate,
		Audio:   !noAudio,
	}
	for _, a := range ffmpegArgs {
		parts, err := camera.SplitArgs(a)
		if err != nil {
			fmt.Fprintf(stderr, "error: --ffmpeg-args: %v\n", err)
			return 2
		}
		defaults.ExtraArgs = append(defaults.ExtraArgs, parts...)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// After the first Ctrl+C, a second one quits immediately.
	go func() { <-ctx.Done(); stop() }()

	if file == nil && len(fs.Args()) == 0 && isInteractive() {
		return runUI(ctx, ov, defaults, ffmpegPath, verbose, stdout, stderr)
	}

	cfg, err := config.Build(file, ov, defaults, fs.Args())
	if err != nil {
		if errors.Is(err, config.ErrNoCameras) {
			fmt.Fprintf(stderr, "error: no video given\n\n%s", usage)
		} else {
			fmt.Fprintf(stderr, "error: %v\n", err)
		}
		return 2
	}

	log := logx.New(stdout, logx.ColorEnabled(os.Stdout))
	err = app.Run(ctx, app.Options{
		Config:  cfg,
		FFmpeg:  ffmpegPath,
		Verbose: verbose,
		Version: buildVersion(),
		Log:     log,
	})
	if err != nil {
		log.Errorf("", "%v", err)
		return 1
	}
	return 0
}

// isInteractive reports whether the UI can run: it needs a terminal for both
// input and output. Tests replace it.
var isInteractive = func() bool {
	return term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd())
}

// runUI runs the interactive UI with the camera list saved from last time.
func runUI(ctx context.Context, ov config.Overrides, defaults config.CameraDefaults, ffmpegPath string, verbose bool, stdout, stderr io.Writer) int {
	savePath, err := config.UIConfigPath()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	file := &config.File{}
	if _, err := os.Stat(savePath); err == nil {
		if file, err = config.LoadFile(savePath); err != nil {
			fmt.Fprintf(stderr, "error: saved camera list: %v\nFix or delete %s and try again.\n", err, savePath)
			return 2
		}
	}
	srv, err := config.BuildServer(file, ov)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	sink := tui.NewLogSink()
	log := logx.New(sink, false)
	eng, err := app.NewEngine(app.EngineOptions{
		Bind:    srv.Bind,
		Port:    srv.Port,
		UDP:     srv.UDP,
		UDPPort: srv.UDPPort,
		FFmpeg:  ffmpegPath,
		Verbose: verbose,
		Log:     log,
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if err := eng.Listen(); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	dir, _ := os.Getwd()
	err = tui.Run(ctx, tui.Options{
		Ctl:      eng,
		Log:      log,
		Version:  buildVersion(),
		Host:     eng.Host(),
		Port:     eng.Port(),
		UDP:      srv.UDP,
		File:     file,
		SavePath: savePath,
		Defaults: defaults,
		Dir:      dir,
	}, sink)
	sink.Detach(stderr)
	fmt.Fprintln(stdout, "Stopping all streams...")
	eng.Close()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func buildVersion() string {
	if version != "dev" {
		return version
	}
	// Installed with "go install ...@vX.Y.Z".
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}
