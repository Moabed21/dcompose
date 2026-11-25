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

// VolumesModel lists docker volumes.
type VolumesModel struct {
	all     []docker.VolumeInfo
	visible []docker.VolumeInfo
	list    listState
	filter  filterBox
	width   int
	height  int
	client  *docker.Client
}

// NewVolumesModel creates the volumes view.
func NewVolumesModel(client *docker.Client) VolumesModel {
	return VolumesModel{client: client, filter: newFilterBox()}
}

// SetVolumes updates the volume list.
func (m *VolumesModel) SetVolumes(v []docker.VolumeInfo) {
	m.all = v
	m.apply()
}

// SetSize sets the render dimensions.
func (m *VolumesModel) SetSize(w, h int) { m.width, m.height = w, h }

// Capturing reports whether the view is consuming all key input.
func (m VolumesModel) Capturing() bool { return m.filter.active }

func (m *VolumesModel) apply() {
	m.visible = nil
	for _, v := range m.all {
		if m.filter.match(v.Name, v.Project, v.Driver, strings.Join(v.UsedBy, " ")) {
			m.visible = append(m.visible, v)
		}
	}
	m.list.sync(m.keys())
}

func (m VolumesModel) keys() []string {
	k := make([]string, len(m.visible))
	for i, v := range m.visible {
		k[i] = v.Name
	}
	return k
}

func (m VolumesModel) Update(msg tea.Msg) (VolumesModel, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.filter.update(km) {
		m.apply()
		return m, nil
	}
	k := km.String()
	if m.list.handleNav(k, m.keys(), tableRows(m.height-2)) {
		m.list.window(len(m.visible), tableRows(m.height-2))
		return m, nil
	}
	client := m.client
	prune := func(all bool) tea.Cmd {
		return tracked("prune:volumes", "pruning", "unused volumes", func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			n, space, err := client.PruneVolumes(ctx, all)
			return FlashMsg{Text: fmt.Sprintf("pruned %d volumes (%s freed)", n, formatBytes(space)), Err: err}
		})
	}
	switch k {
	case "P":
		return m, confirm("Prune volumes", "Remove all unused ANONYMOUS volumes?", true, prune(false))
	case "A":
		return m, confirm("Prune ALL unused volumes", "Remove every volume (named or anonymous) not used by a container?\nData in them will be lost.", true, prune(true))
	}

	if m.list.cursor >= len(m.visible) {
		return m, nil
	}
	v := m.visible[m.list.cursor]
	switch k {
	case "d":
		body := fmt.Sprintf("Remove volume %s?\nAll data in it will be lost.", v.Name)
		if len(v.UsedBy) > 0 {
			body += "\nIn use by: " + strings.Join(v.UsedBy, ", ") + " (remove those containers first)."
		}
		name := v.Name
		return m, confirm("Remove volume", body, true, op("vol:"+name, "removing", name, "remove volume "+name, "", 5*time.Minute, func(ctx context.Context) error {
			return client.RemoveVolume(ctx, name, false)
		}))
	case "i", "enter":
		return m, InspectCmd("volume", v.Name, v.Name)
	}
	return m, nil
}

func (m VolumesModel) View() string {
	title := titleLine(fmt.Sprintf("Volumes (%d)", len(m.all)), m.filter.view(), m.width)
	help := helpLine(m.width, "d remove · P prune anonymous · A prune all unused · i inspect · / filter")
	if len(m.visible) == 0 {
		return title + "\n" + styles.SubtitleStyle.Render("\n  No volumes.") + "\n" + help
	}
	cols := []Column{
		{Title: "NAME", Width: 20, Flex: 4},
		{Title: "PROJECT", Width: 10, Flex: 1},
		{Title: "DRIVER", Width: 7},
		{Title: "USED BY", Width: 12, Flex: 2},
		{Title: "CREATED", Width: 16},
	}
	rows := make([][]string, len(m.visible))
	for i, v := range m.visible {
		used := styles.DimStyle.Render("unused")
		if len(v.UsedBy) > 0 {
			used = styles.SuccessStyle.Render(strings.Join(v.UsedBy, ", "))
		}
		rows[i] = []string{v.Name, v.Project, v.Driver, used, v.CreatedAt}
	}
	return title + "\n" + renderTable(cols, rows, &m.list, m.width, m.height-2) + "\n" + help
}
