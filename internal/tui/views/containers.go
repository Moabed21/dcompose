package views

import (
	"context"
	"fmt"
	"time"

	"dcompose/internal/docker"
	"dcompose/internal/tui/styles"

	tea "github.com/charmbracelet/bubbletea"
)

// ContainersModel displays all containers on the host.
type ContainersModel struct {
	all         []docker.ContainerInfo
	visible     []docker.ContainerInfo
	list        listState
	filter      filterBox
	runningOnly bool
	width       int
	height      int
	client      *docker.Client
}

// NewContainersModel creates a new all-containers view.
func NewContainersModel(client *docker.Client) ContainersModel {
	return ContainersModel{client: client, filter: newFilterBox()}
}

// SetContainers updates the container list.
func (m *ContainersModel) SetContainers(containers []docker.ContainerInfo) {
	m.all = containers
	m.apply()
}

// SetSize sets the available render size.
func (m *ContainersModel) SetSize(w, h int) { m.width, m.height = w, h }

// Capturing reports whether the view is consuming all key input.
func (m ContainersModel) Capturing() bool { return m.filter.active }

func (m *ContainersModel) apply() {
	m.visible = nil
	for _, c := range m.all {
		if m.runningOnly && c.State != "running" {
			continue
		}
		if m.filter.match(c.Name, c.Image, c.Project, c.Service, c.State, c.Ports, c.ID) {
			m.visible = append(m.visible, c)
		}
	}
	m.list.sync(m.keys())
}

func (m ContainersModel) keys() []string {
	k := make([]string, len(m.visible))
	for i, c := range m.visible {
		k[i] = containerKey(c)
	}
	return k
}

func (m ContainersModel) tableHeight() int { return m.height - 2 }

// Selected returns the highlighted container.
func (m ContainersModel) Selected() *docker.ContainerInfo {
	if m.list.cursor < len(m.visible) {
		c := m.visible[m.list.cursor]
		return &c
	}
	return nil
}

func (m ContainersModel) Update(msg tea.Msg) (ContainersModel, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.filter.update(km) {
		m.apply()
		return m, nil
	}
	k := km.String()
	if m.list.handleNav(k, m.keys(), tableRows(m.tableHeight())) {
		m.list.window(len(m.visible), tableRows(m.tableHeight()))
		return m, nil
	}
	switch k {
	case "a":
		m.runningOnly = !m.runningOnly
		m.apply()
		return m, nil
	case "P":
		client := m.client
		return m, confirm("Prune containers", "Remove ALL stopped containers?", true,
			tracked("prune:containers", "pruning", "stopped containers", func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				n, space, err := client.PruneContainers(ctx)
				return FlashMsg{Text: fmt.Sprintf("pruned %d containers (%s freed)", n, formatBytes(space)), Err: err}
			}))
	}
	cmd, _ := containerActions(m.client, k, m.Selected())
	return m, cmd
}

func (m ContainersModel) View() string {
	scope := "all"
	if m.runningOnly {
		scope = "running"
	}
	title := titleLine(fmt.Sprintf("Containers (%d/%d %s)", len(m.visible), len(m.all), scope), m.filter.view(), m.width)

	var body string
	if len(m.visible) == 0 {
		body = styles.SubtitleStyle.Render("\n  No containers match.")
	} else {
		cols := []Column{
			{Title: "NAME", Width: 16, Flex: 3},
			{Title: "STATE", Width: 10},
			{Title: "STATUS", Width: 14, Flex: 2},
			{Title: "IMAGE", Width: 14, Flex: 3},
			{Title: "PORTS", Width: 10, Flex: 2},
			{Title: "PROJECT", Width: 10, Flex: 1},
		}
		rows := make([][]string, len(m.visible))
		for i, c := range m.visible {
			rows[i] = []string{c.Name, stateCell(c), c.Status, c.Image, c.Ports, c.Project}
		}
		body = renderTable(cols, rows, &m.list, m.width, m.tableHeight())
	}

	help := helpLine(m.width, "s start · S stop · r restart · p pause · K kill · d rm · e shell · i inspect · l logs · a all/running · P prune · / filter")
	return title + "\n" + body + "\n" + help
}
