# rtsp-streamer

![CI](https://github.com/sonmez3/rtsp-streamer/actions/workflows/ci.yml/badge.svg)
![Release](https://github.com/sonmez3/rtsp-streamer/actions/workflows/release.yml/badge.svg)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Video dosyalarını RTSP kamerası gibi yayınlayan Linux komut satırı aracı. Her video ayrı bir yayın olur:

```
rtsp-streamer drone1=a.mp4 drone2=b.mp4

  ● drone1  rtsp://192.168.1.20:8554/drone1  a.mp4 · video h264 copy · audio aac copy
  ● drone2  rtsp://192.168.1.20:8554/drone2  b.mkv · video vp9 → h264 · audio none
```

- **Tek program:** RTSP sunucusu içinde gömülü, MediaMTX gerekmez. Tek bağımlılık `ffmpeg`.
- **Çoklu kamera:** İstediğin kadar video, her biri kendi adresinde.
- **Düşük CPU:** H.264/H.265 videolar yeniden kodlanmadan gönderilir, diğerleri H.264'e çevrilir.
- **TCP ve UDP:** İzleyiciler RTSP'yi TCP veya UDP üzerinden alabilir.
- **Kendini toparlar:** ffmpeg çökerse kendiliğinden yeniden başlatılır.
- **Güvenli yayın:** Kameralara yalnızca programın kendi ffmpeg'i yayın basabilir; ağdan biri araya giremez.

## Kurulum

**Debian / Ubuntu** (ffmpeg otomatik kurulur):

```bash
curl -LO https://github.com/sonmez3/rtsp-streamer/releases/latest/download/rtsp-streamer_linux_amd64.deb
sudo apt install ./rtsp-streamer_linux_amd64.deb
```

ARM cihazlarda (Raspberry Pi, Jetson) `amd64` yerine 64-bit sistemde `arm64`, 32-bit sistemde `armv7` yaz.

**Fedora / RHEL** (önce ffmpeg kurulu olmalı, ör. `sudo dnf install ffmpeg-free`):

```bash
sudo dnf install https://github.com/sonmez3/rtsp-streamer/releases/latest/download/rtsp-streamer_linux_amd64.rpm
```

**Diğer Linux dağıtımları** (`ffmpeg` paket yöneticisinden kurulmalı):

```bash
curl -LO https://github.com/sonmez3/rtsp-streamer/releases/latest/download/rtsp-streamer_linux_amd64.tar.gz
tar -xzf rtsp-streamer_linux_amd64.tar.gz
sudo install -m 755 rtsp-streamer_linux_amd64/rtsp-streamer /usr/local/bin/
```

**Kaynak koddan** (Go 1.26+):

```bash
go install github.com/sonmez3/rtsp-streamer/cmd/rtsp-streamer@latest
```

Kontrol: `rtsp-streamer --version`

İndirilen dosyaları doğrulamak için her sürümde bir `SHA256SUMS` dosyası bulunur.

## Kullanım

```bash
rtsp-streamer video.mp4                       # rtsp://<ip>:8554/video
rtsp-streamer drone1=a.mp4 drone2=b.mp4       # iki kamera, adları sen veriyorsun
rtsp-streamer -c cameras.yml                  # kameralar dosyadan
rtsp-streamer cam=a.mkv --codec h264 --bitrate 2M
rtsp-streamer --no-loop video.mp4             # bir kez oynat, bitince çık
```

Başladığında her kameranın adresi makinenin ağ IP'siyle yazdırılır; aynı ağdaki başka bir bilgisayardan doğrudan açılabilir. Durdurmak için `Ctrl+C`.

### Seçenekler

| Seçenek | Açıklama | Varsayılan |
|---|---|---|
| `[ad=]video` | Yayınlanacak video. Ad verilmezse dosya adından türetilir | — |
| `-c, --config FILE` | Kameraları ve ayarları YAML dosyasından oku | — |
| `-p, --port N` | RTSP portu | `8554` |
| `--bind ADDR` | Sadece bu adreste dinle | tüm arayüzler |
| `--udp-port N` | UDP ile izleyenler için portlar (RTP `N`, RTCP `N+1`) | `8000` |
| `--no-udp` | İzleyiciler sadece TCP kullanabilsin | — |
| `--codec MODE` | `auto`, `copy` veya `h264` (aşağıya bak) | `auto` |
| `--bitrate RATE` | H.264 bit hızı, ör. `800k`, `2M` (yeniden kodlar) | — |
| `--preset NAME` | x264 preset | `ultrafast` |
| `--tune NAME` | x264 tune | `zerolatency` |
| `--no-loop` | Videoyu döngüye almadan bir kez oynat | — |
| `--no-audio` | Ses kanalını gönderme | — |
| `--ffmpeg-args ARGS` | Ek ffmpeg çıkış argümanları, ör. `"-vf scale=1280:-2"` | — |
| `--ffmpeg PATH` | Kullanılacak ffmpeg | `PATH`'teki |
| `-v, --verbose` | ffmpeg'in tüm mesajlarını göster | — |
| `--version` | Sürümü yazdır | — |

**Codec modları**

- `auto`: Video H.264 veya H.265 ise olduğu gibi gönderilir (CPU neredeyse sıfır). Değilse H.264'e çevrilir. `--bitrate` ya da `-vf` gibi bir video filtresi verilirse her zaman çevrilir.
- `copy`: Hiç yeniden kodlama yapılmaz; kaynak H.264/H.265 değilse hata verir.
- `h264`: Her zaman H.264'e çevrilir. Yeni izleyicinin hızlı görüntü alması için 2 saniyede bir anahtar kare eklenir.

Ses AAC, Opus veya G.711 ise olduğu gibi, değilse AAC'ye çevrilerek gönderilir.

### Kamera dosyası

Çok kamera için YAML dosyası daha rahattır. Tam örnek: [`examples/cameras.yml`](examples/cameras.yml). `.deb`/`.rpm` paketiyle kurduysan `/usr/share/doc/rtsp-streamer/cameras.example.yml` dosyasında da bulunur.

```yaml
port: 8554
cameras:
  - source: videos/drone1.mp4        # ad: drone1
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

Göreli yollar dosyanın bulunduğu klasöre göredir. Komut satırındaki kamera seçenekleri (`--codec` vb.) tüm kameralar için varsayılandır; dosyada bir kameraya özel yazılan değer önceliklidir. `--port` gibi sunucu seçenekleri dosyadakini ezer.

## İzleme

```bash
vlc rtsp://192.168.1.20:8554/drone1
ffplay rtsp://192.168.1.20:8554/drone1
ffplay -rtsp_transport tcp rtsp://192.168.1.20:8554/drone1   # TCP'ye zorla
```

Güvenlik duvarı açıksa şu portlara izin ver: TCP `8554` ve UDP `8000-8001`. Örneğin `sudo ufw allow 8554/tcp && sudo ufw allow 8000:8001/udp`.

## Sorun giderme

| Mesaj | Çözüm |
|---|---|
| `ffmpeg is not installed` | `sudo apt install ffmpeg` |
| `address already in use` | Port başka bir programda (ör. MediaMTX). Onu durdur ya da `--port` / `--udp-port` ile başka port seç |
| `can't read video file` | Dosya bozuk ya da video değil |
| `codec copy only works with h264 or h265` | `--codec auto` veya `--codec h264` kullan |
| `ffmpeg stopped ..., restarting` | ffmpeg beklenmedik şekilde kapandı ve yeniden başlatılıyor; ayrıntı için `--verbose` |
| İzleyici bağlanıyor ama görüntü yok | Güvenlik duvarı UDP'yi engelliyor olabilir; oynatıcıyı TCP'ye zorla ya da `--no-udp` kullan |

## Geliştirme

```bash
go build -o rtsp-streamer ./cmd/rtsp-streamer
go test -race ./...        # uçtan uca testler için ffmpeg kurulu olmalı
```

Proje yapısı:

```
cmd/rtsp-streamer/   komut satırı
internal/config/     argümanlar + YAML → kamera listesi
internal/server/     gömülü RTSP sunucusu (gortsplib)
internal/camera/     ffprobe, ffmpeg argümanları, ffmpeg'i ayakta tutan döngü
internal/app/        hepsini birleştiren başlatma/kapatma akışı
```

### Sürüm çıkarmak

`v` ile başlayan bir etiket gönderildiğinde GitHub Actions testleri çalıştırır ve [GoReleaser](https://goreleaser.com) ile `.deb`, `.rpm`, `.tar.gz` (amd64, arm64, armv7) ve `SHA256SUMS` dosyalarını release'e yükler:

```bash
git tag v0.1.0
git push origin v0.1.0
```

## Lisans

[MIT](LICENSE)
