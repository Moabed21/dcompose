package styles

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Color palette — clean dark terminal colors using ANSI-friendly values.
// No explicit backgrounds set — inherits from terminal theme.
var (
	// Text colors
	ColorText     = lipgloss.Color("#c9d1d9")
	ColorTextDim  = lipgloss.Color("#8b949e")
	ColorTextMute = lipgloss.Color("#484f58")

	// Accent colors
	ColorCyan    = lipgloss.Color("6")  // ANSI cyan
	ColorBlue    = lipgloss.Color("4")  // ANSI blue
	ColorGreen   = lipgloss.Color("2")  // ANSI green
	ColorRed     = lipgloss.Color("1")  // ANSI red
	ColorYellow  = lipgloss.Color("3")  // ANSI yellow
	ColorMagenta = lipgloss.Color("5")  // ANSI magenta
	ColorWhite   = lipgloss.Color("7")  // ANSI white
	ColorPurple  = lipgloss.Color("13") // bright magenta
	ColorOrange  = lipgloss.Color("3")  // yellow as fallback

	// Borders
	ColorBorder    = lipgloss.Color("#444444")
	ColorBorderLit = lipgloss.Color("6")

	// Status colors
	ColorRunning    = ColorGreen
	ColorStopped    = ColorRed
	ColorRestarting = ColorYellow
	ColorPaused     = ColorYellow
)

// Styles — reusable Lipgloss style definitions (no explicit backgrounds).
var (
	// Tab bar styles
	TabActive = lipgloss.NewStyle().
			Foreground(ColorCyan).
			Bold(true).
			Padding(0, 2).
			Underline(true)

	TabInactive = lipgloss.NewStyle().
			Foreground(ColorTextDim).
			Padding(0, 2)

	// Header
	LogoStyle = lipgloss.NewStyle().
			Foreground(ColorCyan).
			Bold(true)

	ProjectNameStyle = lipgloss.NewStyle().
				Foreground(ColorPurple).
				Bold(true)

	// Status bar
	StatusBarStyle = lipgloss.NewStyle().
			Foreground(ColorTextDim)

	StatusBarKeyStyle = lipgloss.NewStyle().
				Foreground(ColorCyan).
				Bold(true)

	// Table styles
	TableHeaderStyle = lipgloss.NewStyle().
				Foreground(ColorBlue).
				Bold(true).
				Padding(0, 1)

	TableCellStyle = lipgloss.NewStyle().
			Foreground(ColorText).
			Padding(0, 1)

	TableSelectedStyle = lipgloss.NewStyle().
				Foreground(ColorCyan).
				Bold(true).
				Padding(0, 1).
				Reverse(true)

	// Panel
	PanelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorBorder).
			Padding(1, 2)

	PanelActiveStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(ColorCyan).
				Padding(1, 2)

	// Status badges — using reverse video instead of explicit bg
	BadgeRunning = lipgloss.NewStyle().
			Foreground(ColorGreen).
			Bold(true)

	BadgeStopped = lipgloss.NewStyle().
			Foreground(ColorRed).
			Bold(true)

	BadgeRestarting = lipgloss.NewStyle().
			Foreground(ColorYellow).
			Bold(true)

	BadgePaused = lipgloss.NewStyle().
			Foreground(ColorYellow).
			Bold(true)

	// Misc
	TitleStyle = lipgloss.NewStyle().
			Foreground(ColorCyan).
			Bold(true).
			MarginBottom(1)

	SubtitleStyle = lipgloss.NewStyle().
			Foreground(ColorTextDim).
			Italic(true)

	ErrorStyle = lipgloss.NewStyle().
			Foreground(ColorRed).
			Bold(true)

	SuccessStyle = lipgloss.NewStyle().
			Foreground(ColorGreen).
			Bold(true)

	HelpStyle = lipgloss.NewStyle().
			Foreground(ColorTextMute)

	// Selected table row (whole-line highlight)
	RowSelectedStyle = lipgloss.NewStyle().
				Foreground(ColorCyan).
				Bold(true).
				Reverse(true)

	DimStyle = lipgloss.NewStyle().Foreground(ColorTextDim)

	WarnStyle = lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)

	KeyStyle = lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)

	// Overlay frame
	OverlayStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorCyan).
			Padding(0, 1)

	// Confirm dialog
	DialogStyle = lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(ColorYellow).
			Padding(1, 3)

	DialogButtonActive = lipgloss.NewStyle().
				Foreground(ColorCyan).
				Bold(true).
				Reverse(true).
				Padding(0, 2)

	DialogButtonInactive = lipgloss.NewStyle().
				Foreground(ColorTextDim).
				Padding(0, 2)

	// Network diagram
	NetworkBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorGreen).
			Padding(0, 1)

	ContainerBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(ColorBlue).
				Padding(0, 1)

	// Bar chart characters for stats
	BarFull  = "█"
	BarHigh  = "▓"
	BarMed   = "▒"
	BarLow   = "░"
	BarEmpty = "·"
)

// StatusBadge returns a styled status badge based on container state.
func StatusBadge(state string) string {
	switch state {
	case "running":
		return BadgeRunning.Render("● RUN")
	case "exited":
		return BadgeStopped.Render("■ EXIT")
	case "restarting":
		return BadgeRestarting.Render("↻ RESTART")
	case "paused":
		return BadgePaused.Render("⏸ PAUSE")
	case "created":
		return BadgePaused.Render("○ CREATED")
	case "dead":
		return BadgeStopped.Render("✕ DEAD")
	case "removing":
		return BadgeRestarting.Render("… REMOVING")
	case "down", "":
		return lipgloss.NewStyle().Foreground(ColorTextMute).Render("○ DOWN")
	default:
		return lipgloss.NewStyle().Foreground(ColorTextDim).Render(state)
	}
}

// RenderBar renders a percentage bar with block characters.
func RenderBar(percent float64, width int) string {
	filled := int(percent / 100.0 * float64(width))
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}

	var color lipgloss.Color
	switch {
	case percent >= 90:
		color = ColorRed
	case percent >= 70:
		color = ColorYellow
	case percent >= 50:
		color = ColorYellow
	default:
		color = ColorGreen
	}

	bar := lipgloss.NewStyle().Foreground(color).Render(repeatStr(BarFull, filled))
	empty := lipgloss.NewStyle().Foreground(ColorTextMute).Render(repeatStr(BarEmpty, width-filled))
	return bar + empty
}

func repeatStr(s string, n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(s, n)
}
