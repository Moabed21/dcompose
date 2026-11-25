package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"dcompose/internal/docker"
	"dcompose/internal/tui/styles"
	"dcompose/internal/tui/views"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Tab identifiers.
type Tab int

const (
	TabContainers Tab = iota
	TabCompose
	TabLogs
	TabStats
	TabImages
	TabVolumes
	TabNetworks
	numTabs
)

var tabNames = [][]string{
	// full, short
	{"Containers", "Ctrs"},
	{"Compose", "Comp"},
	{"Logs", "Logs"},
	{"Stats", "Stats"},
	{"Images", "Imgs"},
	{"Volumes", "Vols"},
	{"Networks", "Nets"},
}

const (
	refreshInterval = 3 * time.Second
	flashDuration   = 8 * time.Second
	maxTaskLines    = 5000
	dockLines       = 4
)

// Model is the root bubbletea model.
type Model struct {
	client *docker.Client
	store  *docker.ProjectStore
	keys   styles.KeyMap

	containersView views.ContainersModel
	composeView    views.ComposeModel
	logsView       views.LogsModel
	statsView      views.StatsModel
	imagesView     views.ImagesModel
	volumesView    views.VolumesModel
	networkView    views.NetworkModel

	activeTab Tab
	width     int
	height    int
	ready     bool

	// Data
	containers []docker.ContainerInfo
	projects   []docker.ComposeProject
	images     []docker.ImageInfo
	volumes    []docker.VolumeInfo
	networks   []docker.NetworkInfo
	refreshing bool
	loaded     bool
	err        error

	// Status line
	flash    string
	flashErr bool
	flashAt  time.Time

	// Overlays
	overlay   overlayKind
	confirm   confirmDialog
	prompt    promptDialog
	viewer    viewportState
	showHelp  bool
	quitAfter bool

	// Background tasks
	tasks    []*taskState // current first; FIFO queue after it
	lastTask *taskState
	taskSeq  int

	// In-flight Docker API operations (key -> verb), e.g. "c:<id>" -> "stopping".
	pending map[string]string
}

type viewportState struct {
	title string
	vp    viewportModel
}

// NewModel creates the root app model.
func NewModel(client *docker.Client, store *docker.ProjectStore) Model {
	return Model{
		client:         client,
		store:          store,
		keys:           styles.DefaultKeyMap(),
		containersView: views.NewContainersModel(client),
		composeView:    views.NewComposeModel(client),
		logsView:       views.NewLogsModel(client),
		statsView:      views.NewStatsModel(client),
		imagesView:     views.NewImagesModel(client),
		volumesView:    views.NewVolumesModel(client),
		networkView:    views.NewNetworkModel(client),
		viewer:         viewportState{vp: newViewport()},
		pending:        map[string]string{},
	}
}

// ------------------------------------------------------------- messages

type tickMsg time.Time

type dataMsg struct {
	containers []docker.ContainerInfo
	projects   []docker.ComposeProject
	images     []docker.ImageInfo
	volumes    []docker.VolumeInfo
	networks   []docker.NetworkInfo
	fatal      error // containers could not be listed (daemon down)
	errs       []error
}

type projectAddedMsg struct {
	project docker.ComposeProject
	err     error
}

// ------------------------------------------------------------- tea

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refreshCmd(), tick())
}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.layout()
		return m, nil

	case tickMsg:
		cmds = append(cmds, tick())
		if !m.refreshing {
			m.refreshing = true
			cmds = append(cmds, m.refreshCmd())
		}
		if m.flash != "" && time.Since(m.flashAt) > flashDuration {
			m.flash = ""
		}
		if m.overlay == overlayTask {
			m.syncTaskViewer(false)
		}
		return m, tea.Batch(cmds...)

	case dataMsg:
		m.refreshing = false
		rc := m.applyData(msg)
		return m, rc

	case views.StatsUpdateMsg:
		m.statsView, _ = m.statsView.Update(msg)
		return m, nil

	case views.LogBatchMsg:
		var cmd tea.Cmd
		m.logsView, cmd = m.logsView.Update(msg)
		return m, cmd

	case views.ServicesLoadedMsg:
		var cmd tea.Cmd
		m.composeView, cmd = m.composeView.Update(msg)
		return m, cmd

	case TaskLinesMsg:
		rc := m.handleTaskLines(msg)
		return m, rc

	// ----- requests from views
	case views.FlashMsg:
		if msg.OpKey != "" {
			delete(m.pending, msg.OpKey)
			m.distributeContainers()
		}
		m.setFlash(msg.Text, msg.Err)
		rc := m.forceRefresh()
		return m, rc

	case views.OpRequestMsg:
		if verb, busy := m.pending[msg.Key]; busy {
			m.setFlash(fmt.Sprintf("%s is already %s — please wait", msg.Name, verb), errBusy)
			return m, nil
		}
		m.pending[msg.Key] = msg.Verb
		m.distributeContainers()
		text := msg.Verb + " " + msg.Name + "…"
		if msg.Hint != "" {
			text += "  (" + msg.Hint + ")"
		}
		m.setPending(text)
		key, run := msg.Key, msg.Run
		return m, func() tea.Msg {
			out := run()
			if f, ok := out.(views.FlashMsg); ok {
				f.OpKey = key
				return f
			}
			return views.FlashMsg{OpKey: key, Text: msg.Verb + " " + msg.Name}
		}

	case views.ConfirmRequestMsg:
		m.overlay = overlayConfirm
		m.confirm = confirmDialog{title: msg.Title, body: msg.Body, danger: msg.Danger, yes: !msg.Danger, onConfirm: msg.OnConfirm}
		return m, nil

	case views.PromptRequestMsg:
		m.overlay = overlayPrompt
		m.prompt = newPrompt(msg.Title, msg.Placeholder, msg.Initial, msg.OnSubmit, m.width)
		return m, textinputBlink()

	case views.TaskRequestMsg:
		rc := m.enqueueTask(msg)
		return m, rc

	case views.ViewerRequestMsg:
		m.openViewer(msg.Title, msg.Content)
		return m, nil

	case views.ExecShellMsg:
		return m, execShell(msg)

	case views.SwitchToLogsMsg:
		cmd := m.setTab(TabLogs, false)
		cmd2 := m.logsView.SelectContainer(msg.ContainerID)
		return m, tea.Batch(cmd, cmd2)

	case views.SwitchToProjectLogsMsg:
		cmd := m.setTab(TabLogs, false)
		cmd2 := m.logsView.SelectProject(msg.Project)
		return m, tea.Batch(cmd, cmd2)

	case views.ForgetProjectMsg:
		m.store.Forget(msg.Project)
		m.setFlash("forgot project "+msg.Project, nil)
		rc := m.forceRefresh()
		return m, rc

	case views.AddProjectMsg:
		path := msg.Path
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			p, err := docker.ResolveComposeFile(ctx, path)
			return projectAddedMsg{project: p, err: err}
		}

	case projectAddedMsg:
		if msg.err != nil {
			m.setFlash("add project", msg.err)
			return m, nil
		}
		m.store.Remember(msg.project)
		m.setFlash(fmt.Sprintf("added project %s (%s)", msg.project.Name, strings.Join(msg.project.ConfigFiles, ", ")), nil)
		rc := m.forceRefresh()
		return m, rc

	case tea.MouseMsg:
		rc := m.handleMouse(msg)
		return m, rc

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// Anything else (e.g. cursor blink) goes to the prompt or active view.
	if m.overlay == overlayPrompt {
		var cmd tea.Cmd
		m.prompt.input, cmd = m.prompt.input.Update(msg)
		return m, cmd
	}
	rc := m.updateActive(msg)
	return m, rc
}

// ------------------------------------------------------------- keys

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ctrl+c always works.
	if msg.String() == "ctrl+c" {
		return m.quit()
	}

	// Fast typing / key repeat can deliver several runes in one event. Split
	// them so each is handled as its own keypress. Pastes are only accepted
	// by text inputs, never interpreted as commands.
	textInput := m.overlay == overlayPrompt || (m.overlay == overlayNone && m.activeCapturing())
	if msg.Type == tea.KeyRunes && msg.Paste && !textInput {
		return m, nil
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 && !msg.Paste {
		var model tea.Model = m
		var cmds []tea.Cmd
		for _, r := range msg.Runes {
			var c tea.Cmd
			model, c = model.(Model).handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: msg.Alt})
			cmds = append(cmds, c)
		}
		return model, tea.Batch(cmds...)
	}

	switch m.overlay {
	case overlayConfirm:
		switch msg.String() {
		case "y", "Y":
			m.overlay = overlayNone
			return m.afterConfirm(m.confirm.onConfirm)
		case "n", "N", "esc", "q":
			m.overlay = overlayNone
			m.quitAfter = false
		case "left", "right", "tab", "shift+tab", "h", "l":
			m.confirm.yes = !m.confirm.yes
		case "enter":
			m.overlay = overlayNone
			if m.confirm.yes {
				return m.afterConfirm(m.confirm.onConfirm)
			}
			m.quitAfter = false
		}
		return m, nil

	case overlayPrompt:
		switch msg.String() {
		case "esc":
			m.overlay = overlayNone
			return m, nil
		case "enter":
			m.overlay = overlayNone
			if m.prompt.onSubmit != nil {
				return m, m.prompt.onSubmit(m.prompt.input.Value())
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.prompt.input, cmd = m.prompt.input.Update(msg)
		return m, cmd

	case overlayViewer, overlayTask:
		switch msg.String() {
		case "esc", "q", "enter":
			m.overlay = overlayNone
			m.showHelp = false
			return m, nil
		case "?":
			if m.showHelp {
				m.overlay = overlayNone
				m.showHelp = false
				return m, nil
			}
		case "T":
			if m.overlay == overlayTask {
				m.overlay = overlayNone
				return m, nil
			}
		case "x":
			if m.overlay == overlayTask && len(m.tasks) > 0 && !m.tasks[0].done {
				m.tasks[0].task.Cancel()
				m.setFlash("cancelling "+m.tasks[0].title+"…", nil)
				return m, nil
			}
		case "g", "home":
			m.viewer.vp.GotoTop()
			return m, nil
		case "G", "end":
			m.viewer.vp.GotoBottom()
			return m, nil
		}
		var cmd tea.Cmd
		m.viewer.vp, cmd = m.viewer.vp.Update(msg)
		return m, cmd
	}

	// No overlay. If the view is capturing input (e.g. filter), it gets everything.
	if m.activeCapturing() {
		rc := m.updateActive(msg)
		return m, rc
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m.quit()
	case key.Matches(msg, m.keys.Help):
		m.showHelp = true
		m.openViewer("Help — "+tabNames[m.activeTab][0], helpText(m.activeTab))
		m.viewer.vp.GotoTop()
		return m, nil
	case key.Matches(msg, m.keys.Refresh):
		m.setFlash("refreshing…", nil)
		rc := m.forceRefresh()
		return m, rc
	case key.Matches(msg, m.keys.Tasks):
		if m.currentOrLastTask() == nil {
			m.setFlash("no background tasks yet", nil)
			return m, nil
		}
		m.overlay = overlayTask
		m.syncTaskViewer(true)
		return m, nil
	case key.Matches(msg, m.keys.NextTab):
		rc := m.setTab((m.activeTab+1)%numTabs, true)
		return m, rc
	case key.Matches(msg, m.keys.PrevTab):
		rc := m.setTab((m.activeTab+numTabs-1)%numTabs, true)
		return m, rc
	}
	for i, b := range m.keys.Tabs {
		if key.Matches(msg, b) {
			rc := m.setTab(Tab(i), true)
			return m, rc
		}
	}
	rc := m.updateActive(msg)
	return m, rc
}

func (m Model) afterConfirm(cmd tea.Cmd) (tea.Model, tea.Cmd) {
	if m.quitAfter {
		for _, t := range m.tasks {
			if !t.done {
				t.task.Cancel()
			}
		}
		m.logsView.StopLogStream()
		return m, tea.Quit
	}
	return m, cmd
}

func (m Model) quit() (tea.Model, tea.Cmd) {
	if len(m.tasks) > 0 && !m.tasks[0].done && !m.quitAfter {
		m.quitAfter = true
		m.overlay = overlayConfirm
		m.confirm = confirmDialog{
			title:  "Quit while a task is running?",
			body:   fmt.Sprintf("%q is still running (%d queued).\nQuitting will interrupt it.", m.tasks[0].title, len(m.tasks)-1),
			danger: true,
		}
		return m, nil
	}
	m.logsView.StopLogStream()
	return m, tea.Quit
}

// ------------------------------------------------------------- mouse

func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if m.overlay == overlayViewer || m.overlay == overlayTask {
		var cmd tea.Cmd
		m.viewer.vp, cmd = m.viewer.vp.Update(msg)
		return cmd
	}
	if m.overlay != overlayNone {
		return nil
	}
	if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft && msg.Y == 0 {
		for i, r := range m.tabRanges() {
			if msg.X >= r[0] && msg.X < r[1] {
				return m.setTab(Tab(i), true)
			}
		}
		return nil
	}
	if m.activeTab == TabLogs {
		var cmd tea.Cmd
		m.logsView, cmd = m.logsView.Update(msg)
		return cmd
	}
	if msg.Action == tea.MouseActionPress {
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			return m.updateActive(tea.KeyMsg{Type: tea.KeyUp})
		case tea.MouseButtonWheelDown:
			return m.updateActive(tea.KeyMsg{Type: tea.KeyDown})
		}
	}
	return nil
}

// ------------------------------------------------------------- tabs & views

// setTab switches tabs, managing the log stream lifecycle.
func (m *Model) setTab(t Tab, activateLogs bool) tea.Cmd {
	if t == m.activeTab {
		return nil
	}
	var cmds []tea.Cmd
	if m.activeTab == TabLogs {
		m.logsView.Deactivate()
	}
	m.activeTab = t
	switch t {
	case TabLogs:
		if activateLogs {
			cmds = append(cmds, m.logsView.Activate())
		}
	case TabStats:
		cmds = append(cmds, m.statsView.FetchStats())
	}
	return tea.Batch(cmds...)
}

func (m *Model) activeCapturing() bool {
	switch m.activeTab {
	case TabContainers:
		return m.containersView.Capturing()
	case TabCompose:
		return m.composeView.Capturing()
	case TabLogs:
		return m.logsView.Capturing()
	case TabStats:
		return m.statsView.Capturing()
	case TabImages:
		return m.imagesView.Capturing()
	case TabVolumes:
		return m.volumesView.Capturing()
	case TabNetworks:
		return m.networkView.Capturing()
	}
	return false
}

func (m *Model) updateActive(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch m.activeTab {
	case TabContainers:
		m.containersView, cmd = m.containersView.Update(msg)
	case TabCompose:
		m.composeView, cmd = m.composeView.Update(msg)
	case TabLogs:
		m.logsView, cmd = m.logsView.Update(msg)
	case TabStats:
		m.statsView, cmd = m.statsView.Update(msg)
	case TabImages:
		m.imagesView, cmd = m.imagesView.Update(msg)
	case TabVolumes:
		m.volumesView, cmd = m.volumesView.Update(msg)
	case TabNetworks:
		m.networkView, cmd = m.networkView.Update(msg)
	}
	return cmd
}

// ------------------------------------------------------------- data

func (m *Model) forceRefresh() tea.Cmd {
	m.refreshing = true
	return m.refreshCmd()
}

func (m Model) refreshCmd() tea.Cmd {
	client, store := m.client, m.store
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		var d dataMsg
		containers, err := client.ListContainers(ctx)
		if err != nil {
			d.fatal = err
			return d
		}
		d.containers = containers

		var wg sync.WaitGroup
		var mu sync.Mutex
		addErr := func(err error) {
			if err != nil {
				mu.Lock()
				d.errs = append(d.errs, err)
				mu.Unlock()
			}
		}
		wg.Add(4)
		go func() {
			defer wg.Done()
			p, err := client.ListComposeProjects(ctx, store)
			addErr(err)
			d.projects = p
		}()
		go func() {
			defer wg.Done()
			i, err := client.ListImages(ctx, containers)
			addErr(err)
			d.images = i
		}()
		go func() {
			defer wg.Done()
			v, err := client.ListVolumes(ctx, containers)
			addErr(err)
			d.volumes = v
		}()
		go func() {
			defer wg.Done()
			n, err := client.ListNetworks(ctx)
			addErr(err)
			d.networks = n
		}()
		wg.Wait()
		return d
	}
}

func (m *Model) applyData(d dataMsg) tea.Cmd {
	if d.fatal != nil {
		m.err = d.fatal
		return nil
	}
	m.loaded = true
	m.err = errors.Join(d.errs...)

	m.containers = d.containers
	// Only overwrite the secondary lists if they loaded, so a transient
	// failure doesn't blank the view.
	if d.projects != nil || m.projects == nil {
		m.projects = d.projects
	}
	if d.images != nil || m.images == nil {
		m.images = d.images
		m.imagesView.SetImages(d.images)
	}
	if d.volumes != nil || m.volumes == nil {
		m.volumes = d.volumes
		m.volumesView.SetVolumes(d.volumes)
	}
	if d.networks != nil || m.networks == nil {
		m.networks = d.networks
		m.networkView.SetNetworks(d.networks)
	}
	m.distributeContainers()

	var cmds []tea.Cmd
	cmds = append(cmds, m.logsView.SetSources(m.projects, d.containers))
	if m.activeTab == TabStats {
		cmds = append(cmds, m.statsView.FetchStats())
	}
	return tea.Batch(cmds...)
}

var errBusy = errors.New("busy")

// distributeContainers pushes the container list, annotated with pending
// operations, to every view that shows containers.
func (m *Model) distributeContainers() {
	ctrs := make([]docker.ContainerInfo, len(m.containers))
	copy(ctrs, m.containers)
	for i := range ctrs {
		id := ctrs[i].ID
		for _, k := range []string{"d:", "k:", "c:"} { // most drastic first
			if v, ok := m.pending[k+id]; ok {
				ctrs[i].Pending = v
				break
			}
		}
	}
	m.containersView.SetContainers(ctrs)
	m.statsView.SetContainers(ctrs)
	m.composeView.SetData(m.projects, ctrs)
}

// setPending shows an in-progress message that stays until replaced.
func (m *Model) setPending(text string) {
	m.flash = "⟳ " + text
	m.flashErr = false
	m.flashAt = time.Now().Add(time.Hour) // don't expire while running
}

func (m *Model) setFlash(text string, err error) {
	if errors.Is(err, errBusy) {
		m.flash = "⚠ " + text
		m.flashErr = true
		m.flashAt = time.Now()
		return
	}
	m.flashAt = time.Now()
	if err != nil {
		m.flash = fmt.Sprintf("✗ %s: %s", text, oneLine(err.Error()))
		m.flashErr = true
		return
	}
	if strings.HasSuffix(text, "…") {
		m.flash = "⟳ " + text
	} else {
		m.flash = "✓ " + text
	}
	m.flashErr = false
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	if len(lines) > 1 {
		return strings.TrimSpace(lines[len(lines)-1]) + " (+" + fmt.Sprint(len(lines)-1) + " lines)"
	}
	return s
}

// ------------------------------------------------------------- tasks

func (m *Model) enqueueTask(req views.TaskRequestMsg) tea.Cmd {
	m.taskSeq++
	t := &taskState{id: m.taskSeq, title: req.Title, args: req.Args}
	m.tasks = append(m.tasks, t)
	if len(m.tasks) == 1 {
		return m.startTask()
	}
	m.setFlash(fmt.Sprintf("queued %s (%d ahead)", req.Title, len(m.tasks)-1), nil)
	return nil
}

func (m *Model) startTask() tea.Cmd {
	if len(m.tasks) == 0 {
		m.layout()
		return nil
	}
	t := m.tasks[0]
	t.started = time.Now()
	dt, err := docker.StartDocker(t.args)
	if err != nil {
		t.done, t.err, t.ended = true, err, time.Now()
		m.lastTask = t
		m.tasks = m.tasks[1:]
		m.setFlash(t.title, err)
		return m.startTask()
	}
	t.task = dt
	m.layout()
	return waitTask(dt, t.id)
}

func (m *Model) handleTaskLines(msg TaskLinesMsg) tea.Cmd {
	if len(m.tasks) == 0 || m.tasks[0].id != msg.ID {
		return nil
	}
	t := m.tasks[0]
	t.lines = append(t.lines, msg.Lines...)
	if over := len(t.lines) - maxTaskLines; over > 0 {
		t.lines = append([]string(nil), t.lines[over:]...)
	}
	if !msg.Done {
		if m.overlay == overlayTask {
			m.syncTaskViewer(false)
		}
		return waitTask(t.task, t.id)
	}

	t.done, t.err, t.ended = true, msg.Err, time.Now()
	m.lastTask = t
	m.tasks = m.tasks[1:]
	if t.err != nil {
		m.setFlash(t.title, fmt.Errorf("%v — press T for output", t.err))
		// Show the output so the user can see what went wrong.
		if m.overlay == overlayNone || m.overlay == overlayTask {
			m.overlay = overlayTask
		}
	} else {
		m.setFlash(fmt.Sprintf("%s (%s)", t.title, t.elapsed()), nil)
	}
	if m.overlay == overlayTask {
		m.syncTaskViewer(true)
	}
	return tea.Batch(m.startTask(), m.forceRefresh())
}

func (m *Model) currentOrLastTask() *taskState {
	if len(m.tasks) > 0 && m.tasks[0].task != nil {
		return m.tasks[0]
	}
	return m.lastTask
}

func (m *Model) syncTaskViewer(forceBottom bool) {
	t := m.currentOrLastTask()
	if t == nil {
		return
	}
	atBottom := m.viewer.vp.AtBottom()
	m.viewer.title = "task · " + t.title
	m.viewer.vp.SetContent(t.content(m.viewer.vp.Width))
	if forceBottom || atBottom {
		m.viewer.vp.GotoBottom()
	}
}

func (m *Model) openViewer(title, content string) {
	m.overlay = overlayViewer
	m.viewer.title = title
	m.viewer.vp.SetContent(strings.TrimRight(content, "\n"))
	m.viewer.vp.GotoTop()
	m.viewer.vp.SetXOffset(0)
}

// ------------------------------------------------------------- exec

func execShell(msg views.ExecShellMsg) tea.Cmd {
	c := exec.Command("docker", "exec", "-it", msg.ContainerID, "sh", "-c",
		"if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi")
	name := msg.Name
	return tea.ExecProcess(c, func(err error) tea.Msg {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() != 126 && ee.ExitCode() != 127 {
			err = nil // the shell's own exit status is not our error
		}
		if errors.As(err, &ee) {
			err = fmt.Errorf("no shell available in container (exit %d)", ee.ExitCode())
		}
		return views.FlashMsg{Text: "shell in " + name + " closed", Err: err}
	})
}

// ------------------------------------------------------------- layout & view

func (m *Model) dockHeight() int {
	if len(m.tasks) > 0 && m.tasks[0].task != nil {
		return dockLines + 2
	}
	return 0
}

func (m *Model) contentHeight() int {
	h := m.height - 4 - m.dockHeight() // header(2) + flash(1) + status(1)
	if h < 5 {
		h = 5
	}
	return h
}

func (m *Model) layout() {
	if !m.ready {
		return
	}
	w, h := m.width, m.contentHeight()
	m.containersView.SetSize(w, h)
	m.composeView.SetSize(w, h)
	m.logsView.SetSize(w, h)
	m.statsView.SetSize(w, h)
	m.imagesView.SetSize(w, h)
	m.volumesView.SetSize(w, h)
	m.networkView.SetSize(w, h)

	vw, vh := w-4, m.height-4-3 // overlay border+title+hint
	if vw < 20 {
		vw = 20
	}
	if vh < 3 {
		vh = 3
	}
	m.viewer.vp.Width, m.viewer.vp.Height = vw, vh
}

func (m Model) View() string {
	if !m.ready {
		return "\n  Initializing…"
	}
	header := m.renderHeader()
	ch := m.contentHeight()

	var content string
	switch m.overlay {
	case overlayViewer, overlayTask:
		hint := "↑↓ pgup/pgdn scroll · ←→ pan · g/G top/bottom · esc close"
		if m.overlay == overlayTask {
			hint = "↑↓ pgup/pgdn scroll · g/G top/bottom · T/esc close"
			if len(m.tasks) > 0 && !m.tasks[0].done {
				hint += " · x cancel task"
			}
		}
		body := framedViewer(m.viewer.title, hint, m.viewer.vp, m.width)
		return lipgloss.JoinVertical(lipgloss.Left, header,
			lipgloss.NewStyle().Height(m.height-4).MaxHeight(m.height-4).Render(body),
			m.renderFlash(), m.renderStatusBar())
	case overlayConfirm:
		content = lipgloss.Place(m.width, ch, lipgloss.Center, lipgloss.Center, m.confirm.view(m.width))
	case overlayPrompt:
		content = lipgloss.Place(m.width, ch, lipgloss.Center, lipgloss.Center, m.prompt.view())
	default:
		if !m.loaded && m.err == nil {
			content = styles.SubtitleStyle.Render("\n  Loading from Docker…")
		} else {
			content = m.activeView()
		}
	}

	content = lipgloss.NewStyle().MaxWidth(m.width).Height(ch).MaxHeight(ch).Render(content)
	parts := []string{header, content}
	if dock := m.renderDock(); dock != "" {
		parts = append(parts, dock)
	}
	parts = append(parts, m.renderFlash(), m.renderStatusBar())
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m Model) activeView() string {
	switch m.activeTab {
	case TabContainers:
		return m.containersView.View()
	case TabCompose:
		return m.composeView.View()
	case TabLogs:
		return m.logsView.View()
	case TabStats:
		return m.statsView.View()
	case TabImages:
		return m.imagesView.View()
	case TabVolumes:
		return m.volumesView.View()
	case TabNetworks:
		return m.networkView.View()
	}
	return ""
}

// tabBar describes how the header tabs are laid out.
type tabBar struct {
	labels  []string
	pad     int  // padding on each side of a label
	gap     int  // spaces between labels
	compact bool // short logo
}

// tabBarLayout picks the widest tab-bar style that fits the terminal.
func (m Model) tabBarLayout() tabBar {
	configs := []struct {
		short, compact bool
		pad, gap       int
	}{
		{false, false, 2, 0},
		{false, false, 1, 0},
		{true, false, 1, 0},
		{true, false, 0, 1},
		{true, true, 0, 1},
	}
	var tb tabBar
	for _, cfg := range configs {
		tb = tabBar{labels: make([]string, numTabs), pad: cfg.pad, gap: cfg.gap, compact: cfg.compact}
		total := lipgloss.Width(logo(cfg.compact)) + 1 + cfg.gap*int(numTabs-1)
		for i := range tb.labels {
			name := tabNames[i][0]
			if cfg.short {
				name = tabNames[i][1]
			}
			tb.labels[i] = fmt.Sprintf("%d %s", i+1, name)
			total += len([]rune(tb.labels[i])) + 2*cfg.pad
		}
		if total <= m.width {
			break
		}
	}
	return tb
}

func logo(compact bool) string {
	if compact {
		return styles.LogoStyle.Render(" ⬡")
	}
	return styles.LogoStyle.Render(" ⬡ dcompose")
}

// tabRanges returns [start,end) x-ranges of each tab in the header.
func (m Model) tabRanges() [][2]int {
	tb := m.tabBarLayout()
	x := lipgloss.Width(logo(tb.compact)) + 1
	out := make([][2]int, len(tb.labels))
	for i, l := range tb.labels {
		w := len([]rune(l)) + 2*tb.pad
		out[i] = [2]int{x, x + w}
		x += w + tb.gap
	}
	return out
}

func (m Model) renderHeader() string {
	tb := m.tabBarLayout()
	var tabs []string
	for i, l := range tb.labels {
		if Tab(i) == m.activeTab {
			tabs = append(tabs, styles.TabActive.Padding(0, tb.pad).Render(l))
		} else {
			tabs = append(tabs, styles.TabInactive.Padding(0, tb.pad).Render(l))
		}
	}
	line := logo(tb.compact) + " " + strings.Join(tabs, strings.Repeat(" ", tb.gap))
	line = lipgloss.NewStyle().MaxWidth(m.width).Render(line)
	divider := lipgloss.NewStyle().Foreground(styles.ColorBorder).Render(strings.Repeat("─", m.width))
	return line + "\n" + divider
}

func (m Model) renderDock() string {
	if len(m.tasks) == 0 || m.tasks[0].task == nil {
		return ""
	}
	t := m.tasks[0]
	head := styles.WarnStyle.Render("⟳ "+t.title) + styles.DimStyle.Render(fmt.Sprintf("  %s", t.elapsed()))
	if q := len(m.tasks) - 1; q > 0 {
		head += styles.DimStyle.Render(fmt.Sprintf("  (+%d queued)", q))
	}
	head += styles.HelpStyle.Render("   T full output")
	lines := t.lines
	if len(lines) > dockLines-1 {
		lines = lines[len(lines)-(dockLines-1):]
	}
	body := []string{head}
	for _, l := range lines {
		body = append(body, styles.DimStyle.Render(truncateStr(l, m.width-6)))
	}
	for len(body) < dockLines {
		body = append(body, "")
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.ColorYellow).
		Padding(0, 1).
		Width(m.width - 2).
		Render(strings.Join(body, "\n"))
}

func (m Model) renderFlash() string {
	var s string
	switch {
	case m.flash != "" && m.flashErr:
		s = styles.ErrorStyle.Render(" " + m.flash)
	case m.flash != "":
		s = styles.SuccessStyle.Render(" " + m.flash)
	case m.err != nil:
		s = styles.ErrorStyle.Render(" ⚠ " + oneLine(m.err.Error()))
	}
	return lipgloss.NewStyle().MaxWidth(m.width).Render(s)
}

func (m Model) renderStatusBar() string {
	running, stopped := 0, 0
	for _, c := range m.containers {
		if c.State == "running" {
			running++
		} else {
			stopped++
		}
	}
	upProjects := 0
	for _, p := range m.projects {
		if !p.IsDown() {
			upProjects++
		}
	}
	left := fmt.Sprintf(" %s %d  %s %d  %s %d/%d  %s %d",
		lipgloss.NewStyle().Foreground(styles.ColorGreen).Render("●"), running,
		lipgloss.NewStyle().Foreground(styles.ColorRed).Render("■"), stopped,
		lipgloss.NewStyle().Foreground(styles.ColorPurple).Render("⬡"), upProjects, len(m.projects),
		lipgloss.NewStyle().Foreground(styles.ColorBlue).Render("◫"), len(m.images),
	)
	if m.refreshing {
		left += styles.DimStyle.Render("  ⟳")
	}
	k := styles.StatusBarKeyStyle.Render
	right := fmt.Sprintf("%s help  %s refresh  %s tasks  %s quit ", k("?"), k("R"), k("T"), k("q"))
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return styles.StatusBarStyle.MaxWidth(m.width).Render(left)
	}
	return styles.StatusBarStyle.Render(left + strings.Repeat(" ", gap) + right)
}
