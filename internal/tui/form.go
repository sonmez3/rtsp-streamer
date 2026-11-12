package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/sonmez3/rtsp-streamer/internal/config"
)

const (
	fieldVideo = iota
	fieldName
	fieldLoop
	fieldAudio
	fieldCodec
	fieldBitrate
	numFields
)

var codecs = []config.Codec{config.CodecAuto, config.CodecCopy, config.CodecH264}

var codecHelp = map[config.Codec]string{
	config.CodecAuto: "send H.264/H.265 as is, convert anything else to H.264",
	config.CodecCopy: "never re-encode (only works for H.264/H.265 videos)",
	config.CodecH264: "always re-encode to H.264 (uses more CPU)",
}

// The file browser only lets you pick files with these extensions.
var videoExts = func() []string {
	exts := []string{".mp4", ".mkv", ".mov", ".avi", ".ts", ".m2ts", ".mts", ".m4v", ".webm",
		".flv", ".wmv", ".mpg", ".mpeg", ".3gp", ".mxf", ".ogv", ".h264", ".264", ".h265", ".265", ".hevc"}
	for _, e := range slices.Clone(exts) {
		exts = append(exts, strings.ToUpper(e))
	}
	return exts
}()

const labelW = 11 // "▸ Bitrate  "

type form struct {
	editing *item         // nil when adding a camera
	base    config.Camera // keeps the settings the form doesn't show

	video, name, bitrate textinput.Model
	loop, audio          bool
	codec                config.Codec
	focus                int
	err                  string
}

func newInput(placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Placeholder = placeholder
	ti.Cursor.SetMode(cursor.CursorStatic)
	return ti
}

func newForm(editing *item, cam config.Camera, width int) *form {
	f := &form{
		editing: editing,
		base:    cam,
		video:   newInput("path to a video file"),
		name:    newInput(""),
		bitrate: newInput("keep the video's own quality"),
		loop:    cam.Loop,
		audio:   cam.Audio,
		codec:   cam.Codec,
	}
	if f.codec == "" {
		f.codec = config.CodecAuto
	}
	f.video.SetValue(cam.Source)
	f.name.SetValue(cam.Name)
	f.bitrate.SetValue(cam.Bitrate)
	f.setWidth(width)
	f.nameFollowsVideo()
	if editing != nil {
		f.setFocus(fieldName)
	} else {
		f.setFocus(fieldVideo)
	}
	return f
}

func (f *form) setWidth(w int) {
	inputW := max(min(w-labelW-26, 80), 10) // leaves room for the hint after it
	f.video.Width, f.name.Width, f.bitrate.Width = inputW, inputW, inputW
}

// nameFollowsVideo shows the name derived from the video file as a
// placeholder; it is used when the name field is left empty.
func (f *form) nameFollowsVideo() {
	if src := strings.TrimSpace(f.video.Value()); src != "" {
		f.name.Placeholder = config.NameFromSource(src)
	} else {
		f.name.Placeholder = "the last part of the stream URL"
	}
}

func (f *form) setFocus(i int) {
	f.focus = (i + numFields) % numFields
	f.video.Blur()
	f.name.Blur()
	f.bitrate.Blur()
	switch f.focus {
	case fieldVideo:
		f.video.Focus()
	case fieldName:
		f.name.Focus()
	case fieldBitrate:
		f.bitrate.Focus()
	}
}

// camera is the camera the form describes (not yet validated).
func (f *form) camera() config.Camera {
	cam := f.base
	cam.Source = config.ExpandHome(strings.TrimSpace(f.video.Value()))
	cam.Name = strings.TrimSpace(f.name.Value())
	if cam.Name == "" && cam.Source != "" {
		cam.Name = config.NameFromSource(cam.Source)
	}
	cam.Loop, cam.Audio, cam.Codec = f.loop, f.audio, f.codec
	cam.Bitrate = strings.TrimSpace(f.bitrate.Value())
	return cam
}

func (m *Model) formKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.form
	switch msg.String() {
	case "esc":
		m.mode, m.form = modeList, nil
		return m, nil
	case "tab", "down":
		f.setFocus(f.focus + 1)
		return m, nil
	case "shift+tab", "up":
		f.setFocus(f.focus - 1)
		return m, nil
	case "ctrl+o":
		return m, m.openPicker()
	case "ctrl+s":
		return m, m.submit()
	case "enter":
		if f.focus == fieldVideo {
			// A typed path to a file moves on; anything else opens the browser.
			if fi, err := os.Stat(config.ExpandHome(strings.TrimSpace(f.video.Value()))); err == nil && fi.Mode().IsRegular() {
				f.setFocus(fieldName)
				return m, nil
			}
			return m, m.openPicker()
		}
		return m, m.submit()
	}

	k := msg.String()
	switch f.focus {
	case fieldLoop:
		if k == " " || k == "left" || k == "right" {
			f.loop = !f.loop
		}
	case fieldAudio:
		if k == " " || k == "left" || k == "right" {
			f.audio = !f.audio
		}
	case fieldCodec:
		i := slices.Index(codecs, f.codec)
		switch k {
		case " ", "right":
			f.codec = codecs[(i+1)%len(codecs)]
		case "left":
			f.codec = codecs[(i+len(codecs)-1)%len(codecs)]
		}
	case fieldVideo:
		f.video, _ = f.video.Update(msg)
		f.nameFollowsVideo()
	case fieldName:
		f.name, _ = f.name.Update(msg)
	case fieldBitrate:
		f.bitrate, _ = f.bitrate.Update(msg)
	}
	f.err = ""
	return m, nil
}

// submit checks the form and, when it is fine, adds or updates the camera.
func (m *Model) submit() tea.Cmd {
	f := m.form
	cam := f.camera()
	if cam.Source == "" {
		f.err = "choose a video file first (enter on the Video line opens the file browser)"
		f.setFocus(fieldVideo)
		return nil
	}
	if err := config.ValidateCamera(&cam); err != nil {
		f.err = strings.TrimPrefix(err.Error(), fmt.Sprintf("camera %q: ", cam.Name))
		return nil
	}
	for _, it := range m.items {
		if it != f.editing && it.cam.Name == cam.Name {
			f.err = fmt.Sprintf("another camera is already called %q; every camera needs its own name", cam.Name)
			f.setFocus(fieldName)
			return nil
		}
	}
	return m.applyForm(cam)
}

func (m *Model) formView() string {
	f := m.form
	var b strings.Builder
	title := "Add a camera"
	if f.editing != nil {
		title = "Edit " + f.editing.cam.Name
	}
	b.WriteString(bold.Render(title) + "\n\n")

	check := func(on bool) string {
		if on {
			return "[x]"
		}
		return "[ ]"
	}
	name := f.camera().Name
	if name == "" {
		name = "…"
	}
	rows := []struct {
		field int
		label string
		value string
		hint  string
	}{
		{fieldVideo, "Video", f.video.View(), "enter: browse"},
		{fieldName, "Name", f.name.View(), "empty = from file name"},
		{-1, "URL", green.Render(m.url(name)), ""},
		{fieldLoop, "Loop", check(f.loop) + " start over when the video ends", ""},
		{fieldAudio, "Audio", check(f.audio) + " send the audio track", ""},
		{fieldCodec, "Codec", "‹ " + string(f.codec) + " ›  " + dim.Render(codecHelp[f.codec]), ""},
		{fieldBitrate, "Bitrate", f.bitrate.View(), "e.g. 2M, re-encodes"},
	}
	for _, r := range rows {
		label := fmt.Sprintf("  %-*s", labelW-2, r.label)
		if r.field == f.focus {
			label = cyan.Render(fmt.Sprintf("▸ %-*s", labelW-2, r.label))
		} else if r.field == -1 {
			label = dim.Render(label)
		}
		line := label + r.value
		if r.hint != "" && r.field == f.focus {
			line += "  " + dim.Render(r.hint)
		}
		b.WriteString(line + "\n")
	}

	b.WriteString("\n")
	if f.err != "" {
		b.WriteString(red.Render("✗ "+f.err) + "\n\n")
	}
	b.WriteString(dim.Render("tab/↑↓ move · space toggle · ←→ change · enter save · ctrl+o browse · esc cancel"))
	return b.String()
}

// ---- file browser ----

func (m *Model) openPicker() tea.Cmd {
	dir := m.pickDir
	if v := config.ExpandHome(strings.TrimSpace(m.form.video.Value())); v != "" {
		if fi, err := os.Stat(v); err == nil {
			if fi.IsDir() {
				dir = v
			} else {
				dir = filepath.Dir(v)
			}
		}
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}

	fp := filepicker.New()
	fp.CurrentDirectory = dir
	fp.AllowedTypes = videoExts
	fp.ShowPermissions = false
	fp.AutoHeight = false
	// esc leaves the browser; these keys go up a folder.
	fp.KeyMap.Back = key.NewBinding(key.WithKeys("h", "backspace", "left"))
	fp.SetHeight(m.pickerHeight())
	m.picker = fp
	m.pickNote = ""
	m.mode = modePicker
	return fp.Init()
}

func (m *Model) pickerHeight() int {
	// header, blank, title, blank, note, help
	return max(m.height-7, 3)
}

func (m *Model) pickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k := msg.String(); k == "esc" || k == "q" {
		m.mode = modeForm
		return m, nil
	}
	var cmd tea.Cmd
	m.picker, cmd = m.picker.Update(msg)
	if ok, path := m.picker.DidSelectFile(msg); ok {
		f := m.form
		f.video.SetValue(path)
		f.video.CursorEnd()
		f.nameFollowsVideo()
		f.setFocus(fieldName)
		f.err = ""
		m.pickDir = filepath.Dir(path)
		m.mode = modeForm
		return m, nil
	}
	if ok, path := m.picker.DidSelectDisabledFile(msg); ok {
		m.pickNote = filepath.Base(path) + " doesn't look like a video file"
	} else {
		m.pickNote = ""
	}
	return m, cmd
}

func (m *Model) pickerView() string {
	var b strings.Builder
	b.WriteString(bold.Render("Choose a video") + "  " + dim.Render(m.picker.CurrentDirectory) + "\n\n")
	b.WriteString(strings.TrimRight(m.picker.View(), "\n") + "\n")
	b.WriteString(yellow.Render(m.pickNote) + "\n")
	b.WriteString(dim.Render("↑↓ move · enter open folder / choose · ← or backspace: up a folder · esc back"))
	return b.String()
}
