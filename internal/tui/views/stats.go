package views

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"dcompose/internal/docker"
	"dcompose/internal/tui/styles"

	tea "github.com/charmbracelet/bubbletea"
)

var statsSortModes = []string{"name", "cpu", "memory"}

// StatsModel displays real-time resource usage for containers.
type StatsModel struct {
	all      []docker.ContainerInfo
	visible  []docker.ContainerInfo
	stats    map[string]*docker.ContainerStats
	list     listState
	filter   filterBox
	sortMode int
	fetching bool
	width    int
	height   int
	client   *docker.Client
}

// NewStatsModel creates a new stats view.
func NewStatsModel(client *docker.Client) StatsModel {
	return StatsModel{client: client, stats: map[string]*docker.ContainerStats{}, filter: newFilterBox()}
}

// SetContainers updates the container list.
func (m *StatsModel) SetContainers(containers []docker.ContainerInfo) {
	m.all = containers
	m.apply()
}

// SetSize sets the render dimensions.
func (m *StatsModel) SetSize(w, h int) { m.width, m.height = w, h }

// Capturing reports whether the view is consuming all key input.
func (m StatsModel) Capturing() bool { return m.filter.active }

func (m *StatsModel) apply() {
	m.visible = nil
	for _, c := range m.all {
		if m.filter.match(c.Name, c.Image, c.Project, c.Service) {
			m.visible = append(m.visible, c)
		}
	}
	running := func(c docker.ContainerInfo) bool { return c.State == "running" }
	switch statsSortModes[m.sortMode] {
	case "cpu", "memory":
		mode := statsSortModes[m.sortMode]
		sort.SliceStable(m.visible, func(i, j int) bool {
			a, b := m.stats[m.visible[i].ID], m.stats[m.visible[j].ID]
			if (a != nil && running(m.visible[i])) != (b != nil && running(m.visible[j])) {
				return a != nil && running(m.visible[i])
			}
			if a == nil || b == nil {
				return false
			}
			if mode == "cpu" {
				return a.CPUPercent > b.CPUPercent
			}
			return a.MemUsage > b.MemUsage
		})
	default:
		sort.SliceStable(m.visible, func(i, j int) bool {
			if running(m.visible[i]) != running(m.visible[j]) {
				return running(m.visible[i])
			}
			return m.visible[i].Name < m.visible[j].Name
		})
	}
	m.list.sync(m.keys())
}

func (m StatsModel) keys() []string {
	k := make([]string, len(m.visible))
	for i, c := range m.visible {
		k[i] = containerKey(c)
	}
	return k
}

func (m StatsModel) selected() *docker.ContainerInfo {
	if m.list.cursor < len(m.visible) {
		c := m.visible[m.list.cursor]
		return &c
	}
	return nil
}

// StatsUpdateMsg carries updated stats for all running containers.
type StatsUpdateMsg struct {
	Stats map[string]*docker.ContainerStats
}

// FetchStats fetches stats for all running containers concurrently.
// Returns nil if a fetch is already in flight.
func (m *StatsModel) FetchStats() tea.Cmd {
	if m.fetching {
		return nil
	}
	var running []docker.ContainerInfo
	for _, c := range m.all {
		if c.State == "running" {
			running = append(running, c)
		}
	}
	if len(running) == 0 {
		return nil
	}
	m.fetching = true
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()

		var mu sync.Mutex
		var wg sync.WaitGroup
		result := make(map[string]*docker.ContainerStats)
		sem := make(chan struct{}, client.MaxParallel())
		for _, ctr := range running {
			wg.Add(1)
			go func(c docker.ContainerInfo) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				s, err := client.GetStats(ctx, c.ID)
				if err == nil && s != nil {
					s.ContainerName = c.Name
					mu.Lock()
					result[c.ID] = s
					mu.Unlock()
				}
			}(ctr)
		}
		wg.Wait()
		return StatsUpdateMsg{Stats: result}
	}
}

func (m StatsModel) Update(msg tea.Msg) (StatsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case StatsUpdateMsg:
		m.fetching = false
		m.stats = msg.Stats
		m.apply()
		return m, nil

	case tea.KeyMsg:
		if m.filter.update(msg) {
			m.apply()
			return m, nil
		}
		k := msg.String()
		if m.list.handleNav(k, m.keys(), tableRows(m.height-2)) {
			m.list.window(len(m.visible), tableRows(m.height-2))
			return m, nil
		}
		if k == "o" {
			m.sortMode = (m.sortMode + 1) % len(statsSortModes)
			m.apply()
			return m, nil
		}
		cmd, _ := containerActions(m.client, k, m.selected())
		return m, cmd
	}
	return m, nil
}

func (m StatsModel) View() string {
	running := 0
	var totalCPU float64
	var totalMem uint64
	for _, c := range m.all {
		if c.State == "running" {
			running++
			if s := m.stats[c.ID]; s != nil {
				totalCPU += s.CPUPercent
				totalMem += s.MemUsage
			}
		}
	}

	right := m.filter.view()
	if right == "" {
		right = styles.DimStyle.Render(fmt.Sprintf("Σ cpu %.1f%%  mem %s  ·  sort: %s", totalCPU, formatBytes(totalMem), statsSortModes[m.sortMode]))
	}
	title := titleLine(fmt.Sprintf("Resource Usage (%d running)", running), right, m.width)

	if len(m.visible) == 0 {
		return title + "\n" + styles.SubtitleStyle.Render("\n  No containers.") + "\n" + helpLine(m.width, "o sort · / filter")
	}

	barW := 10
	if m.width >= 130 {
		barW = 16
	}
	cols := []Column{
		{Title: "CONTAINER", Width: 16, Flex: 3},
		{Title: "CPU", Width: barW + 7},
		{Title: "MEMORY", Width: barW + 8},
		{Title: "MEM %", Width: 6},
		{Title: "NET RX/TX", Width: 13, Flex: 1},
	}
	showBlock := m.width >= 105
	showPids := m.width >= 92
	if showBlock {
		cols = append(cols, Column{Title: "BLOCK R/W", Width: 13, Flex: 1})
	}
	if showPids {
		cols = append(cols, Column{Title: "PIDS", Width: 5})
	}

	dash := styles.DimStyle.Render("—")
	rows := make([][]string, len(m.visible))
	for i, c := range m.visible {
		s := m.stats[c.ID]
		if s == nil || c.State != "running" || c.Pending != "" {
			badge := stateCell(c)
			if c.State == "running" && c.Pending == "" {
				badge = styles.DimStyle.Render("…")
			}
			row := []string{c.Name, badge, dash, dash, dash}
			if showBlock {
				row = append(row, dash)
			}
			if showPids {
				row = append(row, dash)
			}
			rows[i] = row
			continue
		}
		row := []string{
			c.Name,
			fmt.Sprintf("%s %5.1f%%", styles.RenderBar(s.CPUPercent, barW), s.CPUPercent),
			fmt.Sprintf("%s %7s", styles.RenderBar(s.MemPercent, barW), formatBytes(s.MemUsage)),
			fmt.Sprintf("%5.1f%%", s.MemPercent),
			formatBytes(s.NetInput) + "/" + formatBytes(s.NetOutput),
		}
		if showBlock {
			row = append(row, formatBytes(s.BlockRead)+"/"+formatBytes(s.BlockWrite))
		}
		if showPids {
			row = append(row, fmt.Sprintf("%5d", s.PIDs))
		}
		rows[i] = row
	}

	body := renderTable(cols, rows, &m.list, m.width, m.height-2)
	help := helpLine(m.width, "o sort · s start · S stop · r restart · p pause · K kill · d rm · e shell · i details · w browser · l logs · / filter")
	return title + "\n" + body + "\n" + help
}
