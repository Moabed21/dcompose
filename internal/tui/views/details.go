package views

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"dcompose/internal/desktop"
	"dcompose/internal/docker"
	"dcompose/internal/tui/styles"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// DetailsCmd loads a container's details and opens them in the viewer.
func DetailsCmd(client *docker.Client, id, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		d, err := client.ContainerDetails(ctx, id)
		if err != nil {
			return FlashMsg{Text: "details " + name, Err: err}
		}
		return ViewerRequestMsg{
			Title:   "details · " + d.Name + "   (I for raw JSON)",
			Content: renderDetails(d, client.BrowserHost(), time.Now()),
		}
	}
}

// renderDetails formats container details for the viewer.
func renderDetails(d *docker.ContainerDetails, browserHost string, now time.Time) string {
	var b strings.Builder
	label := func(s string) string { return styles.DimStyle.Render(fmt.Sprintf("%-10s", s)) }
	section := func(s string) { b.WriteString("\n" + styles.KeyStyle.Render(s) + "\n") }
	line := func(k, v string) {
		if v != "" {
			b.WriteString(label(k) + v + "\n")
		}
	}
	ago := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Local().Format("2006-01-02 15:04:05") + styles.DimStyle.Render("  ("+since(now, t)+")")
	}

	b.WriteString(lipgloss.NewStyle().Bold(true).Render(d.Name) + "  " + styles.StatusBadge(d.State) + healthBadge(d.Health) + "\n\n")

	line("ID", d.ID)
	img := d.Image
	if d.ImageID != "" && !strings.HasPrefix(d.ImageID, img) {
		img += styles.DimStyle.Render("  (" + d.ImageID + ")")
	}
	line("Image", img)
	line("Command", d.Command)
	line("Created", ago(d.Created))
	switch d.State {
	case "running", "paused", "restarting":
		line("Started", ago(d.StartedAt))
	default:
		if !d.FinishedAt.IsZero() {
			exit := fmt.Sprintf("exit code %d", d.ExitCode)
			if d.ExitCode != 0 {
				exit = styles.ErrorStyle.Render(exit + exitHint(d.ExitCode, d.OOMKilled))
			}
			line("Stopped", ago(d.FinishedAt)+"  "+exit)
		}
	}
	if d.Error != "" {
		line("Error", styles.ErrorStyle.Render(d.Error))
	}
	restart := "no restart policy"
	if d.RestartPolicy != "" {
		restart = "policy " + d.RestartPolicy
	}
	restart += fmt.Sprintf(" · restarted %d times", d.RestartCount)
	if d.RestartCount > 0 {
		restart = styles.WarnStyle.Render(restart)
	}
	line("Restart", restart)
	var limits []string
	if d.MemoryLimit > 0 {
		limits = append(limits, "memory "+formatBytes(uint64(d.MemoryLimit)))
	}
	if d.CPULimit > 0 {
		limits = append(limits, "cpus "+strconv.FormatFloat(d.CPULimit, 'f', -1, 64))
	}
	line("Limits", strings.Join(limits, " · "))
	if d.Project != "" {
		line("Compose", fmt.Sprintf("project %s · service %s", d.Project, d.Service))
		line("", styles.DimStyle.Render(d.ConfigFiles))
	}

	if d.Health != "" {
		section("Health check")
		status := d.Health
		if d.FailingStreak > 0 {
			status += fmt.Sprintf(" · %d failed in a row", d.FailingStreak)
		}
		b.WriteString("  " + status + "\n")
		for i := len(d.HealthLog) - 1; i >= 0; i-- { // newest first
			h := d.HealthLog[i]
			res := styles.SuccessStyle.Render("ok")
			if h.ExitCode != 0 {
				res = styles.ErrorStyle.Render(fmt.Sprintf("exit %d", h.ExitCode))
			}
			b.WriteString(fmt.Sprintf("  %s  %s\n", styles.DimStyle.Render(h.Start.Local().Format("15:04:05")), res))
			for _, l := range strings.Split(h.Output, "\n") {
				if l = strings.TrimRight(sanitize(l), " "); l != "" {
					b.WriteString("      " + l + "\n")
				}
			}
		}
	}

	section("Ports")
	if len(d.Ports) == 0 {
		b.WriteString(styles.DimStyle.Render("  none published") + "\n")
	}
	for _, p := range d.Ports {
		host := p.HostIP
		if host == "" {
			host = "*"
		}
		row := fmt.Sprintf("  %s:%d → %d/%s", host, p.HostPort, p.ContainerPort, p.Proto)
		if p.Proto == "tcp" {
			row = fmt.Sprintf("%-34s %s", row, styles.DimStyle.Render(p.URL(browserHost)))
		}
		b.WriteString(row + "\n")
	}

	section("Networks")
	if len(d.Networks) == 0 {
		b.WriteString(styles.DimStyle.Render("  none") + "\n")
	}
	for _, n := range d.Networks {
		row := fmt.Sprintf("  %-28s %-16s", n.Name, n.IP)
		if len(n.Aliases) > 0 {
			row += styles.DimStyle.Render(" aliases: " + strings.Join(n.Aliases, ", "))
		}
		b.WriteString(strings.TrimRight(row, " ") + "\n")
	}

	section("Mounts")
	if len(d.Mounts) == 0 {
		b.WriteString(styles.DimStyle.Render("  none") + "\n")
	}
	for _, m := range d.Mounts {
		mode := "rw"
		if m.ReadOnly {
			mode = "ro"
		}
		b.WriteString(fmt.Sprintf("  %-7s %s → %s %s\n", m.Type, m.Source, m.Destination, styles.DimStyle.Render("("+mode+")")))
	}

	section(fmt.Sprintf("Environment (%d)", len(d.Env)))
	for _, e := range d.Env {
		k, v, _ := strings.Cut(e, "=")
		b.WriteString("  " + styles.DimStyle.Render(k+"=") + sanitize(v) + "\n")
	}
	return b.String()
}

func healthBadge(h string) string {
	switch h {
	case "healthy":
		return styles.SuccessStyle.Render("  ♥ healthy")
	case "unhealthy":
		return styles.ErrorStyle.Render("  ✗ unhealthy")
	case "starting":
		return styles.WarnStyle.Render("  … health starting")
	}
	return ""
}

// exitHint explains common exit codes.
func exitHint(code int, oom bool) string {
	switch {
	case oom:
		return " (killed: out of memory)"
	case code == 137:
		return " (killed, SIGKILL)"
	case code == 143:
		return " (terminated, SIGTERM)"
	case code == 126:
		return " (command not executable)"
	case code == 127:
		return " (command not found)"
	}
	return ""
}

func since(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// openPortCmd opens a container's published port in the browser, asking
// which one when there are several.
func openPortCmd(client *docker.Client, ctr *docker.ContainerInfo) tea.Cmd {
	var tcp []docker.PortMapping
	for _, p := range ctr.Published {
		if p.Proto == "tcp" {
			tcp = append(tcp, p)
		}
	}
	if len(tcp) == 0 {
		return emit(FlashMsg{Text: "open " + ctr.Name, Err: fmt.Errorf("no TCP ports are published on the host")})
	}
	host := client.BrowserHost()
	if len(tcp) == 1 {
		return openURLCmd(tcp[0].URL(host))
	}
	var ports []string
	for _, p := range tcp {
		ports = append(ports, strconv.Itoa(int(p.HostPort)))
	}
	return emit(PromptRequestMsg{
		Title:       "Open in browser (published: " + strings.Join(ports, ", ") + ")",
		Placeholder: "URL",
		Initial:     tcp[0].URL(host),
		OnSubmit: func(s string) tea.Cmd {
			if s = strings.TrimSpace(s); s == "" {
				return nil
			}
			if !strings.Contains(s, "://") {
				s = "http://" + s
			}
			return openURLCmd(s)
		},
	})
}

func openURLCmd(u string) tea.Cmd {
	return func() tea.Msg {
		if err := desktop.OpenURL(u); err != nil {
			return FlashMsg{Text: "open " + u, Err: err}
		}
		return FlashMsg{Text: "opened " + u}
	}
}
