package views

import (
	"context"
	"fmt"
	"time"

	"dcompose/internal/docker"
	"dcompose/internal/tui/styles"

	tea "github.com/charmbracelet/bubbletea"
)

// DiskUsageMsg carries the result of a disk usage calculation.
type DiskUsageMsg struct {
	Usage *docker.DiskUsage
	Err   error
}

// diskKinds are the rows of the summary table, in display order.
var diskKinds = []string{"Images", "Containers", "Volumes", "Build cache"}

// DiskModel shows how much space Docker uses and what can be reclaimed.
type DiskModel struct {
	client  *docker.Client
	usage   *docker.DiskUsage
	loading bool
	err     error
	at      time.Time
	list    listState
	width   int
	height  int
}

// NewDiskModel creates the disk usage view.
func NewDiskModel(client *docker.Client) DiskModel {
	return DiskModel{client: client}
}

// SetSize sets the render dimensions.
func (m *DiskModel) SetSize(w, h int) { m.width, m.height = w, h }

// Capturing reports whether the view is consuming all key input.
func (m DiskModel) Capturing() bool { return false }

// Fetch recalculates disk usage. Returns nil if a calculation is running.
func (m *DiskModel) Fetch() tea.Cmd {
	if m.loading {
		return nil
	}
	m.loading = true
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		u, err := client.DiskUsage(ctx)
		return DiskUsageMsg{Usage: u, Err: err}
	}
}

func (m DiskModel) row(kind string) docker.DiskUsageRow {
	if m.usage == nil {
		return docker.DiskUsageRow{}
	}
	switch kind {
	case "Images":
		return m.usage.Images
	case "Containers":
		return m.usage.Containers
	case "Volumes":
		return m.usage.Volumes
	default:
		return m.usage.BuildCache
	}
}

func (m DiskModel) Update(msg tea.Msg) (DiskModel, tea.Cmd) {
	switch msg := msg.(type) {
	case DiskUsageMsg:
		m.loading = false
		m.err = msg.Err
		if msg.Err == nil {
			m.usage = msg.Usage
			m.at = time.Now()
		}
		return m, nil

	case tea.KeyMsg:
		k := msg.String()
		if m.list.handleNav(k, diskKinds, len(diskKinds)) {
			return m, nil
		}
		if k == "P" || k == "A" {
			return m, m.prune(diskKinds[m.list.cursor], k == "A")
		}
	}
	return m, nil
}

// prune asks for confirmation, then prunes the selected kind of object.
func (m DiskModel) prune(kind string, all bool) tea.Cmd {
	client := m.client
	type plan struct {
		title, body, key, what string
		run                    func(ctx context.Context) (int, uint64, error)
	}
	var p plan
	switch kind {
	case "Images":
		p = plan{"Prune dangling images", "Remove all dangling (untagged) images?", "prune:images", "images",
			func(ctx context.Context) (int, uint64, error) { return client.PruneImages(ctx, false) }}
		if all {
			p = plan{"Prune ALL unused images", "Remove every image not used by any container?", "prune:images", "images",
				func(ctx context.Context) (int, uint64, error) { return client.PruneImages(ctx, true) }}
		}
	case "Containers":
		p = plan{"Prune containers", "Remove ALL stopped containers?", "prune:containers", "containers",
			func(ctx context.Context) (int, uint64, error) { return client.PruneContainers(ctx) }}
	case "Volumes":
		p = plan{"Prune volumes", "Remove all unused ANONYMOUS volumes?", "prune:volumes", "volumes",
			func(ctx context.Context) (int, uint64, error) { return client.PruneVolumes(ctx, false) }}
		if all {
			p = plan{"Prune ALL unused volumes", "Remove every volume (named or anonymous) not used by a container?\nData in them will be lost.", "prune:volumes", "volumes",
				func(ctx context.Context) (int, uint64, error) { return client.PruneVolumes(ctx, true) }}
		}
	default:
		p = plan{"Prune build cache", "Remove dangling build cache?", "prune:buildcache", "build cache entries",
			func(ctx context.Context) (int, uint64, error) { return client.PruneBuildCache(ctx, false) }}
		if all {
			p = plan{"Prune ALL build cache", "Remove all unused build cache?\nThe next builds will start from scratch.", "prune:buildcache", "build cache entries",
				func(ctx context.Context) (int, uint64, error) { return client.PruneBuildCache(ctx, true) }}
		}
	}
	return confirm(p.title, p.body, true, tracked(p.key, "pruning", p.what, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		n, space, err := p.run(ctx)
		return FlashMsg{Text: fmt.Sprintf("pruned %d %s (%s freed)", n, p.what, formatBytes(space)), Err: err}
	}))
}

func (m DiskModel) View() string {
	right := ""
	switch {
	case m.loading:
		right = styles.WarnStyle.Render("⟳ calculating…")
	case m.usage != nil:
		right = styles.DimStyle.Render(fmt.Sprintf("total %s · reclaimable %s · %s",
			formatBytes(uint64(m.usage.TotalSize())), formatBytes(uint64(m.usage.TotalReclaimable())), m.at.Format("15:04:05")))
	}
	title := titleLine("Disk Usage", right, m.width)
	help := helpLine(m.width, "↑↓ select · P prune selected · A prune all unused (images, volumes, build cache) · R recalculate")

	if m.usage == nil {
		msg := "\n  Calculating disk usage… (this can take a few seconds with large volumes)"
		if m.err != nil && !m.loading {
			msg = "\n  " + styles.ErrorStyle.Render("Could not get disk usage: "+oneLineErr(m.err))
		}
		return title + "\n" + styles.SubtitleStyle.Render(msg) + "\n" + help
	}

	// Fixed widths keep numbers next to their labels on wide terminals.
	cols := []Column{
		{Title: "TYPE", Width: 13},
		{Title: "TOTAL", Width: 6},
		{Title: "ACTIVE", Width: 6},
		{Title: "SIZE", Width: 9},
		{Title: "RECLAIMABLE", Width: 16},
	}
	rows := make([][]string, len(diskKinds))
	for i, kind := range diskKinds {
		r := m.row(kind)
		pct := 0.0
		if r.Size > 0 {
			pct = float64(r.Reclaimable) / float64(r.Size) * 100
		}
		reclaim := fmt.Sprintf("%-8s %s", formatBytes(uint64(r.Reclaimable)), styles.DimStyle.Render(fmt.Sprintf("(%.0f%%)", pct)))
		rows[i] = []string{kind, fmt.Sprint(r.Total), fmt.Sprint(r.Active), formatBytes(uint64(r.Size)), reclaim}
	}
	body := renderTable(cols, rows, &m.list, m.width, len(diskKinds)+2)

	// Largest volumes: their sizes aren't shown anywhere else.
	// Lines left after title, summary table, blank, heading, header and help.
	room := m.height - (len(diskKinds) + 1) - 5
	var vols string
	if room > 0 && len(m.usage.VolumeSizes) > 0 {
		vols = "\n\n" + styles.KeyStyle.Render("  Largest volumes") + "\n"
		nameW := 13
		for _, v := range m.usage.VolumeSizes {
			if w := len(v.Name); w > nameW {
				nameW = w
			}
		}
		if limit := m.width - 24; nameW > limit {
			nameW = limit
		}
		vcols := []Column{
			{Title: "VOLUME", Width: nameW},
			{Title: "SIZE", Width: 9},
			{Title: "IN USE", Width: 6},
		}
		n := len(m.usage.VolumeSizes)
		if n > room {
			n = room
		}
		vrows := make([][]string, n)
		for i, v := range m.usage.VolumeSizes[:n] {
			size := styles.DimStyle.Render("?")
			if v.Size >= 0 {
				size = formatBytes(uint64(v.Size))
			}
			used := styles.DimStyle.Render("no")
			if v.InUse {
				used = styles.SuccessStyle.Render("yes")
			}
			vrows[i] = []string{v.Name, size, used}
		}
		noSel := listState{cursor: -1}
		vols += renderTable(vcols, vrows, &noSel, m.width, n+2)
	}
	return title + "\n" + body + vols + "\n" + help
}

func oneLineErr(err error) string {
	s := err.Error()
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
