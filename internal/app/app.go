// Package app wires the RTSP server and the camera runners together.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sonmez3/rtsp-streamer/internal/camera"
	"github.com/sonmez3/rtsp-streamer/internal/config"
	"github.com/sonmez3/rtsp-streamer/internal/logx"
	"github.com/sonmez3/rtsp-streamer/internal/server"
)

type Options struct {
	Config  *config.Config
	FFmpeg  string // ffmpeg binary; empty = look up in PATH
	Verbose bool
	Version string
	Log     *logx.Logger

	// How long to wait for all cameras to go live before printing the summary.
	ReadyTimeout time.Duration
	// Called once all cameras are live (or ReadyTimeout passed), with the
	// actual RTSP port. Used by tests.
	OnReady func(port int)
}

// Run starts everything and blocks until ctx is cancelled or, when no camera
// loops, until every video has ended.
func Run(ctx context.Context, o Options) error {
	cfg, log := o.Config, o.Log
	if o.ReadyTimeout == 0 {
		o.ReadyTimeout = 15 * time.Second
	}

	ffmpeg, ffprobe, err := findFFmpeg(o.FFmpeg)
	if err != nil {
		return err
	}

	pass, err := randomHex(16)
	if err != nil {
		return err
	}
	const user = "publisher"

	names := make([]string, len(cfg.Cameras))
	once := make(map[string]bool)
	for i, c := range cfg.Cameras {
		names[i] = c.Name
		once[c.Name] = !c.Loop
	}

	// Read every video before opening any port, so a bad file stops us early.
	infos := make([]camera.MediaInfo, len(cfg.Cameras))
	for i, cam := range cfg.Cameras {
		infos[i], err = camera.Probe(ctx, ffprobe, cam.Source)
		if err != nil {
			return fmt.Errorf("camera %q (%s): %w", cam.Name, cam.Source, err)
		}
	}

	srv := server.New(server.Options{
		Bind:        cfg.Bind,
		Port:        cfg.Port,
		UDP:         cfg.UDP,
		UDPPort:     cfg.UDPPort,
		Names:       names,
		Once:        once,
		PublishUser: user,
		PublishPass: pass,
		Log:         log,
	})
	if err := srv.Start(); err != nil {
		return startError(err, cfg)
	}
	defer srv.Close()
	port := srv.Port()

	publishHost := "127.0.0.1"
	if ip := net.ParseIP(cfg.Bind); ip != nil && !ip.IsUnspecified() {
		publishHost = cfg.Bind
	}
	runners := make([]*camera.Runner, len(cfg.Cameras))
	plans := make([]camera.Plan, len(cfg.Cameras))
	for i, cam := range cfg.Cameras {
		u := url.URL{
			Scheme: "rtsp",
			User:   url.UserPassword(user, pass),
			Host:   net.JoinHostPort(publishHost, strconv.Itoa(port)),
			Path:   "/" + cam.Name,
		}
		plan, err := camera.BuildPlan(cam, infos[i], u.String(), o.Verbose)
		if err != nil {
			return fmt.Errorf("camera %q (%s): %w", cam.Name, cam.Source, err)
		}
		plans[i] = plan
		runners[i] = &camera.Runner{
			Name:    cam.Name,
			FFmpeg:  ffmpeg,
			Args:    plan.Args,
			Loop:    cam.Loop,
			Verbose: o.Verbose,
			Log:     log,
			Secret:  pass,
		}
	}

	runCtx, stopRunners := context.WithCancel(ctx)
	defer stopRunners()
	var wg sync.WaitGroup
	for _, r := range runners {
		wg.Go(func() { r.Run(runCtx) })
	}
	allDone := make(chan struct{})
	go func() { wg.Wait(); close(allDone) }()

	waitLive(ctx, srv, names, o.ReadyTimeout)
	if ctx.Err() == nil {
		printSummary(log, o.Version, cfg, plans, port, srv)
		if o.OnReady != nil {
			o.OnReady(port)
		}
	}

	select {
	case <-ctx.Done():
		log.Printf("\n")
		log.Infof("", "shutting down...")
	case <-allDone:
		log.Infof("", "all videos finished")
	}

	// Stop ffmpeg first so it can say goodbye to the server; the deferred
	// srv.Close runs after this.
	srv.BeginShutdown()
	stopRunners()
	wg.Wait()
	return nil
}

// waitLive waits until every camera is live, the timeout passes or ctx ends.
func waitLive(ctx context.Context, srv *server.Server, names []string, timeout time.Duration) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for _, n := range names {
		select {
		case <-srv.Live(n):
		case <-deadline.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

func printSummary(log *logx.Logger, version string, cfg *config.Config, plans []camera.Plan, port int, srv *server.Server) {
	host := displayHost(cfg.Bind)
	transports := "TCP"
	if cfg.UDP {
		transports = "TCP + UDP"
	}

	nameW, urlW := 0, 0
	urls := make([]string, len(cfg.Cameras))
	for i, c := range cfg.Cameras {
		urls[i] = fmt.Sprintf("rtsp://%s/%s", net.JoinHostPort(host, strconv.Itoa(port)), c.Name)
		nameW = max(nameW, len(c.Name))
		urlW = max(urlW, len(urls[i]))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n%s %s  ·  %d camera(s)  ·  port %d (%s)\n\n",
		log.Style("bold", "rtsp-streamer"), version, len(cfg.Cameras), port, transports)
	for i, c := range cfg.Cameras {
		status := log.Style("green", "●")
		if !srv.IsLive(c.Name) {
			status = log.Style("yellow", "○")
		}
		detail := fmt.Sprintf("%s · video %s · audio %s", filepath.Base(c.Source), plans[i].Video, plans[i].Audio)
		if !c.Loop {
			detail += " · once"
		}
		fmt.Fprintf(&b, "  %s %-*s  %s  %s\n", status,
			nameW, c.Name,
			log.Style("green", fmt.Sprintf("%-*s", urlW, urls[i])),
			log.Style("dim", detail))
	}
	for _, c := range cfg.Cameras {
		if !srv.IsLive(c.Name) {
			fmt.Fprintf(&b, "\n  %s some cameras are not live yet; see the messages above.\n", log.Style("yellow", "○"))
			break
		}
	}
	fmt.Fprintf(&b, "\nPress Ctrl+C to stop.\n\n")
	log.Printf("%s", b.String())
}

// displayHost picks the address to show in URLs: the bind address if one was
// given, otherwise this machine's LAN address so the URL works from elsewhere.
func displayHost(bind string) string {
	if ip := net.ParseIP(bind); ip != nil && !ip.IsUnspecified() {
		return bind
	}
	if ip := lanIP(); ip != "" {
		return ip
	}
	return "127.0.0.1"
}

// lanIP returns the first IPv4 address of an interface that is up and is not
// loopback or a container/VM bridge.
func lanIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	skip := []string{"docker", "br-", "veth", "virbr", "lxc", "lxd", "cni", "flannel", "podman", "vmnet", "vboxnet"}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		bridge := false
		for _, p := range skip {
			if strings.HasPrefix(iface.Name, p) {
				bridge = true
				break
			}
		}
		if bridge {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip4 := ipn.IP.To4(); ip4 != nil && !ip4.IsLinkLocalUnicast() {
					return ip4.String()
				}
			}
		}
	}
	return ""
}

func findFFmpeg(custom string) (ffmpeg, ffprobe string, err error) {
	if custom != "" {
		ffmpeg, err = exec.LookPath(custom)
		if err != nil {
			return "", "", fmt.Errorf("ffmpeg not found at %s", custom)
		}
		// Prefer the ffprobe that sits next to the given ffmpeg.
		if p, err := exec.LookPath(filepath.Join(filepath.Dir(ffmpeg), "ffprobe")); err == nil {
			return ffmpeg, p, nil
		}
	} else {
		ffmpeg, err = exec.LookPath("ffmpeg")
		if err != nil {
			return "", "", errors.New("ffmpeg is not installed (install it with: sudo apt install ffmpeg)")
		}
	}
	ffprobe, err = exec.LookPath("ffprobe")
	if err != nil {
		return "", "", errors.New("ffprobe is not installed; it comes with ffmpeg (sudo apt install ffmpeg)")
	}
	return ffmpeg, ffprobe, nil
}

func startError(err error, cfg *config.Config) error {
	if errors.Is(err, syscall.EADDRINUSE) {
		msg := fmt.Sprintf("can't start RTSP server: %v\n", err)
		msg += fmt.Sprintf("Another program (maybe MediaMTX or another rtsp-streamer) is using port %d", cfg.Port)
		if cfg.UDP {
			msg += fmt.Sprintf(" or UDP ports %d-%d", cfg.UDPPort, cfg.UDPPort+1)
		}
		msg += ". Stop it, or choose other ports with --port"
		if cfg.UDP {
			msg += " / --udp-port"
		}
		return errors.New(msg + ".")
	}
	if errors.Is(err, syscall.EACCES) {
		return fmt.Errorf("can't start RTSP server: %v (ports below 1024 need root; use a higher port)", err)
	}
	return fmt.Errorf("can't start RTSP server: %w", err)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
