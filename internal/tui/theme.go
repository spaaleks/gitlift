package tui

import "github.com/charmbracelet/lipgloss"

var (
	accent = lipgloss.Color("#ec483b")
	text   = lipgloss.Color("#e0e0e0")
	muted  = lipgloss.Color("#9b9b9b")
	good   = lipgloss.Color("#49e150")
	warn   = lipgloss.Color("#e5c07b")
	alert  = lipgloss.Color("#ff5f5f")

	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(accent)
	sectionStyle = lipgloss.NewStyle().Bold(true).Foreground(text)
	itemStyle    = lipgloss.NewStyle().Foreground(text)
	cursorStyle  = lipgloss.NewStyle().Bold(true).Foreground(accent)
	dimStyle     = lipgloss.NewStyle().Foreground(muted)
	okStyle      = lipgloss.NewStyle().Foreground(good)
	warnStyle    = lipgloss.NewStyle().Foreground(warn)
	errStyle     = lipgloss.NewStyle().Bold(true).Foreground(alert)
	fieldStyle   = lipgloss.NewStyle().Foreground(text)
	valueStyle   = lipgloss.NewStyle().Foreground(accent)
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var logoLines = []string{
	`╔═╗╦╔╦╗╦  ╦╔═╗╔╦╗`,
	`║ ╦║ ║ ║  ║╠╣  ║ `,
	`╚═╝╩ ╩ ╩═╝╩╚   ╩ `,
}

func logoWidth() int {
	width := 0
	for _, line := range logoLines {
		width = max(width, len([]rune(line)))
	}
	return width
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func clamp(v, low, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}

func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}
