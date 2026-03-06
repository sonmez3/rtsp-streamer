# streamer-cli

MediaMTX ve FFmpeg'i tek komutla yöneten Go CLI aracı. Video dosyasını RTSP protokolü üzerinden yayınlamak için her iki aracı otomatik olarak başlatır ve yönetir.

---

## Gereksinimler

Aşağıdaki araçların sisteminizde kurulu ve PATH'te erişilebilir olması gerekir.

### Go

```bash
# Ubuntu/Debian
sudo apt install golang-go

# Versiyon kontrolü
go version
```

### FFmpeg

```bash
sudo apt install ffmpeg

# Versiyon kontrolü
ffmpeg -version
```

### MediaMTX

Paket yöneticisinde bulunmadığı için manuel kurulum gerekir:

1. [MediaMTX Releases](https://github.com/bluenviron/mediamtx/releases) sayfasından sisteminize uygun sürümü indirin.
2. Arşivi açın ve binary'yi PATH'e taşıyın:

```bash
tar -xzf mediamtx_*_linux_amd64.tar.gz
sudo mv mediamtx /usr/local/bin/
```

3. Config dosyasını home dizinine kopyalayın:

```bash
mv mediamtx.yml ~/mediamtx.yml
```

4. Kurulumu doğrulayın:

```bash
which mediamtx   # /usr/local/bin/mediamtx
which ffmpeg     # /usr/bin/ffmpeg
```

---

## Kurulum

```bash
git clone https://github.com/sonmez3/streamer-cli.git
cd streamer-cli
go build -o streamer .
```

İsterseniz binary'yi PATH'e taşıyabilirsiniz:

```bash
sudo mv streamer /usr/local/bin/
```

---

## Kullanım

```bash
streamer -v <video_dosyası> [seçenekler]
```

### Seçenekler

| Parametre | Açıklama | Varsayılan |
|---|---|---|
| `-v <dosya>` | Yayınlanacak video dosyası **(zorunlu)** | — |
| `--host <adres>` | RTSP sunucu adresi | `127.0.0.1` |
| `--port <port>` | RTSP port | `8554` |
| `--path <path>` | Stream path | `/test` |
| `--preset <preset>` | FFmpeg x264 preset | `ultrafast` |
| `--tune <tune>` | FFmpeg x264 tune | `zerolatency` |
| `--loop` | Videoyu döngüye al | `true` |
| `--config <dosya>` | mediamtx.yml dosya yolu | `~/mediamtx.yml` |
| `--ffmpeg-extra <arg>` | Ekstra FFmpeg argümanı (tekrarlanabilir) | — |
| `-h` | Yardım ekranını göster | — |

---

## Örnekler

**Temel kullanım — varsayılan ayarlarla yayın:**
```bash
streamer -v /home/user/video.mp4
```

**Farklı bir adres ve path ile yayın:**
```bash
streamer -v video.mp4 --host 192.168.1.10 --port 8554 --path /drone1
```

**Döngüyü kapat:**
```bash
streamer -v video.mp4 --loop=false
```

**Özel mediamtx config dosyası:**
```bash
streamer -v video.mp4 --config /etc/mediamtx/mediamtx.yml
```

**Ekstra FFmpeg argümanları:**
```bash
streamer -v video.mp4 --ffmpeg-extra "-b:v 2M" --ffmpeg-extra "-maxrate 2M"
```

**Yardım:**
```bash
streamer -h
```

---

## Çalışma Mantığı

```
streamer başlatıldı
    │
    ├─▶ MediaMTX başlatılır (RTSP sunucu ayağa kalkar)
    │       1 saniye beklenir
    │
    ├─▶ FFmpeg başlatılır (video akışı başlar)
    │
    │   [MTX] MediaMTX log satırları
    │   [FF]  FFmpeg log satırları
    │
    └─▶ Ctrl+C
            │
            ├─▶ FFmpeg'e SIGTERM
            └─▶ MediaMTX'e SIGTERM
```

Başarılı bir başlatma sonrası terminalde şu bilgiler görünür:

```
=== streamer ===

  Video:   /home/user/video.mp4
  RTSP URL: rtsp://127.0.0.1:8554/test
  Preset:  ultrafast
  Tune:    zerolatency
  Loop:    true
  Config:  /home/user/mediamtx.yml

[INFO] Starting MediaMTX...
[INFO] MediaMTX started (PID 12345)
[MTX] INF MediaMTX v1.x.x
[INFO] Starting FFmpeg...
[INFO] FFmpeg started (PID 12346)

 Stream active: rtsp://127.0.0.1:8554/test
[INFO] Press Ctrl+C to stop.
```

---

## VLC ile izleme

```bash
vlc rtsp://127.0.0.1:8554/test
```

---

## Olası Hatalar

| Hata | Çözüm |
|---|---|
| `'mediamtx' not found in PATH` | MediaMTX kurulu değil veya PATH'te değil |
| `'ffmpeg' not found in PATH` | FFmpeg kurulu değil: `sudo apt install ffmpeg` |
| `Video file not found` | `-v` ile verilen dosya yolu hatalı |
| FFmpeg bağlanamıyor | MediaMTX henüz hazır değil, birkaç saniye bekleyip tekrar deneyin |
