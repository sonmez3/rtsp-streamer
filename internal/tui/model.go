package tui

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/filepicker"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/sonmez3/rtsp-streamer/internal/app"
	"github.com/sonmez3/rtsp-streamer/internal/camera"
	"github.com/sonmez3/rtsp-streamer/internal/config"
)

type mode int

const (
	modeList mode = iota
	modeForm
	modePicker
	modeDelete // asking "delete this camera?"
)

type state int

const (
	stateStopped  state = iota // not streaming
	stateStarting              // reading the video and starting ffmpeg
	stateRunning               // started; the engine's status says whether it's live
	stateStopping
	stateFailed // could not start; item.err says why
)

type item struct {
	id       int
	cam      config.Camera
	enabled  bool // the user wants it streaming; this is what gets saved
	state    state
	plan     camera.Plan
	err      string
	status   app.CameraStatus
	deleting bool // remove it from the list once it has stopped
}

type Model struct {
	o      Options
	items  []*item
	nextID int
	cursor int
	mode   mode

	form     *form
	picker   filepicker.Model
	pickDir  string // folder the file browser opens in next time
	pickNote string // message under the file browser

	note   string // message above the help line, e.g. a save error
	logs   []string
	width  int
	height int
}

const maxLogs = 500

type (
	tickMsg    time.Time
	logMsg     string
	startedMsg struct {
		id   int
		plan camera.Plan
		err  error
	}
	stoppedMsg struct{ id int }
)

// New builds the UI from the saved camera list in o.File.
func New(o Options) *Model {
	m := &Model{o: o, pickDir: o.Dir, width: 100, height: 30}
	if o.File != nil {
		for i, fc := range o.File.Cameras {
			cam, err := config.CameraFromFile(fc, o.Defaults, i)
			if err != nil {
				m.note = "skipped a saved camera: " + err.Error()
				continue
			}
			m.addItem(cam, fc.Enabled == nil || *fc.Enabled)
		}
	}
	return m
}

func (m *Model) addItem(cam config.Camera, enabled bool) *item {
	m.nextID++
	it := &item{id: m.nextID, cam: cam, enabled: enabled}
	m.items = append(m.items, it)
	return it
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tick()}
	for _, it := range m.items {
		if it.enabled {
			cmds = append(cmds, m.start(it))
		}
	}
	return tea.Batch(cmds...)
}

func tick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// start, restart and stop run the slow engine calls (reading the video,
// waiting for ffmpeg to exit) in the background and report back with a message.

func (m *Model) start(it *item) tea.Cmd { return m.restart(it, "") }

// restart stops the camera running as oldName (if any) and starts it.
func (m *Model) restart(it *item, oldName string) tea.Cmd {
	it.state, it.err = stateStarting, ""
	ctl, cam, id := m.o.Ctl, it.cam, it.id
	return func() tea.Msg {
		if oldName != "" {
			ctl.StopCamera(oldName)
		}
		if err := config.ValidateCamera(&cam); err != nil {
			return startedMsg{id: id, err: err}
		}
		plan, err := ctl.StartCamera(context.Background(), cam, nil)
		return startedMsg{id: id, plan: plan, err: err}
	}
}

func (m *Model) stop(it *item) tea.Cmd {
	it.state = stateStopping
	ctl, name, id := m.o.Ctl, it.cam.Name, it.id
	return func() tea.Msg {
		ctl.StopCamera(name)
		return stoppedMsg{id: id}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.picker.SetHeight(m.pickerHeight())
		if m.form != nil {
			m.form.setWidth(m.width)
		}
		return m, nil
	case tickMsg:
		for _, it := range m.items {
			if it.state == stateRunning {
				it.status = m.o.Ctl.Status(it.cam.Name)
			}
		}
		return m, tick()
	case logMsg:
		m.logs = append(m.logs, string(msg))
		if len(m.logs) > maxLogs {
			m.logs = m.logs[len(m.logs)-maxLogs:]
		}
		return m, nil
	case startedMsg:
		if it := m.find(msg.id); it != nil {
			if msg.err != nil {
				it.state, it.err = stateFailed, msg.err.Error()
				m.o.Log.Errorf(it.cam.Name, "%v", msg.err)
			} else {
				it.state, it.plan = stateRunning, msg.plan
				it.status = m.o.Ctl.Status(it.cam.Name)
			}
		}
		return m, nil
	case stoppedMsg:
		if it := m.find(msg.id); it != nil {
			it.state, it.status = stateStopped, app.CameraStatus{}
			if it.deleting {
				m.remove(it)
			}
		}
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		switch m.mode {
		case modeList:
			return m.listKey(msg)
		case modeForm:
			return m.formKey(msg)
		case modePicker:
			return m.pickerKey(msg)
		case modeDelete:
			return m.deleteKey(msg)
		}
	}
	// Anything else (the file browser reading a folder) goes to the browser.
	if m.mode == modePicker {
		var cmd tea.Cmd
		m.picker, cmd = m.picker.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) listKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.note = ""
	it := m.current()
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j":
		m.cursor = min(m.cursor+1, max(len(m.items)-1, 0))
	case "a", "+":
		m.form = newForm(nil, m.o.Defaults.NewCamera(), m.width)
		m.mode = modeForm
		return m, m.openPicker()
	case "e", "enter":
		if it == nil {
			return m, nil
		}
		if busy(it) {
			m.note = "wait until " + it.cam.Name + " has started or stopped"
			return m, nil
		}
		m.form = newForm(it, it.cam, m.width)
		m.mode = modeForm
	case " ", "s":
		if it == nil {
			return m, nil
		}
		switch it.state {
		case stateRunning:
			it.enabled = false
			m.save()
			return m, m.stop(it)
		case stateStopped, stateFailed:
			it.enabled = true
			m.save()
			return m, m.start(it)
		default:
			m.note = "wait until " + it.cam.Name + " has started or stopped"
		}
	case "d", "delete", "x":
		if it == nil {
			return m, nil
		}
		if busy(it) {
			m.note = "wait until " + it.cam.Name + " has started or stopped"
			return m, nil
		}
		m.mode = modeDelete
	}
	return m, nil
}

func (m *Model) deleteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		m.mode = modeList
		it := m.current()
		if it == nil {
			return m, nil
		}
		if it.state == stateRunning {
			it.deleting = true
			m.save()
			return m, m.stop(it)
		}
		m.remove(it)
		m.save()
	case "n", "N", "esc", "q":
		m.mode = modeList
	}
	return m, nil
}

func busy(it *item) bool { return it.state == stateStarting || it.state == stateStopping }

func (m *Model) current() *item {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return nil
	}
	return m.items[m.cursor]
}

func (m *Model) find(id int) *item {
	for _, it := range m.items {
		if it.id == id {
			return it
		}
	}
	return nil
}

func (m *Model) remove(it *item) {
	for i, x := range m.items {
		if x == it {
			m.items = append(m.items[:i], m.items[i+1:]...)
			break
		}
	}
	m.cursor = min(m.cursor, max(len(m.items)-1, 0))
}

// save writes the camera list, so the next start brings the same cameras back.
func (m *Model) save() {
	if m.o.SavePath == "" {
		return
	}
	f := config.File{Cameras: []config.FileCamera{}}
	if m.o.File != nil {
		f.Port, f.Bind, f.UDP, f.UDPPort = m.o.File.Port, m.o.File.Bind, m.o.File.UDP, m.o.File.UDPPort
	}
	for _, it := range m.items {
		if !it.deleting {
			f.Cameras = append(f.Cameras, it.cam.ToFile(it.enabled))
		}
	}
	if err := config.WriteFile(m.o.SavePath, &f); err != nil {
		m.note = "can't save the camera list: " + err.Error()
	}
}

// applyForm adds the camera from the form, or updates the one being edited.
func (m *Model) applyForm(cam config.Camera) tea.Cmd {
	editing := m.form.editing
	m.mode, m.form = modeList, nil
	if editing == nil {
		it := m.addItem(cam, true)
		m.cursor = len(m.items) - 1
		m.save()
		return m.start(it)
	}

	old := editing.cam
	editing.cam = cam
	m.save()
	switch {
	case editing.state == stateRunning && !reflect.DeepEqual(old, cam):
		return m.restart(editing, old.Name)
	case editing.state == stateFailed && editing.enabled:
		return m.start(editing)
	}
	return nil
}

func (m *Model) url(name string) string {
	return fmt.Sprintf("rtsp://%s/%s", net.JoinHostPort(m.o.Host, strconv.Itoa(m.o.Port)), name)
}

// ---- drawing ----

var (
	bold   = lipgloss.NewStyle().Bold(true)
	dim    = lipgloss.NewStyle().Faint(true)
	green  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	yellow = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	red    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	cyan   = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
)

func (m *Model) View() string {
	var body string
	switch m.mode {
	case modeForm:
		body = m.formView()
	case modePicker:
		body = m.pickerView()
	default:
		body = m.listView()
	}
	lines := strings.Split(m.header()+"\n\n"+body, "\n")
	// Never draw more lines than fit, or the terminal scrolls the top away.
	if m.height > 0 && len(lines) > m.height {
		lines = lines[:m.height]
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, m.width, "…")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) header() string {
	transports := "TCP"
	if m.o.UDP {
		transports = "TCP + UDP"
	}
	live := 0
	for _, it := range m.items {
		if it.state == stateRunning && it.status.Live {
			live++
		}
	}
	return bold.Render("rtsp-streamer") + " " + m.o.Version +
		dim.Render(fmt.Sprintf("  ·  %s  ·  port %d (%s)  ·  %d of %d live",
			m.o.Host, m.o.Port, transports, live, len(m.items)))
}

func (m *Model) listView() string {
	var b strings.Builder

	// Lines around the camera rows: table header, blank, log title, help
	// line, and the note line when there is one.
	fixed := 2 + 4
	if m.note != "" || m.mode == modeDelete {
		fixed++
	}
	const minLog = 3
	rows := len(m.items)
	room := max(m.height-fixed-minLog, 1)
	first := 0
	if rows > room {
		// Scroll so the selected camera stays in view.
		first = min(max(m.cursor-room/2, 0), rows-room)
		rows = room
	}

	if len(m.items) == 0 {
		b.WriteString(dim.Render("  No cameras yet. Press a to add a video.") + "\n")
		rows = 1
	} else {
		b.WriteString(m.table(first, rows))
	}

	b.WriteString("\n" + bold.Render("Log") + "\n")
	logRows := max(m.height-fixed-rows, 0)
	start := max(len(m.logs)-logRows, 0)
	for _, l := range m.logs[start:] {
		b.WriteString(styleLog(l) + "\n")
	}
	for i := len(m.logs) - start; i < logRows; i++ {
		b.WriteString("\n")
	}

	switch {
	case m.mode == modeDelete:
		b.WriteString(yellow.Render(fmt.Sprintf("Delete %s? y = yes, n = no", m.current().cam.Name)) + "\n")
	case m.note != "":
		b.WriteString(yellow.Render(m.note) + "\n")
	}
	b.WriteString(dim.Render("a add · e edit · space start/stop · d delete · ↑↓ select · q quit"))
	return b.String()
}

func (m *Model) table(first, rows int) string {
	nameW, urlW := len("NAME"), len("URL")
	for _, it := range m.items {
		nameW = max(nameW, len(it.cam.Name))
		urlW = max(urlW, len(m.url(it.cam.Name)))
	}

	var b strings.Builder
	b.WriteString(dim.Render(fmt.Sprintf("  %-11s %-*s  %-*s  %7s  %s", "STATUS", nameW, "NAME", urlW, "URL", "VIEWERS", "VIDEO")) + "\n")
	for i := first; i < first+rows; i++ {
		it := m.items[i]
		marker := "  "
		name := fmt.Sprintf("%-*s", nameW, it.cam.Name)
		if i == m.cursor {
			marker = cyan.Render("▸ ")
			name = bold.Render(name)
		}
		label, style := statusLabel(it)
		viewers := ""
		if it.state == stateRunning && it.status.Live {
			viewers = strconv.Itoa(it.status.Viewers)
		}
		detail := dim.Render(m.detail(it))
		if it.state == stateFailed {
			detail = red.Render(it.err)
		}
		fmt.Fprintf(&b, "%s%s %s  %s  %7s  %s\n", marker,
			style.Render(fmt.Sprintf("%-11s", label)), name,
			fmt.Sprintf("%-*s", urlW, m.url(it.cam.Name)), viewers, detail)
	}
	return b.String()
}

func statusLabel(it *item) (string, lipgloss.Style) {
	switch it.state {
	case stateStarting:
		return "◌ starting", yellow
	case stateStopping:
		return "◌ stopping", yellow
	case stateFailed:
		return "✗ failed", red
	case stateRunning:
		switch {
		case it.status.Finished:
			return "✓ finished", dim
		case it.status.Live:
			return "● live", green
		default:
			return "○ waiting", yellow
		}
	}
	return "■ stopped", dim
}

// detail says what is sent: "a.mp4 · video h264 copy · audio aac copy".
func (m *Model) detail(it *item) string {
	parts := []string{filepath.Base(it.cam.Source)}
	if it.state == stateRunning {
		parts = append(parts, "video "+it.plan.Video, "audio "+it.plan.Audio)
	}
	if !it.cam.Loop {
		parts = append(parts, "once")
	}
	return strings.Join(parts, " · ")
}

// styleLog colors the level of a log line: "15:04:05 WRN [cam] ...".
func styleLog(line string) string {
	if len(line) < 13 || line[8] != ' ' || line[12] != ' ' {
		return line
	}
	level := line[9:12]
	var style lipgloss.Style
	switch level {
	case "INF":
		style = green
	case "WRN":
		style = yellow
	case "ERR":
		style = red
	default:
		return line
	}
	return dim.Render(line[:8]) + " " + style.Render(level) + line[12:]
}
