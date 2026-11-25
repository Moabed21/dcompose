package views

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dcompose/internal/docker"
	"dcompose/internal/tui/styles"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// NetworkModel displays networks in a two-pane layout.
type NetworkModel struct {
	all     []docker.NetworkInfo
	visible []docker.NetworkInfo
	list    listState
	filter  filterBox
	width   int
	height  int
	client  *docker.Client
}

// NewNetworkModel creates a new network view.
func NewNetworkModel(client *docker.Client) NetworkModel {
	return NetworkModel{client: client, filter: newFilterBox()}
}

// SetNetworks updates the network list.
func (m *NetworkModel) SetNetworks(networks []docker.NetworkInfo) {
	m.all = networks
	m.apply()
}

// SetSize sets the render dimensions.
func (m *NetworkModel) SetSize(w, h int) { m.width, m.height = w, h }

// Capturing reports whether the view is consuming all key input.
func (m NetworkModel) Capturing() bool { return m.filter.active }

func (m *NetworkModel) apply() {
	m.visible = nil
	for _, n := range m.all {
		if m.filter.match(n.Name, n.Driver, n.Project, n.Subnet, strings.Join(n.Containers, " ")) {
			m.visible = append(m.visible, n)
		}
	}
	m.list.sync(m.keys())
}

func (m NetworkModel) keys() []string {
	k := make([]string, len(m.visible))
	for i, n := range m.visible {
		k[i] = n.Name
	}
	return k
}

func (m NetworkModel) listRows() int {
	r := m.height - 4 // title, help, borders
	if m.width < 70 {
		r = 1
	}
	if r < 1 {
		r = 1
	}
	return r - 1 // header line inside the box
}

func (m NetworkModel) Update(msg tea.Msg) (NetworkModel, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.filter.update(km) {
		m.apply()
		return m, nil
	}
	k := km.String()
	if m.list.handleNav(k, m.keys(), m.listRows()) {
		m.list.window(len(m.visible), m.listRows())
		return m, nil
	}
	client := m.client
	if k == "P" {
		return m, confirm("Prune networks", "Remove all networks not used by any container?", true, tracked("prune:networks", "pruning", "unused networks", func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			n, err := client.PruneNetworks(ctx)
			return FlashMsg{Text: fmt.Sprintf("pruned %d networks", n), Err: err}
		}))
	}
	if m.list.cursor >= len(m.visible) {
		return m, nil
	}
	n := m.visible[m.list.cursor]
	switch k {
	case "d":
		if n.IsBuiltin() {
			return m, emit(FlashMsg{Text: "remove network " + n.Name, Err: fmt.Errorf("%s is a predefined network and cannot be removed", n.Name)})
		}
		body := fmt.Sprintf("Remove network %s?", n.Name)
		if len(n.Containers) > 0 {
			body += "\nAttached: " + strings.Join(n.Containers, ", ") + " (disconnect them first)."
		}
		id, name := n.ID, n.Name
		return m, confirm("Remove network", body, true, op("net:"+name, "removing", name, "remove network "+name, "", time.Minute, func(ctx context.Context) error {
			return client.RemoveNetwork(ctx, id)
		}))
	case "i", "enter":
		return m, InspectCmd("network", n.ID, n.Name)
	}
	return m, nil
}

func (m NetworkModel) View() string {
	title := titleLine(fmt.Sprintf("Networks (%d)", len(m.all)), m.filter.view(), m.width)
	help := helpLine(m.width, "d remove · P prune unused · i inspect · / filter · ↑↓ navigate")

	if len(m.visible) == 0 {
		return title + "\n" + styles.SubtitleStyle.Render("\n  No networks match.") + "\n" + help
	}
	cur := m.list.cursor
	if cur >= len(m.visible) {
		cur = len(m.visible) - 1
	}

	contentHeight := m.height - 2
	if contentHeight < 6 {
		contentHeight = 6
	}

	if m.width < 70 {
		sel := m.visible[cur]
		nav := styles.SubtitleStyle.Render(fmt.Sprintf("  [%d/%d] %s", cur+1, len(m.visible), sel.Name))
		card := m.renderDetails(sel, m.width-4, contentHeight-1)
		return title + "\n" + nav + "\n" + lipgloss.NewStyle().MarginLeft(2).Render(card) + "\n" + help
	}

	listWidth := 32
	if m.width >= 120 {
		listWidth = 40
	}
	detailsWidth := m.width - listWidth - 6
	left := m.renderList(listWidth, contentHeight)
	right := m.renderDetails(m.visible[cur], detailsWidth, contentHeight)
	panes := lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
	return title + "\n" + lipgloss.NewStyle().MarginLeft(2).Render(panes) + "\n" + help
}

func (m NetworkModel) renderList(width, height int) string {
	inner := width - 2
	rowsAvail := height - 3
	if rowsAvail < 1 {
		rowsAvail = 1
	}
	l := m.list
	start, end := l.window(len(m.visible), rowsAvail)

	rows := []string{lipgloss.NewStyle().Bold(true).Foreground(styles.ColorCyan).Render(fit(fmt.Sprintf(" NETWORKS (%d)", len(m.visible)), inner))}
	for i := start; i < end; i++ {
		n := m.visible[i]
		count := ""
		if c := len(n.Endpoints); c > 0 {
			count = fmt.Sprintf(" [%d]", c)
		}
		badge := fmt.Sprintf(" (%s)", n.Driver)
		name := truncate(n.Name, inner-2-len(badge)-len(count))
		if i == l.cursor {
			rows = append(rows, styles.KeyStyle.Render("▸ ")+styles.RowSelectedStyle.Render(fit(name+badge+count, inner-2)))
		} else {
			line := "  " + name + styles.DimStyle.Render(badge) + lipgloss.NewStyle().Foreground(styles.ColorPurple).Render(count)
			rows = append(rows, fit(line, inner))
		}
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.ColorBorder).
		Width(width - 2).
		Height(height - 2).
		Render(strings.Join(rows, "\n"))
}

func (m NetworkModel) renderDetails(n docker.NetworkInfo, width, height int) string {
	inner := width - 4
	if inner < 20 {
		inner = 20
	}
	label := func(s string) string { return styles.DimStyle.Render(fmt.Sprintf("%-10s", s)) }
	orDash := func(s string) string {
		if s == "" || s == "invalid IP" || s == "invalid Prefix" {
			return "—"
		}
		return s
	}

	var lines []string
	head := styles.KeyStyle.Render(n.Name) + "  " +
		lipgloss.NewStyle().Foreground(styles.ColorPurple).Bold(true).Render("["+n.Driver+"]")
	if n.Scope != "" {
		head += "  " + styles.DimStyle.Render("scope:"+n.Scope)
	}
	if n.Internal {
		head += "  " + styles.WarnStyle.Render("internal")
	}
	div := lipgloss.NewStyle().Foreground(styles.ColorBorder).Render(strings.Repeat("─", inner))
	lines = append(lines, head, div,
		label("ID:")+n.ID,
		label("Subnet:")+orDash(n.Subnet),
		label("Gateway:")+orDash(n.Gateway),
	)
	if n.Project != "" {
		lines = append(lines, label("Project:")+n.Project)
	}
	lines = append(lines, div, styles.SuccessStyle.Render(fmt.Sprintf("Connected Containers (%d)", len(n.Endpoints))))

	if len(n.Endpoints) == 0 {
		lines = append(lines, styles.DimStyle.Italic(true).Render("  (no containers attached)"))
	} else {
		nameW, ipW := 24, 20
		if inner < 64 {
			nameW, ipW = 16, 16
		}
		lines = append(lines, "  "+styles.TableHeaderStyle.Padding(0).Render(fit("CONTAINER", nameW)+" "+fit("IPv4", ipW)+" MAC"))
		for _, ep := range n.Endpoints {
			lines = append(lines, "  "+
				lipgloss.NewStyle().Foreground(styles.ColorBlue).Bold(true).Render(fit(ep.Name, nameW))+" "+
				styles.SuccessStyle.UnsetBold().Render(fit(orDash(ep.IPv4), ipW))+" "+
				styles.DimStyle.Render(orDash(ep.Mac)))
		}
	}
	maxLines := height - 2
	if maxLines > 0 && len(lines) > maxLines {
		lines = append(lines[:maxLines-1], styles.DimStyle.Render(fmt.Sprintf("  … %d more", len(lines)-maxLines+1)))
	}
	for i := range lines {
		lines[i] = truncate(lines[i], inner)
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.ColorCyan).
		Padding(0, 1).
		Width(width - 2).
		Height(height - 2).
		Render(strings.Join(lines, "\n"))
}
