package views

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dcompose/internal/docker"
	"dcompose/internal/tui/styles"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ============================================================ messages
// Views never perform blocking work or show modal UI themselves. They emit
// these request messages and the root model handles them.

// FlashMsg reports the outcome of an action (shown in the status line).
// The root triggers a data refresh whenever it receives one.
type FlashMsg struct {
	Text  string
	Err   error
	OpKey string // set when this completes an OpRequestMsg
}

// ConfirmRequestMsg asks the root to show a yes/no dialog.
type ConfirmRequestMsg struct {
	Title     string
	Body      string
	Danger    bool
	OnConfirm tea.Cmd
}

// PromptRequestMsg asks the root to show a text-input dialog.
type PromptRequestMsg struct {
	Title       string
	Placeholder string
	Initial     string
	OnSubmit    func(string) tea.Cmd
}

// TaskRequestMsg asks the root to run a docker CLI command in the
// background with live output (used for compose operations and pulls).
type TaskRequestMsg struct {
	Title string
	Args  []string
}

// ExecShellMsg asks the root to suspend the TUI and open a shell in a container.
type ExecShellMsg struct {
	ContainerID string
	Name        string
}

// ViewerRequestMsg asks the root to show scrollable text (inspect, config).
type ViewerRequestMsg struct {
	Title   string
	Content string
}

// SwitchToLogsMsg requests switching to the logs tab for a container.
type SwitchToLogsMsg struct {
	ContainerID string
}

// SwitchToProjectLogsMsg requests aggregated logs for a compose project.
type SwitchToProjectLogsMsg struct {
	Project string
}

// ForgetProjectMsg asks the root to drop a remembered compose project.
type ForgetProjectMsg struct {
	Project string
}

// EditComposeMsg asks the root to open a project's compose files in an
// editor (suspending the TUI) and validate them afterwards.
type EditComposeMsg struct {
	Project docker.ComposeProject
}

// AddProjectMsg asks the root to register a compose file/directory.
type AddProjectMsg struct {
	Path string
}

// OpRequestMsg asks the root to run a short Docker API operation. The root
// marks Key as pending (shown immediately in the UI), rejects duplicate
// requests for the same key while it runs, and clears it when the
// resulting FlashMsg (with the same OpKey) arrives.
type OpRequestMsg struct {
	Key  string // pending-tracking key, e.g. "c:<id>"
	Verb string // progressive verb shown while running, e.g. "stopping"
	Name string // target name for messages
	Hint string // optional extra hint shown while pending
	Run  tea.Cmd
}

// ============================================================ helpers

func emit(msg tea.Msg) tea.Cmd { return func() tea.Msg { return msg } }

// op wraps a Docker API call as a tracked, pending operation.
func op(key, verb, name, desc, hint string, timeout time.Duration, fn func(ctx context.Context) error) tea.Cmd {
	return emit(OpRequestMsg{Key: key, Verb: verb, Name: name, Hint: hint, Run: func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		err := fn(ctx)
		return FlashMsg{Text: desc, Err: err, OpKey: key}
	}})
}

// tracked marks an arbitrary command as a pending operation under key.
func tracked(key, verb, name string, run tea.Cmd) tea.Cmd {
	return emit(OpRequestMsg{Key: key, Verb: verb, Name: name, Run: run})
}

func confirm(title, body string, danger bool, onYes tea.Cmd) tea.Cmd {
	return emit(ConfirmRequestMsg{Title: title, Body: body, Danger: danger, OnConfirm: onYes})
}

func task(title string, args ...string) tea.Cmd {
	return emit(TaskRequestMsg{Title: title, Args: args})
}

// InspectCmd runs `docker inspect` and opens the result in the viewer.
func InspectCmd(kind, ref, title string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, err := docker.RunDocker(ctx, "inspect", "--type", kind, ref)
		if err != nil {
			return FlashMsg{Text: "inspect " + title, Err: err}
		}
		return ViewerRequestMsg{Title: "inspect · " + title, Content: string(out)}
	}
}

// containerActions handles the container keybindings shared by the
// containers, stats, and compose-service views. Returns (cmd, handled).
func containerActions(client *docker.Client, k string, ctr *docker.ContainerInfo) (tea.Cmd, bool) {
	if ctr == nil {
		switch k {
		case "s", "S", "r", "p", "K", "d", "e", "i", "I", "w", "l", "enter":
			return nil, true
		}
		return nil, false
	}
	id, name := ctr.ID, ctr.Name
	life := "c:" + id // start/stop/restart/pause share one slot
	const t = 2 * time.Minute
	switch k {
	case "s":
		if ctr.State == "paused" {
			return op(life, "resuming", name, "unpause "+name, "", t, func(ctx context.Context) error { return client.UnpauseContainer(ctx, id) }), true
		}
		return op(life, "starting", name, "start "+name, "", t, func(ctx context.Context) error { return client.StartContainer(ctx, id) }), true
	case "S":
		return op(life, "stopping", name, "stop "+name, "graceful, up to 10s · K to kill now", t, func(ctx context.Context) error { return client.StopContainer(ctx, id) }), true
	case "r":
		return op(life, "restarting", name, "restart "+name, "graceful stop can take up to 10s", t, func(ctx context.Context) error { return client.RestartContainer(ctx, id) }), true
	case "p":
		if ctr.State == "paused" {
			return op(life, "resuming", name, "unpause "+name, "", t, func(ctx context.Context) error { return client.UnpauseContainer(ctx, id) }), true
		}
		return op(life, "pausing", name, "pause "+name, "", t, func(ctx context.Context) error { return client.PauseContainer(ctx, id) }), true
	case "K":
		return confirm("Kill container", fmt.Sprintf("Send SIGKILL to %s?", name), true,
			op("k:"+id, "killing", name, "kill "+name, "", t, func(ctx context.Context) error { return client.KillContainer(ctx, id) })), true
	case "d":
		return confirm("Remove container", fmt.Sprintf("Force-remove %s?\nIt will be stopped first if running.", name), true,
			op("d:"+id, "removing", name, "remove "+name, "", t, func(ctx context.Context) error { return client.RemoveContainer(ctx, id) })), true
	case "e":
		if ctr.State != "running" {
			return emit(FlashMsg{Text: "exec " + name, Err: fmt.Errorf("container is %s, not running", ctr.State)}), true
		}
		return emit(ExecShellMsg{ContainerID: id, Name: name}), true
	case "i":
		return DetailsCmd(client, id, name), true
	case "I":
		return InspectCmd("container", id, name), true
	case "w":
		return openPortCmd(client, ctr), true
	case "l", "enter":
		return emit(SwitchToLogsMsg{ContainerID: id}), true
	}
	return nil, false
}

// ============================================================ list state

// listState tracks cursor and scroll offset for a list, and remembers the
// selected item's key so the selection survives data refreshes and reorders.
type listState struct {
	cursor int
	offset int
	selKey string
}

// sync re-locates the selected key in keys after a data change.
//
// Keys may have the form "group|item" (e.g. "project/service|container").
// If the exact item is gone (e.g. compose is recreating a container), the
// cursor moves to another item of the same group rather than to whatever
// row now sits at the old index. The wanted key is kept, so the selection
// snaps back once the item reappears.
func (l *listState) sync(keys []string) {
	if l.selKey != "" {
		for i, k := range keys {
			if k == l.selKey {
				l.cursor = i
				return
			}
		}
		if g, _, ok := strings.Cut(l.selKey, "|"); ok && g != "" {
			for i, k := range keys {
				if strings.HasPrefix(k, g+"|") {
					l.cursor = i
					return
				}
			}
		}
		l.clamp(len(keys))
		return
	}
	l.clamp(len(keys))
	if l.cursor < len(keys) {
		l.selKey = keys[l.cursor]
	}
}

// containerKey is a stable selection key for a container: names survive
// recreation (new ID), and compose containers are grouped by service.
func containerKey(c docker.ContainerInfo) string {
	if c.Service != "" {
		return c.Project + "/" + c.Service + "|" + c.Name
	}
	return c.Name
}

func (l *listState) clamp(n int) {
	if l.cursor >= n {
		l.cursor = n - 1
	}
	if l.cursor < 0 {
		l.cursor = 0
	}
}

// handleNav processes navigation keys. Returns true if the key was consumed.
func (l *listState) handleNav(k string, keys []string, page int) bool {
	n := len(keys)
	if page < 1 {
		page = 1
	}
	switch k {
	case "up", "k":
		l.cursor--
	case "down", "j":
		l.cursor++
	case "pgup", "ctrl+u":
		l.cursor -= page
	case "pgdown", "ctrl+d":
		l.cursor += page
	case "home", "g":
		l.cursor = 0
	case "end", "G":
		l.cursor = n - 1
	default:
		return false
	}
	l.clamp(n)
	if l.cursor < n {
		l.selKey = keys[l.cursor]
	}
	return true
}

// window returns the [start,end) range of rows to show in height rows.
func (l *listState) window(n, height int) (int, int) {
	if height < 1 {
		height = 1
	}
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+height {
		l.offset = l.cursor - height + 1
	}
	if l.offset > n-height {
		l.offset = n - height
	}
	if l.offset < 0 {
		l.offset = 0
	}
	end := l.offset + height
	if end > n {
		end = n
	}
	return l.offset, end
}

// ============================================================ table

// Column describes a table column. Width is the minimum width; Flex is the
// share of any extra horizontal space the column receives.
type Column struct {
	Title string
	Width int
	Flex  int
}

func layoutColumns(cols []Column, total int) []int {
	widths := make([]int, len(cols))
	used, flexSum := 0, 0
	for i, c := range cols {
		widths[i] = c.Width
		used += c.Width + 1
		flexSum += c.Flex
	}
	extra := total - used
	if extra > 0 && flexSum > 0 {
		given := 0
		lastFlex := -1
		for i, c := range cols {
			if c.Flex > 0 {
				add := extra * c.Flex / flexSum
				widths[i] += add
				given += add
				lastFlex = i
			}
		}
		if lastFlex >= 0 {
			widths[lastFlex] += extra - given
		}
	}
	return widths
}

// renderTable renders a header plus the visible window of rows. Rows may
// contain ANSI styling; the selected row is rendered as a solid highlight.
func renderTable(cols []Column, rows [][]string, l *listState, width, height int) string {
	widths := layoutColumns(cols, width-2)

	var b strings.Builder
	var hdr []string
	for i, c := range cols {
		hdr = append(hdr, fit(c.Title, widths[i]))
	}
	b.WriteString("  " + styles.TableHeaderStyle.Padding(0).Render(strings.Join(hdr, " ")))

	start, end := l.window(len(rows), tableRows(height))
	for i := start; i < end; i++ {
		var cells []string
		for j := range cols {
			cell := ""
			if j < len(rows[i]) {
				cell = rows[i][j]
			}
			cells = append(cells, fit(cell, widths[j]))
		}
		line := strings.Join(cells, " ")
		b.WriteString("\n")
		if i == l.cursor {
			b.WriteString(styles.KeyStyle.Render("▸ ") + styles.RowSelectedStyle.Render(ansi.Strip(line)))
		} else {
			b.WriteString("  " + line)
		}
	}
	if len(rows) > end-start {
		b.WriteString("\n" + styles.DimStyle.Render(fmt.Sprintf("  %d–%d of %d", start+1, end, len(rows))))
	}
	return b.String()
}

// tableRows is the number of data rows renderTable shows in height lines
// (one line each for the header and the pager).
func tableRows(height int) int {
	if height < 3 {
		return 1
	}
	return height - 2
}

// fit truncates (ANSI- and width-aware) and pads s to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	if pad := w - ansi.StringWidth(s); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

// truncate shortens s to at most n display cells.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= n {
		return s
	}
	return ansi.Truncate(s, n, "…")
}

// ============================================================ filter

// filterBox is an inline "/" filter used by the list views.
type filterBox struct {
	input  textinput.Model
	active bool
}

func newFilterBox() filterBox {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "filter…"
	ti.CharLimit = 128
	return filterBox{input: ti}
}

func (f *filterBox) Value() string { return strings.TrimSpace(f.input.Value()) }

// update handles a key while the filter is focused. Returns true if consumed.
func (f *filterBox) update(msg tea.KeyMsg) bool {
	if !f.active {
		if msg.String() == "/" {
			f.active = true
			f.input.Focus()
			return true
		}
		if msg.String() == "esc" && f.Value() != "" {
			f.input.SetValue("")
			return true
		}
		return false
	}
	switch msg.String() {
	case "enter":
		f.active = false
		f.input.Blur()
	case "esc":
		f.active = false
		f.input.Blur()
		f.input.SetValue("")
	default:
		f.input, _ = f.input.Update(msg)
	}
	return true
}

// match reports whether any of the fields contains the filter (case-insensitive).
func (f *filterBox) match(fields ...string) bool {
	q := strings.ToLower(f.Value())
	if q == "" {
		return true
	}
	for _, s := range fields {
		if strings.Contains(strings.ToLower(s), q) {
			return true
		}
	}
	return false
}

func (f *filterBox) view() string {
	if f.active {
		return f.input.View()
	}
	if v := f.Value(); v != "" {
		return styles.DimStyle.Render("filter: ") + styles.KeyStyle.Render(v) + styles.DimStyle.Render("  (esc clears)")
	}
	return ""
}

// ============================================================ misc

// titleLine renders a view title with an optional right-aligned annotation.
func titleLine(title, right string, width int) string {
	t := styles.TitleStyle.MarginBottom(0).Render("  " + title)
	if right == "" {
		return t
	}
	gap := width - lipgloss.Width(t) - lipgloss.Width(right) - 1
	if gap < 2 {
		return t + "  " + right
	}
	return t + strings.Repeat(" ", gap) + right
}

// helpLine renders a key hint line, truncated to fit with a pointer to "?".
func helpLine(width int, s string) string {
	s = "  " + s
	if width > 10 && ansi.StringWidth(s) > width-1 {
		s = ansi.Truncate(s, width-12, "") + " … ? more"
	}
	return styles.HelpStyle.Render(s)
}

func formatBytes(b uint64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1fG", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1fM", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.1fK", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%dB", b)
	}
}

func humanSince(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

var pendingLabels = map[string]string{
	"stopping": "◌ STOPPING", "starting": "◌ STARTING", "restarting": "◌ RESTART",
	"pausing": "◌ PAUSING", "resuming": "◌ RESUMING", "killing": "◌ KILLING", "removing": "◌ REMOVING",
}

func stateCell(ctr docker.ContainerInfo) string {
	if ctr.Pending != "" {
		l := pendingLabels[ctr.Pending]
		if l == "" {
			l = "◌ " + strings.ToUpper(ctr.Pending)
		}
		return styles.WarnStyle.Render(l)
	}
	s := styles.StatusBadge(ctr.State)
	switch ctr.Health {
	case "healthy":
		s += styles.SuccessStyle.Render(" ♥")
	case "unhealthy":
		s += styles.ErrorStyle.Render(" ✗")
	case "starting":
		s += styles.WarnStyle.Render(" …")
	}
	return s
}
