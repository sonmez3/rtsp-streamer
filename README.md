# rtsp-streamer

![CI](https://github.com/sonmez3/rtsp-streamer/actions/workflows/ci.yml/badge.svg)
![Release](https://github.com/sonmez3/rtsp-streamer/actions/workflows/release.yml/badge.svg)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A lightweight Linux CLI tool that turns video files into RTSP cameras. Built-in RTSP server, multiple streams, and an interactive terminal UI.

```bash
rtsp-streamer drone1=a.mp4 drone2=b.mp4
```

```
● drone1  rtsp://192.168.1.20:8554/drone1
● drone2  rtsp://192.168.1.20:8554/drone2
```

## Features

- Built-in RTSP server — no MediaMTX required.
- Multiple independent streams from video files.
- Interactive terminal UI to manage cameras.
- H.264/H.265 passthrough for minimal CPU usage.
- RTSP over TCP and UDP.
- Automatic restart on FFmpeg failure.
- YAML configuration and persistent camera settings.

## Installation

FFmpeg is the only runtime dependency.

### Debian / Ubuntu

```bash
curl -LO https://github.com/sonmez3/rtsp-streamer/releases/latest/download/rtsp-streamer_linux_amd64.deb
sudo apt install ./rtsp-streamer_linux_amd64.deb
```

### Fedora / RHEL

```bash
sudo dnf install ffmpeg-free
sudo dnf install https://github.com/sonmez3/rtsp-streamer/releases/latest/download/rtsp-streamer_linux_amd64.rpm
```

### From source

Requires Go 1.26+.

```bash
go install github.com/sonmez3/rtsp-streamer/cmd/rtsp-streamer@latest
```

For other Linux distributions and ARM devices, see [Releases](https://github.com/sonmez3/rtsp-streamer/releases/latest).

## Usage

```bash
# Interactive UI
rtsp-streamer

# Single camera
rtsp-streamer video.mp4

# Multiple cameras
rtsp-streamer drone1=a.mp4 drone2=b.mp4

# YAML configuration
rtsp-streamer -c cameras.yml

# Custom codec and bitrate
rtsp-streamer cam=video.mkv --codec h264 --bitrate 2M

# Play once
rtsp-streamer --no-loop video.mp4
```

Streams are available at `rtsp://<ip>:8554/<name>`. URLs are printed on startup.

### Interactive UI

Run `rtsp-streamer` without arguments to manage cameras from the terminal.

| Key | Action |
|---|---|
| `a` | Add camera |
| `e` / `Enter` | Edit camera |
| `Space` | Start / stop |
| `d` | Delete camera |
| `↑` / `↓` | Select camera |
| `q` | Quit |

Camera settings are automatically saved to `~/.config/rtsp-streamer/cameras.yml`.

### Options

| Option | Description |
|---|---|
| `-c, --config FILE` | Load YAML configuration |
| `-p, --port N` | RTSP port (default: `8554`) |
| `--bind ADDR` | Bind to a specific address |
| `--codec MODE` | `auto`, `copy`, `h264` |
| `--bitrate RATE` | Set H.264 bitrate |
| `--no-loop` | Play once |
| `--no-audio` | Disable audio |
| `--no-udp` | TCP-only streaming |
| `--ffmpeg-args ARGS` | Custom FFmpeg arguments |
| `-v, --verbose` | Enable verbose logging |
| `--version` | Print version |

By default, H.264/H.265 videos are streamed without re-encoding. Other codecs are converted to H.264.

### Configuration

```yaml
port: 8554
cameras:
  - name: drone1
    source: videos/drone1.mp4

  - name: drone2
    source: videos/drone2.mkv
    codec: h264
    bitrate: 2M
    audio: false
```

See [examples/cameras.yml](examples/cameras.yml) for the full configuration example.

## Watching streams

Open a stream using VLC, FFplay, or any RTSP-compatible client.

```bash
vlc rtsp://192.168.1.20:8554/drone1

ffplay -rtsp_transport tcp rtsp://192.168.1.20:8554/drone1
```

If a firewall is enabled, allow TCP port `8554` and UDP ports `8000–8001`.

## Development

```bash
git clone https://github.com/sonmez3/rtsp-streamer.git
cd rtsp-streamer

go build -o rtsp-streamer ./cmd/rtsp-streamer
go test -race ./...
```

Releases are built automatically through GitHub Actions and GoReleaser for `amd64`, `arm64`, and `armv7`.

## License

[MIT](LICENSE)
