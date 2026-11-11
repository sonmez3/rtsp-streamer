#!/usr/bin/env bash
# Installs a static ffmpeg + ffprobe build from FFmpeg-Builds (hosted on
# GitHub) and puts it on PATH for the following workflow steps.
#
# CI uses this instead of apt so the tests and the release don't depend on
# the Ubuntu package mirrors being reachable from GitHub's runners.
set -euo pipefail

name=ffmpeg-n8.1-latest-linux64-gpl-8.1
base=https://github.com/FFmpeg-Builds/FFmpeg-Builds/releases/download/latest
dir="${RUNNER_TEMP:-/tmp}/ffmpeg"

mkdir -p "$dir"
cd "$dir"
curl -fsSL --retry 3 -O "$base/$name.tar.xz"
curl -fsSL --retry 3 -O "$base/checksums.sha256"
grep "  $name.tar.xz\$" checksums.sha256 | sha256sum -c -
tar -xf "$name.tar.xz"

echo "$dir/$name/bin" >> "$GITHUB_PATH"
"$dir/$name/bin/ffmpeg" -hide_banner -version | head -1
