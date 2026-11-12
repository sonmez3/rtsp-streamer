// Package config turns command-line arguments and an optional YAML file into
// a validated list of cameras and server settings.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	DefaultPort    = 8554
	DefaultUDPPort = 8000
	DefaultPreset  = "ultrafast"
	DefaultTune    = "zerolatency"
)

// Codec is how a camera's video is sent.
type Codec string

const (
	CodecAuto Codec = "auto" // copy when the source is H.264/H.265, otherwise encode to H.264
	CodecCopy Codec = "copy" // never re-encode
	CodecH264 Codec = "h264" // always encode to H.264
)

// Config is the fully resolved configuration.
type Config struct {
	Port    int    // RTSP port (TCP)
	Bind    string // listen address, empty = all interfaces
	UDP     bool   // allow viewers to use RTSP over UDP
	UDPPort int    // RTP port for UDP viewers; RTCP uses UDPPort+1
	Cameras []Camera
}

type Camera struct {
	Name      string
	Source    string // absolute path to a video file
	Loop      bool
	Codec     Codec
	Preset    string
	Tune      string
	Bitrate   string // e.g. "2M"; empty = encoder default
	Audio     bool
	ExtraArgs []string // extra ffmpeg output arguments
}

// CameraDefaults are the per-camera settings given on the command line. They
// apply to every camera unless a camera in the YAML file sets its own value.
type CameraDefaults struct {
	Loop      bool
	Codec     Codec
	Preset    string
	Tune      string
	Bitrate   string
	Audio     bool
	ExtraArgs []string
}

func BuiltinDefaults() CameraDefaults {
	return CameraDefaults{
		Loop:   true,
		Codec:  CodecAuto,
		Preset: DefaultPreset,
		Tune:   DefaultTune,
		Audio:  true,
	}
}

// File is the YAML file format. Pointer fields tell "not set" apart from a
// zero value.
type File struct {
	Port    *int         `yaml:"port,omitempty"`
	Bind    *string      `yaml:"bind,omitempty"`
	UDP     *bool        `yaml:"udp,omitempty"`
	UDPPort *int         `yaml:"udp_port,omitempty"`
	Cameras []FileCamera `yaml:"cameras"`
}

type FileCamera struct {
	Name       string   `yaml:"name,omitempty"`
	Source     string   `yaml:"source"`
	Loop       *bool    `yaml:"loop,omitempty"`
	Codec      *string  `yaml:"codec,omitempty"`
	Preset     *string  `yaml:"preset,omitempty"`
	Tune       *string  `yaml:"tune,omitempty"`
	Bitrate    *string  `yaml:"bitrate,omitempty"`
	Audio      *bool    `yaml:"audio,omitempty"`
	FFmpegArgs []string `yaml:"ffmpeg_args,omitempty"`
	// Enabled false keeps the camera in the file without streaming it. The
	// interactive UI writes it for cameras that were stopped.
	Enabled *bool `yaml:"enabled,omitempty"`
}

// LoadFile reads a YAML config. Unknown keys are an error so typos don't
// silently fall back to defaults. Relative camera sources are resolved against
// the file's directory.
func LoadFile(path string) (*File, error) {
	data, err := os.ReadFile(ExpandHome(path))
	if err != nil {
		return nil, err
	}
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	dir := filepath.Dir(ExpandHome(path))
	for i := range f.Cameras {
		src := ExpandHome(f.Cameras[i].Source)
		if src != "" && !filepath.IsAbs(src) {
			src = filepath.Join(dir, src)
		}
		f.Cameras[i].Source = src
	}
	return &f, nil
}

// Overrides are server settings given on the command line. A nil field means
// the flag was not used.
type Overrides struct {
	Port    *int
	Bind    *string
	UDP     *bool
	UDPPort *int
}

// Build merges built-in defaults, the YAML file (may be nil), command-line
// overrides and positional camera arguments, then validates the result.
func Build(file *File, ov Overrides, defaults CameraDefaults, args []string) (*Config, error) {
	cfg := serverSettings(file, ov)

	disabled := 0
	if file != nil {
		for i, fc := range file.Cameras {
			if fc.Enabled != nil && !*fc.Enabled {
				disabled++
				continue
			}
			cam, err := CameraFromFile(fc, defaults, i)
			if err != nil {
				return nil, err
			}
			cfg.Cameras = append(cfg.Cameras, cam)
		}
	}
	if disabled > 0 && len(cfg.Cameras) == 0 && len(args) == 0 {
		return nil, errors.New("every camera in the config file has enabled: false")
	}

	for _, a := range args {
		name, src, err := ParseCameraArg(a)
		if err != nil {
			return nil, err
		}
		cam := cameraFromDefaults(defaults)
		cam.Name, cam.Source = name, src
		cfg.Cameras = append(cfg.Cameras, cam)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// BuildServer resolves only the server settings (no cameras): built-in
// defaults, then the file (may be nil), then command-line overrides.
func BuildServer(file *File, ov Overrides) (*Config, error) {
	cfg := serverSettings(file, ov)
	if err := cfg.validateServer(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func serverSettings(file *File, ov Overrides) *Config {
	cfg := &Config{Port: DefaultPort, UDP: true, UDPPort: DefaultUDPPort}
	if file != nil {
		setIf(&cfg.Port, file.Port)
		setIf(&cfg.Bind, file.Bind)
		setIf(&cfg.UDP, file.UDP)
		setIf(&cfg.UDPPort, file.UDPPort)
	}
	setIf(&cfg.Port, ov.Port)
	setIf(&cfg.Bind, ov.Bind)
	setIf(&cfg.UDP, ov.UDP)
	setIf(&cfg.UDPPort, ov.UDPPort)
	return cfg
}

// CameraFromFile turns one camera of a YAML file into a Camera. Fields the
// file doesn't set come from defaults. i is the camera's position in the
// file, for the error message. The result is not validated.
func CameraFromFile(fc FileCamera, defaults CameraDefaults, i int) (Camera, error) {
	cam := cameraFromDefaults(defaults)
	cam.Name = fc.Name
	cam.Source = fc.Source
	setIf(&cam.Loop, fc.Loop)
	if fc.Codec != nil {
		cam.Codec = Codec(*fc.Codec)
	}
	setIf(&cam.Preset, fc.Preset)
	setIf(&cam.Tune, fc.Tune)
	setIf(&cam.Bitrate, fc.Bitrate)
	setIf(&cam.Audio, fc.Audio)
	if fc.FFmpegArgs != nil {
		cam.ExtraArgs = fc.FFmpegArgs
	}
	if cam.Name == "" {
		if cam.Source == "" {
			return Camera{}, fmt.Errorf("camera #%d in config file has no name and no source", i+1)
		}
		cam.Name = NameFromSource(cam.Source)
	}
	return cam, nil
}

// ToFile is the reverse of CameraFromFile: it writes only the fields that
// differ from the built-in defaults, so saved files stay short.
func (c Camera) ToFile(enabled bool) FileCamera {
	fc := FileCamera{Name: c.Name, Source: c.Source}
	if !c.Loop {
		fc.Loop = new(false)
	}
	if c.Codec != "" && c.Codec != CodecAuto {
		fc.Codec = new(string(c.Codec))
	}
	if c.Preset != "" && c.Preset != DefaultPreset {
		fc.Preset = new(c.Preset)
	}
	if c.Tune != "" && c.Tune != DefaultTune {
		fc.Tune = new(c.Tune)
	}
	if c.Bitrate != "" {
		fc.Bitrate = new(c.Bitrate)
	}
	if !c.Audio {
		fc.Audio = new(false)
	}
	if len(c.ExtraArgs) > 0 {
		fc.FFmpegArgs = append([]string(nil), c.ExtraArgs...)
	}
	if !enabled {
		fc.Enabled = new(false)
	}
	return fc
}

const savedHeader = `# rtsp-streamer cameras, saved by the interactive UI (run rtsp-streamer
# without arguments). The same file also works with: rtsp-streamer -c <file>
`

// WriteFile saves f as YAML. It writes a temporary file and renames it over
// path, so a crash never leaves half a file behind.
func WriteFile(path string, f *File) error {
	var buf bytes.Buffer
	buf.WriteString(savedHeader)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// UIConfigPath is where the interactive UI keeps its camera list, normally
// ~/.config/rtsp-streamer/cameras.yml.
func UIConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "rtsp-streamer", "cameras.yml"), nil
}

// NewCamera returns a camera with these settings and no name or source yet.
func (d CameraDefaults) NewCamera() Camera { return cameraFromDefaults(d) }

func cameraFromDefaults(d CameraDefaults) Camera {
	return Camera{
		Loop:      d.Loop,
		Codec:     d.Codec,
		Preset:    d.Preset,
		Tune:      d.Tune,
		Bitrate:   d.Bitrate,
		Audio:     d.Audio,
		ExtraArgs: append([]string(nil), d.ExtraArgs...),
	}
}

func setIf[T any](dst *T, src *T) {
	if src != nil {
		*dst = *src
	}
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// ValidName reports whether s can be used as a stream name (the URL path).
func ValidName(s string) bool { return len(s) <= 64 && nameRe.MatchString(s) }

// ParseCameraArg parses "name=path" or "path". When no name is given it is
// derived from the file name. An argument that is itself an existing file is
// always taken as a path, even if it contains '='.
func ParseCameraArg(arg string) (name, source string, err error) {
	if arg == "" {
		return "", "", errors.New("empty camera argument")
	}
	if _, statErr := os.Stat(ExpandHome(arg)); statErr == nil || !strings.Contains(arg, "=") {
		return NameFromSource(arg), ExpandHome(arg), nil
	}
	name, source, _ = strings.Cut(arg, "=")
	if !ValidName(name) {
		return "", "", fmt.Errorf("invalid camera name %q in %q: use letters, digits, '-', '_' or '.'", name, arg)
	}
	if source == "" {
		return "", "", fmt.Errorf("camera %q has no video file", name)
	}
	return name, ExpandHome(source), nil
}

var nameCleanRe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// Letters with accents are replaced by their plain form so that file names
// like "çekim 1.mp4" give readable stream names ("cekim-1").
var asciiFold = strings.NewReplacer(
	"ç", "c", "Ç", "C", "ğ", "g", "Ğ", "G", "ı", "i", "İ", "I",
	"ö", "o", "Ö", "O", "ş", "s", "Ş", "S", "ü", "u", "Ü", "U",
	"â", "a", "Â", "A", "î", "i", "Î", "I", "û", "u", "Û", "U",
	"á", "a", "à", "a", "ä", "a", "é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ó", "o", "ò", "o", "ú", "u", "ù", "u", "ñ", "n", "ß", "ss",
)

// NameFromSource derives a stream name from a file path: "/x/Drone 1.mp4" -> "Drone-1".
func NameFromSource(src string) string {
	base := filepath.Base(src)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = asciiFold.Replace(base)
	name := strings.Trim(nameCleanRe.ReplaceAllString(base, "-"), "-._")
	if len(name) > 64 {
		name = name[:64]
	}
	if name == "" {
		return "cam"
	}
	return name
}

var bitrateRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[kKmM]?$`)

// ParseBitrate converts "2M", "800k" or "1500000" to bits per second.
func ParseBitrate(s string) (int64, error) {
	if !bitrateRe.MatchString(s) {
		return 0, fmt.Errorf("invalid bitrate %q (examples: 800k, 2M, 2.5M)", s)
	}
	mult := 1.0
	switch s[len(s)-1] {
	case 'k', 'K':
		mult, s = 1e3, s[:len(s)-1]
	case 'm', 'M':
		mult, s = 1e6, s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("invalid bitrate %q", s)
	}
	return int64(v * mult), nil
}

func (c *Config) validate() error {
	if len(c.Cameras) == 0 {
		return ErrNoCameras
	}
	if err := c.validateServer(); err != nil {
		return err
	}

	seen := map[string]string{}
	for i := range c.Cameras {
		cam := &c.Cameras[i]
		if prev, ok := seen[cam.Name]; ok {
			return fmt.Errorf("two cameras are named %q (%s and %s); give them different names with name=video", cam.Name, prev, cam.Source)
		}
		seen[cam.Name] = cam.Source
		if err := ValidateCamera(cam); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) validateServer() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("invalid port %d", c.Port)
	}
	if c.UDP {
		if c.UDPPort < 1 || c.UDPPort > 65534 || c.UDPPort%2 != 0 {
			return fmt.Errorf("invalid UDP port %d: must be an even number (RTP uses it, RTCP uses the next one)", c.UDPPort)
		}
	}
	return nil
}

// ValidateCamera checks one camera and fills in what can be derived: the
// absolute video path and the default x264 preset and tune.
func ValidateCamera(cam *Camera) error {
	if !ValidName(cam.Name) {
		return fmt.Errorf("invalid camera name %q: use letters, digits, '-', '_' or '.'", cam.Name)
	}
	if cam.Source == "" {
		return fmt.Errorf("camera %q has no source", cam.Name)
	}
	abs, err := filepath.Abs(ExpandHome(cam.Source))
	if err != nil {
		return err
	}
	cam.Source = abs
	fi, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("camera %q: video file not found: %s", cam.Name, abs)
		}
		return fmt.Errorf("camera %q: %w", cam.Name, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("camera %q: not a regular file: %s", cam.Name, abs)
	}

	switch cam.Codec {
	case CodecAuto, CodecCopy, CodecH264:
	default:
		return fmt.Errorf("camera %q: unknown codec %q (use auto, copy or h264)", cam.Name, cam.Codec)
	}
	if cam.Bitrate != "" {
		if cam.Codec == CodecCopy {
			return fmt.Errorf("camera %q: bitrate needs re-encoding, it can't be used with codec copy", cam.Name)
		}
		if _, err := ParseBitrate(cam.Bitrate); err != nil {
			return fmt.Errorf("camera %q: %w", cam.Name, err)
		}
	}
	if cam.Preset == "" {
		cam.Preset = DefaultPreset
	}
	if cam.Tune == "" {
		cam.Tune = DefaultTune
	}
	return nil
}

// ErrNoCameras is returned when no camera was given at all.
var ErrNoCameras = errors.New("no video given")

// ExpandHome replaces a leading "~" with the home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[1:])
		}
	}
	return p
}
