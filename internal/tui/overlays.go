package tui

import (
	"fmt"
	"strings"
	"time"

	"dcompose/internal/docker"
	"dcompose/internal/tui/styles"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type overlayKind int

const (
	overlayNone overlayKind = iota
	overlayConfirm
	overlayPrompt
	overlayViewer
	overlayTask
)

// ---------------------------------------------------------------- confirm

type confirmDialog struct {
	title     string
	body      string
	danger    bool
	yes       bool
	onConfirm tea.Cmd
}

func (d confirmDialog) view(width int) string {
	titleStyle := styles.WarnStyle
	border := styles.ColorYellow
	if d.danger {
		titleStyle = styles.ErrorStyle
		border = styles.ColorRed
	}
	yes := styles.DialogButtonInactive.Render("Yes")
	no := styles.DialogButtonInactive.Render("No")
	if d.yes {
		yes = styles.DialogButtonActive.Render("Yes")
	} else {
		no = styles.DialogButtonActive.Render("No")
	}
	w := width - 10
	if w > 70 {
		w = 70
	}
	if w < 30 {
		w = 30
	}
	content := titleStyle.Render(d.title) + "\n\n" +
		lipgloss.NewStyle().Width(w).Render(d.body) + "\n\n" +
		yes + "   " + no + "\n\n" +
		styles.HelpStyle.Render("y confirm · n/esc cancel · ←/→ select · enter choose")
	return styles.DialogStyle.BorderForeground(border).Render(content)
}

// ---------------------------------------------------------------- prompt

type promptDialog struct {
	title    string
	input    textinput.Model
	onSubmit func(string) tea.Cmd
}

func newPrompt(title, placeholder, initial string, onSubmit func(string) tea.Cmd, width int) promptDialog {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.SetValue(initial)
	ti.CharLimit = 512
	w := width - 16
	if w > 70 {
		w = 70
	}
	if w < 20 {
		w = 20
	}
	ti.Width = w
	ti.Focus()
	return promptDialog{title: title, input: ti, onSubmit: onSubmit}
}

func (d promptDialog) view() string {
	content := styles.KeyStyle.Render(d.title) + "\n\n" + d.input.View() + "\n\n" +
		styles.HelpStyle.Render("enter submit · esc cancel")
	return styles.DialogStyle.BorderForeground(styles.ColorCyan).Render(content)
}

// ---------------------------------------------------------------- viewer

func newViewport() viewport.Model {
	vp := viewport.New(80, 20)
	vp.MouseWheelDelta = 3
	vp.KeyMap = viewport.KeyMap{
		PageDown:     key.NewBinding(key.WithKeys("pgdown", " ", "f")),
		PageUp:       key.NewBinding(key.WithKeys("pgup", "b")),
		HalfPageUp:   key.NewBinding(key.WithKeys("ctrl+u", "u")),
		HalfPageDown: key.NewBinding(key.WithKeys("ctrl+d", "d")),
		Up:           key.NewBinding(key.WithKeys("up", "k")),
		Down:         key.NewBinding(key.WithKeys("down", "j")),
		Left:         key.NewBinding(key.WithKeys("left", "h")),
		Right:        key.NewBinding(key.WithKeys("right", "l")),
	}
	vp.SetHorizontalStep(8)
	return vp
}

func framedViewer(title, hint string, vp viewport.Model, width int) string {
	head := styles.KeyStyle.Render(truncateStr(title, width-30))
	pct := styles.DimStyle.Render(fmt.Sprintf("%3.0f%%", vp.ScrollPercent()*100))
	gap := width - 4 - lipgloss.Width(head) - lipgloss.Width(pct)
	if gap < 1 {
		gap = 1
	}
	top := head + strings.Repeat(" ", gap) + pct
	return styles.OverlayStyle.Render(top + "\n" + vp.View() + "\n" + styles.HelpStyle.Render(hint))
}

// ---------------------------------------------------------------- tasks

// taskState tracks one background docker CLI command.
type taskState struct {
	id      int
	title   string
	args    []string
	task    *docker.Task
	lines   []string
	done    bool
	err     error
	started time.Time
	ended   time.Time
}

// TaskLinesMsg carries output from a running task.
type TaskLinesMsg struct {
	ID    int
	Lines []string
	Done  bool
	Err   error
}

func waitTask(t *docker.Task, id int) tea.Cmd {
	return func() tea.Msg {
		l, ok := <-t.Lines
		if !ok {
			return TaskLinesMsg{ID: id, Done: true, Err: t.Err()}
		}
		batch := []string{l}
		timer := time.NewTimer(50 * time.Millisecond)
		defer timer.Stop()
		for len(batch) < 2000 {
			select {
			case l, ok := <-t.Lines:
				if !ok {
					return TaskLinesMsg{ID: id, Lines: batch, Done: true, Err: t.Err()}
				}
				batch = append(batch, l)
			case <-timer.C:
				return TaskLinesMsg{ID: id, Lines: batch}
			}
		}
		return TaskLinesMsg{ID: id, Lines: batch}
	}
}

func (t *taskState) elapsed() time.Duration {
	end := t.ended
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(t.started).Round(100 * time.Millisecond)
}

func (t *taskState) content(width int) string {
	wrap := func(s string) string {
		if width > 10 && ansi.StringWidth(s) > width {
			return ansi.Wrap(s, width, " /,")
		}
		return s
	}
	var b strings.Builder
	b.WriteString(styles.DimStyle.Render(wrap("$ docker "+strings.Join(t.args, " "))) + "\n")
	for _, l := range t.lines {
		b.WriteString(wrap(l) + "\n")
	}
	switch {
	case !t.done:
		b.WriteString(styles.WarnStyle.Render(fmt.Sprintf("⟳ running… (%s)", t.elapsed())))
	case t.err != nil:
		b.WriteString(styles.ErrorStyle.Render(fmt.Sprintf("✗ failed after %s: %v", t.elapsed(), t.err)))
	default:
		b.WriteString(styles.SuccessStyle.Render(fmt.Sprintf("✓ completed in %s", t.elapsed())))
	}
	return b.String()
}

func truncateStr(s string, n int) string {
	if n < 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ---------------------------------------------------------------- help

func helpText(tab Tab) string {
	k := styles.KeyStyle.Render
	h := styles.TitleStyle.MarginBottom(0).Render
	row := func(keys, desc string) string {
		pad := 24 - lipgloss.Width(keys)
		if pad < 1 {
			pad = 1
		}
		return "  " + k(keys) + strings.Repeat(" ", pad) + desc
	}

	global := []string{
		h("Global"),
		row("1-7 / tab / shift+tab", "switch tab (or click a tab)"),
		row("R / ctrl+r", "refresh now (auto-refresh every 3s)"),
		row("T", "show output of the current/last background task"),
		row("?", "toggle this help"),
		row("q / ctrl+c", "quit"),
		row("/", "filter the current list (esc clears)"),
		row("↑↓ j/k pgup/pgdn g/G", "navigate · mouse wheel scrolls"),
		"",
		styles.DimStyle.Render("  Tip: hold shift while dragging to select text in most terminals."),
		"",
	}

	ctr := []string{
		row("s", "start (or unpause)"),
		row("S", "stop"),
		row("r", "restart"),
		row("p", "pause / unpause"),
		row("K", "kill (SIGKILL) — confirms"),
		row("d", "force remove — confirms"),
		row("e", "open a shell (bash, falls back to sh)"),
		row("i", "inspect (JSON)"),
		row("l / enter", "follow logs"),
	}

	var tabHelp []string
	switch tab {
	case TabContainers:
		tabHelp = append([]string{h("Containers")}, ctr...)
		tabHelp = append(tabHelp, row("a", "toggle all / running only"), row("P", "prune stopped containers — confirms"))
	case TabCompose:
		tabHelp = []string{
			h("Compose — project list"),
			row("enter / →", "open project (services view)"),
			row("u / U", "up -d  /  up -d --build"),
			row("s / S", "start / stop"),
			row("r", "restart"),
			row("p", "pause / unpause"),
			row("d / D", "down  /  down --volumes — confirms"),
			row("b / B", "build  /  build --no-cache"),
			row("P", "pull images"),
			row("L", "follow aggregated project logs"),
			row("c", "show resolved compose config"),
			row("o", "add a project from a compose file or directory"),
			row("x", "forget a project that is down"),
			"",
			h("Compose — services view"),
			row("esc / ←", "back to projects"),
			row("u / U", "up / up --build this service"),
			row("s / S / r / p", "start / stop / restart / pause service"),
			row("b / B / P", "build / build --no-cache / pull service"),
			row("d", "remove service containers — confirms"),
			row("e / i / l / K", "shell / inspect / logs / kill container"),
			row("L / c", "project logs / compose config"),
			"",
			styles.DimStyle.Render("  Compose operations run in the background; live output appears in the"),
			styles.DimStyle.Render("  task panel. Press T for full output. Failures open it automatically."),
			styles.DimStyle.Render("  Projects you bring down stay listed (remembered in " + storeLocation() + ")."),
		}
	case TabLogs:
		tabHelp = []string{
			h("Logs"),
			row("[ ] / ← →", "previous / next source (⬡ = whole compose project)"),
			row("↑↓ pgup/pgdn ctrl+u/d", "scroll (scrolling up pauses follow)"),
			row("g / G", "top / bottom (G resumes follow)"),
			row("f", "toggle follow"),
			row("w", "toggle line wrap"),
			row("t", "toggle timestamps"),
			row("/", "filter lines"),
			row("c", "clear screen buffer"),
			row("r", "reconnect / reload"),
			row("E", "export buffer to ./dcompose-<name>-<time>.log"),
			"",
			styles.DimStyle.Render("  Red ▌ marks stderr lines. Streams auto-reconnect when a container restarts."),
		}
	case TabStats:
		tabHelp = append([]string{h("Stats"), row("o", "cycle sort: name / cpu / memory")}, ctr...)
	case TabImages:
		tabHelp = []string{
			h("Images"),
			row("n", "pull a new image by name"),
			row("p", "re-pull selected image"),
			row("d / D", "remove / force remove — confirms"),
			row("P", "prune dangling images — confirms"),
			row("A", "prune ALL unused images — confirms"),
			row("i / enter", "inspect"),
		}
	case TabVolumes:
		tabHelp = []string{
			h("Volumes"),
			row("d", "remove — confirms"),
			row("P", "prune unused anonymous volumes — confirms"),
			row("A", "prune ALL unused volumes — confirms"),
			row("i / enter", "inspect"),
		}
	case TabNetworks:
		tabHelp = []string{
			h("Networks"),
			row("d", "remove — confirms"),
			row("P", "prune unused networks — confirms"),
			row("i / enter", "inspect"),
		}
	}
	return strings.Join(append(global, tabHelp...), "\n")
}

func storeLocation() string {
	if p := docker.ProjectStorePath(); p != "" {
		return p
	}
	return "memory only: no user config directory"
}

type viewportModel = viewport.Model

func textinputBlink() tea.Cmd { return textinput.Blink }
