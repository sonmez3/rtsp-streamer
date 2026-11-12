# rtsp-streamer

![CI](https://github.com/sonmez3/rtsp-streamer/actions/workflows/ci.yml/badge.svg)
![Release](https://github.com/sonmez3/rtsp-streamer/actions/workflows/release.yml/badge.svg)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A Linux command-line tool that streams video files as RTSP cameras. Each video becomes its own stream:

```
rtsp-streamer drone1=a.mp4 drone2=b.mp4

  ● drone1  rtsp://192.168.1.20:8554/drone1  a.mp4 · video h264 copy · audio aac copy
  ● drone2  rtsp://192.168.1.20:8554/drone2  b.mkv · video vp9 → h264 · audio none
```

- **One program:** the RTSP server is built in, no MediaMTX needed. The only dependency is `ffmpeg`.
- **Many cameras:** as many videos as you like, each at its own URL.
- **Interactive UI:** run it without arguments to pick videos, name the streams and start or stop them from the terminal.
- **Low CPU:** H.264/H.265 videos are sent without re-encoding; anything else is converted to H.264.
- **TCP and UDP:** viewers can receive RTSP over TCP or UDP.
- **Self-healing:** if ffmpeg crashes, it is restarted automatically.
- **Safe publishing:** only rtsp-streamer's own ffmpeg can publish to the cameras; nobody on the network can hijack a stream.

## Install

**Debian / Ubuntu** (installs ffmpeg automatically):

```bash
curl -LO https://github.com/sonmez3/rtsp-streamer/releases/latest/download/rtsp-streamer_linux_amd64.deb
sudo apt install ./rtsp-streamer_linux_amd64.deb
```

On ARM devices (Raspberry Pi, Jetson) use `arm64` on a 64-bit OS or `armv7` on a 32-bit OS instead of `amd64`.

**Fedora / RHEL** (install ffmpeg first, e.g. `sudo dnf install ffmpeg-free`):

```bash
sudo dnf install https://github.com/sonmez3/rtsp-streamer/releases/latest/download/rtsp-streamer_linux_amd64.rpm
```

**Other Linux distributions** (install `ffmpeg` with your package manager):

```bash
curl -LO https://github.com/sonmez3/rtsp-streamer/releases/latest/download/rtsp-streamer_linux_amd64.tar.gz
tar -xzf rtsp-streamer_linux_amd64.tar.gz
sudo install -m 755 rtsp-streamer_linux_amd64/rtsp-streamer /usr/local/bin/
```

**From source** (Go 1.26+):

```bash
go install github.com/sonmez3/rtsp-streamer/cmd/rtsp-streamer@latest
```

Check the install with `rtsp-streamer --version`.

Every release includes a `SHA256SUMS` file for verifying the downloads.

## Usage

```bash
rtsp-streamer                                 # interactive UI (see below)
rtsp-streamer video.mp4                       # rtsp://<ip>:8554/video
rtsp-streamer drone1=a.mp4 drone2=b.mp4       # two cameras, named by you
rtsp-streamer -c cameras.yml                  # cameras from a file
rtsp-streamer cam=a.mkv --codec h264 --bitrate 2M
rtsp-streamer --no-loop video.mp4             # play once, exit when done
```

On startup, each camera's URL is printed with the machine's network IP, so it can be opened directly from another computer on the same network. Press `Ctrl+C` to stop.

### Interactive UI

Run `rtsp-streamer` without any videos to open a live control panel in the terminal:

```
rtsp-streamer 0.2.0  ·  192.168.1.20  ·  port 8554 (TCP + UDP)  ·  2 of 3 live

  STATUS      NAME    URL                                VIEWERS  VIDEO
▸ ● live      drone1  rtsp://192.168.1.20:8554/drone1          1  a.mp4 · video h264 copy · audio aac copy
  ● live      drone2  rtsp://192.168.1.20:8554/drone2          0  b.mkv · video vp9 → h264 · audio none
  ■ stopped   gate    rtsp://192.168.1.20:8554/gate               gate.mp4

Log
10:41:02 INF [drone1] viewer 192.168.1.31 connected over UDP (1 watching)

a add · e edit · space start/stop · d delete · ↑↓ select · q quit
```

| Key | Action |
|---|---|
| `a` | Add a camera: pick a video in the file browser, then set its name (the last part of the URL) and options |
| `e` / `Enter` | Edit the selected camera; a running camera restarts with the new settings |
| `Space` | Start or stop the selected camera |
| `d` | Delete the selected camera |
| `q` / `Ctrl+C` | Stop every stream and quit |

In the camera form, `Tab` and the arrow keys move between fields, `Space` toggles options, `Enter` on the Video line opens the file browser and `Enter` elsewhere saves.

Cameras start as soon as they are added. The list is saved to `~/.config/rtsp-streamer/cameras.yml` and comes back the next time the UI opens, with stopped cameras still stopped. That file is a normal camera file, so `rtsp-streamer -c ~/.config/rtsp-streamer/cameras.yml` streams the same cameras without the UI. Server options such as `--port` and camera options such as `--no-audio` (the default for new cameras) work with the UI too.

### Options

| Option | Description | Default |
|---|---|---|
| `[name=]video` | Video to stream. Without a name, the name comes from the file name | — |
| `-c, --config FILE` | Read cameras and settings from a YAML file | — |
| `-p, --port N` | RTSP port | `8554` |
| `--bind ADDR` | Listen only on this address | all interfaces |
| `--udp-port N` | Ports for UDP viewers (RTP `N`, RTCP `N+1`) | `8000` |
| `--no-udp` | Viewers may only use TCP | — |
| `--codec MODE` | `auto`, `copy` or `h264` (see below) | `auto` |
| `--bitrate RATE` | H.264 bitrate, e.g. `800k`, `2M` (re-encodes) | — |
| `--preset NAME` | x264 preset | `ultrafast` |
| `--tune NAME` | x264 tune | `zerolatency` |
| `--no-loop` | Play each video once instead of looping | — |
| `--no-audio` | Don't send the audio track | — |
| `--ffmpeg-args ARGS` | Extra ffmpeg output arguments, e.g. `"-vf scale=1280:-2"` | — |
| `--ffmpeg PATH` | ffmpeg binary to use | from `PATH` |
| `-v, --verbose` | Show all ffmpeg messages | — |
| `--version` | Print the version | — |

**Codec modes**

- `auto`: H.264 and H.265 videos are sent as is (almost no CPU). Other codecs are converted to H.264. Giving `--bitrate` or a video filter such as `-vf` always converts.
- `copy`: never re-encode; fails if the source isn't H.264/H.265.
- `h264`: always convert to H.264. A keyframe is inserted every 2 seconds so new viewers get a picture quickly.

Audio is sent as is when it is AAC, Opus or G.711, otherwise it is converted to AAC.

### Camera file

For many cameras a YAML file is easier. Full example: [`examples/cameras.yml`](examples/cameras.yml). If you installed the `.deb`/`.rpm` package it is also at `/usr/share/doc/rtsp-streamer/cameras.example.yml`.

```yaml
port: 8554
cameras:
  - source: videos/drone1.mp4        # name: drone1
  - name: drone2
    source: videos/drone2.mkv
    codec: h264
    bitrate: 2M
    audio: false
  - name: gate
    source: ~/videos/gate.mp4
    loop: false
    ffmpeg_args: ["-vf", "scale=1280:-2"]
```

A camera with `enabled: false` stays in the file but isn't streamed. Relative paths are relative to the file's folder. Camera options on the command line (`--codec` etc.) are defaults for every camera; a value set for a camera in the file wins. Server options such as `--port` override the file.

## Watching

```bash
vlc rtsp://192.168.1.20:8554/drone1
ffplay rtsp://192.168.1.20:8554/drone1
ffplay -rtsp_transport tcp rtsp://192.168.1.20:8554/drone1   # force TCP
```

If a firewall is on, allow TCP `8554` and UDP `8000-8001`, for example `sudo ufw allow 8554/tcp && sudo ufw allow 8000:8001/udp`.

## Troubleshooting

| Message | Fix |
|---|---|
| `ffmpeg is not installed` | `sudo apt install ffmpeg` |
| `address already in use` | Another program (e.g. MediaMTX) is using the port. Stop it, or pick other ports with `--port` / `--udp-port` |
| `can't read video file` | The file is damaged or not a video |
| `the file is incomplete: the recording was cut off` | The recording stopped before it was saved properly, so the MP4 has no index. Recover it with a tool such as [untrunc](https://github.com/anthwlock/untrunc) |
| `codec copy only works with h264 or h265` | Use `--codec auto` or `--codec h264` |
| `ffmpeg stopped ..., restarting` | ffmpeg exited unexpectedly and is being restarted; use `--verbose` for details |
| Viewer connects but shows no picture | A firewall may be blocking UDP; force the player to TCP or use `--no-udp` |

## Development

```bash
go build -o rtsp-streamer ./cmd/rtsp-streamer
go test -race ./...        # the end-to-end tests need ffmpeg installed
```

Project layout:

```
cmd/rtsp-streamer/   command line
internal/config/     arguments + YAML → camera list
internal/server/     built-in RTSP server (gortsplib)
internal/camera/     ffprobe, ffmpeg arguments, loop that keeps ffmpeg running
internal/app/        engine that starts and stops cameras on the server; CLI startup
internal/tui/        interactive terminal UI (Bubble Tea)
```

### Releasing

Pushing a tag that starts with `v` makes GitHub Actions run the tests and use [GoReleaser](https://goreleaser.com) to upload `.deb`, `.rpm` and `.tar.gz` files (amd64, arm64, armv7) plus `SHA256SUMS` to the release:

```bash
git tag v0.1.0
git push origin v0.1.0
```

## License

[MIT](LICENSE)
