package views

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dcompose/internal/docker"
	"dcompose/internal/tui/styles"

	tea "github.com/charmbracelet/bubbletea"
)

// ComposeModel shows compose projects and lets you drill into one.
type ComposeModel struct {
	projects   []docker.ComposeProject
	visible    []docker.ComposeProject
	containers []docker.ContainerInfo // all containers (for per-project data)
	list       listState
	filter     filterBox

	// Drill-in state
	drilled  bool
	proj     docker.ComposeProject // cached copy of the drilled project
	services []string              // services defined in compose config
	rows     []serviceRow
	sub      listState

	width  int
	height int
	client *docker.Client
}

// serviceRow is one line in the drilled view: a container, or a service
// that has no container yet.
type serviceRow struct {
	service string
	ctr     *docker.ContainerInfo
}

func (r serviceRow) key() string {
	if r.ctr != nil {
		return r.service + "|" + r.ctr.Name
	}
	return r.service + "|"
}

// ServicesLoadedMsg carries the service list of a compose project.
type ServicesLoadedMsg struct {
	Project  string
	Services []string
	Err      error
}

// NewComposeModel creates a new compose projects view.
func NewComposeModel(client *docker.Client) ComposeModel {
	return ComposeModel{client: client, filter: newFilterBox()}
}

// SetData updates projects and containers.
func (m *ComposeModel) SetData(projects []docker.ComposeProject, containers []docker.ContainerInfo) {
	m.projects = projects
	m.containers = containers
	if m.drilled {
		for _, p := range projects {
			if p.Name == m.proj.Name {
				m.proj = p
				break
			}
		}
		if !m.projectListed(m.proj.Name) {
			m.proj.Status = "down"
		}
	}
	m.apply()
}

func (m ComposeModel) projectListed(name string) bool {
	for _, p := range m.projects {
		if p.Name == name {
			return true
		}
	}
	return false
}

// SetSize sets the available render size.
func (m *ComposeModel) SetSize(w, h int) { m.width, m.height = w, h }

// Capturing reports whether the view is consuming all key input.
func (m ComposeModel) Capturing() bool { return m.filter.active }

// IsDrilled returns whether we're inside a project.
func (m ComposeModel) IsDrilled() bool { return m.drilled }

func (m *ComposeModel) apply() {
	m.visible = nil
	for _, p := range m.projects {
		if m.filter.match(p.Name, p.Status, strings.Join(p.ConfigFiles, " ")) {
			m.visible = append(m.visible, p)
		}
	}
	keys := make([]string, len(m.visible))
	for i, p := range m.visible {
		keys[i] = p.Name
	}
	m.list.sync(keys)

	if m.drilled {
		m.buildRows()
	}
}

func (m *ComposeModel) buildRows() {
	m.rows = nil
	seen := map[string]bool{}
	for i := range m.containers {
		c := m.containers[i]
		if c.Project != m.proj.Name {
			continue
		}
		seen[c.Service] = true
		cc := c
		m.rows = append(m.rows, serviceRow{service: c.Service, ctr: &cc})
	}
	for _, s := range m.services {
		if !seen[s] {
			m.rows = append(m.rows, serviceRow{service: s})
		}
	}
	// Sort by service then container name.
	for i := 1; i < len(m.rows); i++ {
		for j := i; j > 0 && rowLess(m.rows[j], m.rows[j-1]); j-- {
			m.rows[j], m.rows[j-1] = m.rows[j-1], m.rows[j]
		}
	}
	m.sub.sync(m.rowKeys())
}

func rowLess(a, b serviceRow) bool {
	if a.service != b.service {
		return a.service < b.service
	}
	an, bn := "", ""
	if a.ctr != nil {
		an = a.ctr.Name
	}
	if b.ctr != nil {
		bn = b.ctr.Name
	}
	return an < bn
}

func (m ComposeModel) rowKeys() []string {
	k := make([]string, len(m.rows))
	for i, r := range m.rows {
		k[i] = r.key()
	}
	return k
}

func (m ComposeModel) selectedProject() *docker.ComposeProject {
	if m.list.cursor < len(m.visible) {
		p := m.visible[m.list.cursor]
		return &p
	}
	return nil
}

func (m ComposeModel) selectedRow() *serviceRow {
	if m.sub.cursor < len(m.rows) {
		r := m.rows[m.sub.cursor]
		return &r
	}
	return nil
}

func loadServices(p docker.ComposeProject) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		s, err := docker.ComposeServices(ctx, p)
		return ServicesLoadedMsg{Project: p.Name, Services: s, Err: err}
	}
}

func (m ComposeModel) Update(msg tea.Msg) (ComposeModel, tea.Cmd) {
	switch msg := msg.(type) {
	case ServicesLoadedMsg:
		if m.drilled && msg.Project == m.proj.Name {
			m.services = msg.Services
			m.buildRows()
			if msg.Err != nil {
				return m, emit(FlashMsg{Text: "load services", Err: msg.Err})
			}
		}
		return m, nil

	case tea.KeyMsg:
		if m.drilled {
			return m.updateDrilled(msg)
		}
		return m.updateProjectList(msg)
	}
	return m, nil
}

// projectOps maps keys to compose operations shared by both levels.
// svc may be empty (whole project).
func projectOp(p docker.ComposeProject, k, svc string, paused bool) (tea.Cmd, bool) {
	target := p.Name
	var svcArgs []string
	if svc != "" {
		target = p.Name + "/" + svc
		svcArgs = []string{svc}
	}
	needsConfig := func(cmd tea.Cmd) tea.Cmd {
		if p.HasConfig() {
			return cmd
		}
		return emit(FlashMsg{Text: "compose " + target, Err: fmt.Errorf("compose file not found on disk (%s); use 'o' to point to it",
			strings.Join(p.ConfigFiles, ", "))})
	}
	run := func(title string, sub ...string) tea.Cmd {
		return task(title+" "+target, docker.ComposeArgs(p, append(sub, svcArgs...)...)...)
	}
	switch k {
	case "u":
		return needsConfig(run("up", "up", "-d", "--remove-orphans")), true
	case "U":
		return needsConfig(run("up --build", "up", "-d", "--build", "--remove-orphans")), true
	case "s":
		return run("start", "start"), true
	case "S":
		return run("stop", "stop"), true
	case "r":
		return run("restart", "restart"), true
	case "p":
		if paused {
			return run("unpause", "unpause"), true
		}
		return run("pause", "pause"), true
	case "b":
		return needsConfig(run("build", "build")), true
	case "B":
		return needsConfig(run("build --no-cache", "build", "--no-cache")), true
	case "P":
		return needsConfig(run("pull", "pull")), true
	case "c":
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			out, err := docker.ComposeConfig(ctx, p)
			if err != nil {
				return FlashMsg{Text: "compose config " + p.Name, Err: err}
			}
			return ViewerRequestMsg{Title: "compose config · " + p.Name, Content: out}
		}, true
	case "L":
		return emit(SwitchToProjectLogsMsg{Project: p.Name}), true
	}
	return nil, false
}

func (m ComposeModel) updateProjectList(msg tea.KeyMsg) (ComposeModel, tea.Cmd) {
	if m.filter.update(msg) {
		m.apply()
		return m, nil
	}
	k := msg.String()
	keys := make([]string, len(m.visible))
	for i, p := range m.visible {
		keys[i] = p.Name
	}
	if m.list.handleNav(k, keys, tableRows(m.height-2)) {
		m.list.window(len(m.visible), tableRows(m.height-2))
		return m, nil
	}
	if k == "o" {
		return m, emit(PromptRequestMsg{
			Title:       "Add compose project",
			Placeholder: "path to compose file or project directory",
			OnSubmit:    func(s string) tea.Cmd { return emit(AddProjectMsg{Path: s}) },
		})
	}

	p := m.selectedProject()
	if p == nil {
		return m, nil
	}
	proj := *p
	switch k {
	case "enter", "right", "l":
		m.drilled = true
		m.proj = proj
		m.services = nil
		m.sub = listState{}
		m.buildRows()
		return m, loadServices(proj)
	case "d":
		return m, confirm("Compose down",
			fmt.Sprintf("Stop and remove all containers and networks of %q?\nVolumes are kept.", proj.Name), true,
			task("down "+proj.Name, docker.ComposeArgs(proj, "down", "--remove-orphans")...))
	case "D":
		return m, confirm("Compose down + volumes",
			fmt.Sprintf("Remove containers, networks AND VOLUMES of %q?\nAll data in its volumes will be lost.", proj.Name), true,
			task("down -v "+proj.Name, docker.ComposeArgs(proj, "down", "--volumes", "--remove-orphans")...))
	case "x":
		if !proj.IsDown() {
			return m, emit(FlashMsg{Text: "forget " + proj.Name, Err: fmt.Errorf("project is not down; bring it down first")})
		}
		return m, confirm("Forget project", fmt.Sprintf("Remove %q from the remembered project list?\n(No files are deleted.)", proj.Name), false,
			emit(ForgetProjectMsg{Project: proj.Name}))
	}
	cmd, _ := projectOp(proj, k, "", strings.Contains(proj.Status, "paused"))
	return m, cmd
}

func (m ComposeModel) updateDrilled(msg tea.KeyMsg) (ComposeModel, tea.Cmd) {
	k := msg.String()
	if m.sub.handleNav(k, m.rowKeys(), tableRows(m.height-3)) {
		m.sub.window(len(m.rows), tableRows(m.height-3))
		return m, nil
	}
	switch k {
	case "esc", "backspace", "left", "h":
		m.drilled = false
		m.services = nil
		m.rows = nil
		return m, nil
	case "c", "L":
		cmd, _ := projectOp(m.proj, k, "", false)
		return m, cmd
	}

	row := m.selectedRow()
	if row == nil {
		return m, nil
	}
	svc := row.service
	ctr := row.ctr

	switch k {
	case "d":
		if ctr == nil && svc == "" {
			return m, nil
		}
		if svc == "" { // container labelled with project but without service label
			return m, nil
		}
		return m, confirm("Remove service containers",
			fmt.Sprintf("Stop and remove the container(s) of service %q?", svc), true,
			task("rm "+m.proj.Name+"/"+svc, docker.ComposeArgs(m.proj, "rm", "--stop", "--force", svc)...))
	case "s":
		if ctr == nil {
			// Not created yet: `start` would fail, so create it with up.
			cmd, _ := projectOp(m.proj, "u", svc, false)
			return m, cmd
		}
	case "K", "e", "i", "l", "enter":
		if ctr == nil {
			return m, emit(FlashMsg{Text: k + " " + svc, Err: fmt.Errorf("service %q has no container yet (press u to create it)", svc)})
		}
		cmd, _ := containerActions(m.client, k, ctr)
		return m, cmd
	}
	paused := ctr != nil && ctr.State == "paused"
	cmd, _ := projectOp(m.proj, k, svc, paused)
	return m, cmd
}

func (m ComposeModel) View() string {
	if m.drilled {
		return m.viewDrilled()
	}
	return m.viewProjectList()
}

func (m ComposeModel) projectCounts(name string) (running, total int) {
	for _, c := range m.containers {
		if c.Project == name {
			total++
			if c.State == "running" {
				running++
			}
		}
	}
	return
}

func projectStatusCell(p docker.ComposeProject) string {
	s := p.Status
	switch {
	case p.IsDown():
		return styles.StatusBadge("down")
	case strings.Contains(s, "running") && !strings.Contains(s, "exited"):
		return styles.BadgeRunning.Render(s)
	case strings.Contains(s, "running"):
		return styles.BadgeRestarting.Render(s)
	case strings.Contains(s, "paused"):
		return styles.BadgePaused.Render(s)
	default:
		return styles.BadgeStopped.Render(s)
	}
}

func (m ComposeModel) viewProjectList() string {
	title := titleLine(fmt.Sprintf("Compose Projects (%d)", len(m.projects)), m.filter.view(), m.width)
	help := helpLine(m.width, "enter open · u up · U up+build · s/S start/stop · r restart · p pause · d down · D down+vols · b build · P pull · L logs · c config · o add · x forget")

	if len(m.visible) == 0 {
		msg := "\n  No compose projects found.\n\n" +
			"  Projects appear here once they have containers, or press 'o' to add a\n" +
			"  compose file / directory (or start dcompose with a path argument)."
		return title + "\n" + styles.SubtitleStyle.Render(msg) + "\n" + help
	}

	cols := []Column{
		{Title: "PROJECT", Width: 16, Flex: 2},
		{Title: "STATUS", Width: 20, Flex: 1},
		{Title: "CONTAINERS", Width: 10},
		{Title: "CONFIG FILES", Width: 20, Flex: 4},
	}
	rows := make([][]string, len(m.visible))
	for i, p := range m.visible {
		run, tot := m.projectCounts(p.Name)
		counts := fmt.Sprintf("%d/%d up", run, tot)
		cfg := strings.Join(p.ConfigFiles, ", ")
		if !p.HasConfig() {
			if cfg == "" {
				cfg = styles.WarnStyle.Render("(unknown — press o to add)")
			} else {
				cfg = styles.WarnStyle.Render("missing: ") + cfg
			}
		} else {
			cfg = styles.DimStyle.Render(cfg)
		}
		rows[i] = []string{p.Name, projectStatusCell(p), counts, cfg}
	}
	body := renderTable(cols, rows, &m.list, m.width, m.height-2)
	return title + "\n" + body + "\n" + help
}

func (m ComposeModel) viewDrilled() string {
	p := m.proj
	run, tot := m.projectCounts(p.Name)
	title := titleLine("← "+p.Name, projectStatusCell(p)+styles.DimStyle.Render(fmt.Sprintf("  %d/%d running", run, tot)), m.width)
	sub := styles.DimStyle.Render("    " + truncate(strings.Join(p.ConfigFiles, ", "), m.width-6))
	help := helpLine(m.width, "esc back · u up · U up+build · s start · S stop · r restart · p pause · d rm · b build · P pull · e shell · i inspect · l logs · L all logs · K kill")

	if len(m.rows) == 0 {
		msg := "\n  No containers or services for this project."
		if !p.HasConfig() {
			msg += "\n  The compose file is not on disk, so services cannot be listed."
		} else if m.services == nil {
			msg = "\n  Loading services…"
		}
		return title + "\n" + sub + "\n" + styles.SubtitleStyle.Render(msg) + "\n" + help
	}

	cols := []Column{
		{Title: "SERVICE", Width: 14, Flex: 2},
		{Title: "CONTAINER", Width: 16, Flex: 2},
		{Title: "STATE", Width: 13},
		{Title: "STATUS", Width: 14, Flex: 2},
		{Title: "IMAGE", Width: 12, Flex: 2},
		{Title: "PORTS", Width: 10, Flex: 2},
	}
	rows := make([][]string, len(m.rows))
	for i, r := range m.rows {
		if r.ctr == nil {
			rows[i] = []string{r.service, styles.DimStyle.Render("—"), styles.DimStyle.Render("○ NOT CREATED"), "", "", ""}
			continue
		}
		c := r.ctr
		rows[i] = []string{r.service, c.Name, stateCell(*c), c.Status, c.Image, c.Ports}
	}
	body := renderTable(cols, rows, &m.sub, m.width, m.height-3)
	return title + "\n" + sub + "\n" + body + "\n" + help
}
