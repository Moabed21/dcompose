package views

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"dcompose/internal/docker"
	"dcompose/internal/tui/styles"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	maxLogLines    = 10000
	initialTail    = 500
	sidebarWidth   = 28
	logBatchWindow = 40 * time.Millisecond
)

// logSource is something we can stream logs from.
type logSource struct {
	key     string // "p:<project>" or "c:<id>"
	label   string
	project string // set for whole-project sources
	ctrID   string // set for container sources
	state   string
}

type logEntry struct {
	ts     time.Time
	text   string
	stderr bool
	marker bool // dcompose-generated status line
}

// LogBatchMsg carries a batch of streamed log lines.
type LogBatchMsg struct {
	Gen   int
	Lines []docker.LogLine
	Done  bool
	Err   error
}

// LogsModel is the live log viewer.
type LogsModel struct {
	client   *docker.Client
	projects []docker.ComposeProject

	sources []logSource
	list    listState

	// Active stream
	active    logSource
	stream    *docker.LogStream
	gen       int
	streaming bool
	ended     bool
	endErr    error
	lastTS    time.Time

	entries  []logEntry
	viewport viewport.Model
	follow   bool
	wrap     bool
	showTS   bool
	filter   filterBox
	visible  bool // tab is active

	width  int
	height int
}

// NewLogsModel creates a new logs view.
func NewLogsModel(client *docker.Client) LogsModel {
	vp := viewport.New(80, 20)
	vp.KeyMap = viewport.KeyMap{
		PageDown:     key.NewBinding(key.WithKeys("pgdown", " ")),
		PageUp:       key.NewBinding(key.WithKeys("pgup")),
		HalfPageUp:   key.NewBinding(key.WithKeys("ctrl+u")),
		HalfPageDown: key.NewBinding(key.WithKeys("ctrl+d")),
		Up:           key.NewBinding(key.WithKeys("up", "k")),
		Down:         key.NewBinding(key.WithKeys("down", "j")),
		Left:         key.NewBinding(key.WithDisabled()),
		Right:        key.NewBinding(key.WithDisabled()),
	}
	vp.MouseWheelDelta = 3
	return LogsModel{
		client:   client,
		viewport: vp,
		follow:   true,
		wrap:     true,
		showTS:   true,
		filter:   newFilterBox(),
	}
}

// Capturing reports whether the view is consuming all key input.
func (m LogsModel) Capturing() bool { return m.filter.active }

// SetSources rebuilds the source list from projects and containers. It may
// return a command to reconnect a stream that ended because its container
// stopped and has now come back.
func (m *LogsModel) SetSources(projects []docker.ComposeProject, containers []docker.ContainerInfo) tea.Cmd {
	m.projects = projects
	var srcs []logSource
	projRunning := map[string]bool{}
	for _, c := range containers {
		if c.Project != "" && c.State == "running" {
			projRunning[c.Project] = true
		}
	}
	for _, p := range projects {
		if p.IsDown() {
			continue
		}
		st := "exited"
		if projRunning[p.Name] {
			st = "running"
		}
		srcs = append(srcs, logSource{key: "p:" + p.Name, label: "⬡ " + p.Name, project: p.Name, state: st})
	}
	for _, c := range containers {
		srcs = append(srcs, logSource{key: "c:" + c.Name, label: c.Name, ctrID: c.ID, state: c.State})
	}
	m.sources = srcs
	m.list.sync(m.sourceKeys())

	// Keep the active source's state fresh and auto-reconnect when it restarts.
	for _, s := range srcs {
		if s.key == m.active.key {
			m.active.state = s.state
			m.active.ctrID = s.ctrID // a recreated container keeps its name but gets a new ID
			if m.visible && m.ended && m.endErr == nil && s.state == "running" {
				m.addMarker("— reconnected —")
				return m.startStream(true)
			}
		}
	}
	return nil
}

func (m LogsModel) sourceKeys() []string {
	k := make([]string, len(m.sources))
	for i, s := range m.sources {
		k[i] = s.key
	}
	return k
}

// SetSize sets the available render size.
func (m *LogsModel) SetSize(w, h int) {
	m.width, m.height = w, h
	m.layoutViewport()
	m.render()
}

func (m LogsModel) showSidebar() bool { return m.width >= 90 }

func (m *LogsModel) layoutViewport() {
	w := m.width - 4 // border + side margins
	if m.showSidebar() {
		w -= sidebarWidth + 1
	}
	h := m.height - 4 // title, help, border
	if w < 10 {
		w = 10
	}
	if h < 3 {
		h = 3
	}
	m.viewport.Width = w
	m.viewport.Height = h
}

// Activate is called when the logs tab becomes visible.
func (m *LogsModel) Activate() tea.Cmd {
	m.visible = true
	if m.active.key == "" {
		if m.list.cursor < len(m.sources) {
			m.active = m.sources[m.list.cursor]
		} else {
			return nil
		}
	}
	m.entries = nil
	m.lastTS = time.Time{}
	return m.startStream(false)
}

// Deactivate stops streaming when the tab is hidden.
func (m *LogsModel) Deactivate() {
	m.visible = false
	m.StopLogStream()
}

// SelectContainer focuses a container (by ID or name) and starts streaming it.
func (m *LogsModel) SelectContainer(idOrName string) tea.Cmd {
	for i, s := range m.sources {
		if s.ctrID != "" && (s.ctrID == idOrName || strings.HasPrefix(s.ctrID, idOrName) || s.label == idOrName) {
			return m.selectIndex(i)
		}
	}
	return nil
}

// SelectProject focuses aggregated logs for a compose project.
func (m *LogsModel) SelectProject(name string) tea.Cmd {
	for i, s := range m.sources {
		if s.project == name {
			return m.selectIndex(i)
		}
	}
	// Project has no containers; still allow (compose will show nothing).
	m.active = logSource{key: "p:" + name, label: "⬡ " + name, project: name}
	m.visible = true
	m.entries = nil
	return m.startStream(false)
}

func (m *LogsModel) selectIndex(i int) tea.Cmd {
	m.list.cursor = i
	m.list.selKey = m.sources[i].key
	m.active = m.sources[i]
	m.visible = true
	m.entries = nil
	m.lastTS = time.Time{}
	return m.startStream(false)
}

// StopLogStream cancels the active log stream.
func (m *LogsModel) StopLogStream() {
	if m.stream != nil {
		m.stream.Cancel()
		m.stream = nil
	}
	m.streaming = false
	m.gen++ // invalidate in-flight batches
}

func (m *LogsModel) startStream(resume bool) tea.Cmd {
	m.StopLogStream()
	m.ended = false
	m.endErr = nil
	m.follow = true

	src := m.active
	var s *docker.LogStream
	switch {
	case src.ctrID != "":
		tail, since := initialTail, ""
		if resume && !m.lastTS.IsZero() {
			tail = -1 // everything since the last line we saw
			since = m.lastTS.Add(time.Nanosecond).Format(time.RFC3339Nano)
		}
		s = m.client.FollowContainerLogs(src.ctrID, tail, since)
	case src.project != "":
		p := docker.ComposeProject{Name: src.project}
		for _, pp := range m.projects {
			if pp.Name == src.project {
				p = pp
			}
		}
		tail := initialTail
		if resume {
			tail = 0
		}
		var err error
		s, err = docker.FollowComposeLogs(p, tail)
		if err != nil {
			m.ended, m.endErr = true, err
			m.addMarker("error: " + err.Error())
			return nil
		}
	default:
		return nil
	}
	m.stream = s
	m.streaming = true
	m.render()
	return waitLogs(s, m.gen)
}

func waitLogs(s *docker.LogStream, gen int) tea.Cmd {
	return func() tea.Msg {
		l, ok := <-s.Lines
		if !ok {
			return LogBatchMsg{Gen: gen, Done: true, Err: s.Err()}
		}
		batch := []docker.LogLine{l}
		deadline := time.NewTimer(logBatchWindow)
		defer deadline.Stop()
		for len(batch) < 5000 {
			select {
			case l, ok := <-s.Lines:
				if !ok {
					return LogBatchMsg{Gen: gen, Lines: batch, Done: true, Err: s.Err()}
				}
				batch = append(batch, l)
			case <-deadline.C:
				return LogBatchMsg{Gen: gen, Lines: batch}
			}
		}
		return LogBatchMsg{Gen: gen, Lines: batch}
	}
}

var ctrlChars = regexp.MustCompile(`[\x00-\x08\x0b-\x1f\x7f]`)

func sanitize(s string) string {
	s = ansi.Strip(s)
	s = strings.ReplaceAll(s, "\t", "    ")
	return ctrlChars.ReplaceAllString(s, "")
}

func (m *LogsModel) addMarker(text string) {
	m.entries = append(m.entries, logEntry{ts: time.Now(), text: text, marker: true})
	m.render()
}

func (m *LogsModel) appendLines(lines []docker.LogLine) {
	isCtr := m.active.ctrID != ""
	for _, l := range lines {
		e := logEntry{text: l.Text, stderr: l.Stderr}
		if isCtr {
			// Docker prefixes each line with an RFC3339Nano timestamp.
			if i := strings.IndexByte(l.Text, ' '); i > 0 {
				if t, err := time.Parse(time.RFC3339Nano, l.Text[:i]); err == nil {
					e.ts = t
					e.text = l.Text[i+1:]
					if t.After(m.lastTS) {
						m.lastTS = t
					}
				}
			} else if t, err := time.Parse(time.RFC3339Nano, l.Text); err == nil {
				e.ts, e.text = t, ""
			}
		}
		e.text = sanitize(e.text)
		m.entries = append(m.entries, e)
	}
	if over := len(m.entries) - maxLogLines; over > 0 {
		m.entries = append([]logEntry(nil), m.entries[over:]...)
	}
}

// render rebuilds the viewport content.
func (m *LogsModel) render() {
	var b strings.Builder
	stderrMark := lipgloss.NewStyle().Foreground(styles.ColorRed).Render("▌")
	tsStyle := styles.DimStyle
	markStyle := lipgloss.NewStyle().Foreground(styles.ColorYellow).Italic(true)
	q := strings.ToLower(m.filter.Value())
	first := true
	for _, e := range m.entries {
		if q != "" && !e.marker && !strings.Contains(strings.ToLower(e.text), q) {
			continue
		}
		prefix := ""
		if m.showTS && !e.ts.IsZero() {
			prefix = tsStyle.Render(e.ts.Local().Format("15:04:05.000")) + " "
		}
		if e.stderr {
			prefix += stderrMark
		}
		text := e.text
		if e.marker {
			text = markStyle.Render(text)
		}
		line := prefix + text
		if m.wrap && m.viewport.Width > 0 && ansi.StringWidth(line) > m.viewport.Width {
			line = ansi.Wrap(line, m.viewport.Width, " ")
		}
		if !first {
			b.WriteByte('\n')
		}
		first = false
		b.WriteString(line)
	}
	m.viewport.SetContent(b.String())
	if m.follow {
		m.viewport.GotoBottom()
	}
}

func (m LogsModel) Update(msg tea.Msg) (LogsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case LogBatchMsg:
		if msg.Gen != m.gen {
			return m, nil // stale stream
		}
		m.appendLines(msg.Lines)
		if msg.Done {
			m.streaming = false
			m.ended = true
			m.endErr = msg.Err
			m.stream = nil
			if msg.Err != nil {
				m.entries = append(m.entries, logEntry{ts: time.Now(), text: "stream error: " + msg.Err.Error(), marker: true})
			} else if m.active.ctrID != "" {
				m.entries = append(m.entries, logEntry{ts: time.Now(), text: "— container stopped; will reconnect when it runs again (r to retry now) —", marker: true})
			} else {
				m.entries = append(m.entries, logEntry{ts: time.Now(), text: "— log stream ended (r to reconnect) —", marker: true})
			}
			m.render()
			return m, nil
		}
		m.render()
		return m, waitLogs(m.stream, m.gen)

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		if !m.viewport.AtBottom() {
			m.follow = false
		}
		return m, cmd

	case tea.KeyMsg:
		if m.filter.update(msg) {
			m.render()
			return m, nil
		}
		switch msg.String() {
		case "[", "left", "h":
			if m.list.cursor > 0 {
				return m, m.selectIndex(m.list.cursor - 1)
			}
			return m, nil
		case "]", "right", "l":
			if m.list.cursor < len(m.sources)-1 {
				return m, m.selectIndex(m.list.cursor + 1)
			}
			return m, nil
		case "f":
			m.follow = !m.follow
			if m.follow {
				m.viewport.GotoBottom()
			}
			return m, nil
		case "w":
			m.wrap = !m.wrap
			m.render()
			return m, nil
		case "t":
			m.showTS = !m.showTS
			m.render()
			return m, nil
		case "c":
			m.entries = nil
			m.render()
			return m, nil
		case "r":
			m.entries = nil
			m.lastTS = time.Time{}
			return m, m.startStream(false)
		case "g", "home":
			m.follow = false
			m.viewport.GotoTop()
			return m, nil
		case "G", "end":
			m.follow = true
			m.viewport.GotoBottom()
			return m, nil
		case "E":
			return m, m.export()
		}
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		if !m.viewport.AtBottom() {
			m.follow = false
		} else if msg.String() == "down" || msg.String() == "j" || msg.String() == "pgdown" {
			m.follow = true
		}
		return m, cmd
	}
	return m, nil
}

func (m LogsModel) export() tea.Cmd {
	entries := append([]logEntry(nil), m.entries...)
	label := m.active.label
	return func() tea.Msg {
		safe := safeFileName(strings.TrimSpace(strings.TrimPrefix(label, "⬡ ")))
		name := fmt.Sprintf("dcompose-%s-%s.log", safe, time.Now().Format("20060102-150405"))
		path, _ := filepath.Abs(name)
		var b strings.Builder
		for _, e := range entries {
			if !e.ts.IsZero() {
				b.WriteString(e.ts.Format(time.RFC3339Nano) + " ")
			}
			b.WriteString(e.text + "\n")
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			return FlashMsg{Text: "export logs", Err: err}
		}
		return FlashMsg{Text: fmt.Sprintf("exported %d lines to %s", len(entries), path)}
	}
}

// safeFileName keeps only characters that are valid in file names on every OS.
func safeFileName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, s)
	if s = strings.Trim(s, "."); s == "" {
		s = "logs"
	}
	return s
}

func (m LogsModel) View() string {
	if len(m.sources) == 0 && m.active.key == "" {
		return titleLine("Logs", "", m.width) + "\n" + styles.SubtitleStyle.Render("\n  No containers available.")
	}

	state := ""
	switch {
	case m.streaming:
		state = styles.SuccessStyle.Render("● live")
	case m.ended && m.endErr != nil:
		state = styles.ErrorStyle.Render("✗ error")
	case m.ended:
		state = styles.WarnStyle.Render("■ ended")
	}
	onOff := func(name string, v bool) string {
		if v {
			return styles.KeyStyle.Render(name)
		}
		return styles.HelpStyle.Render(name)
	}
	flags := strings.Join([]string{state, onOff("follow", m.follow), onOff("wrap", m.wrap), onOff("time", m.showTS),
		styles.DimStyle.Render(fmt.Sprintf("%d lines", len(m.entries)))}, styles.DimStyle.Render(" · "))
	if fv := m.filter.view(); fv != "" {
		flags = fv + "   " + flags
	}
	title := titleLine("Logs · "+truncate(m.active.label, 40), flags, m.width)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.ColorBorder).
		Render(lipgloss.NewStyle().Width(m.viewport.Width).Height(m.viewport.Height).Render(m.viewport.View()))

	body := box
	if m.showSidebar() {
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.renderSidebar(), " ", box)
	}
	help := helpLine(m.width, "[ ] / ←→ source · ↑↓ pgup/pgdn scroll · f follow · w wrap · t time · / filter · c clear · r reconnect · E export")
	return title + "\n" + lipgloss.NewStyle().MarginLeft(1).Render(body) + "\n" + help
}

func (m LogsModel) renderSidebar() string {
	h := m.viewport.Height
	inner := sidebarWidth - 2
	l := m.list
	start, end := l.window(len(m.sources), h)
	var rows []string
	for i := start; i < end; i++ {
		s := m.sources[i]
		dot := lipgloss.NewStyle().Foreground(styles.ColorTextMute).Render("○")
		switch s.state {
		case "running":
			dot = lipgloss.NewStyle().Foreground(styles.ColorGreen).Render("●")
		case "paused", "restarting":
			dot = lipgloss.NewStyle().Foreground(styles.ColorYellow).Render("●")
		case "exited", "dead":
			dot = lipgloss.NewStyle().Foreground(styles.ColorRed).Render("■")
		}
		label := truncate(s.label, inner-3)
		if s.key == m.active.key {
			rows = append(rows, dot+" "+styles.RowSelectedStyle.Render(fit(label, inner-2)))
		} else {
			rows = append(rows, dot+" "+fit(label, inner-2))
		}
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.ColorBorder).
		Width(inner).
		Height(h).
		Render(strings.Join(rows, "\n"))
}
