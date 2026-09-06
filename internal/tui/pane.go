package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type pane struct {
	title    string
	body     string
	markdown bool
	offset   int

	cached      []string
	cachedWidth int
}

func newPane(title, body string) pane {
	return pane{title: title, body: body}
}

func newMarkdownPane(title, body string) pane {
	return pane{title: title, body: body, markdown: true}
}

func (p *pane) update(msg tea.KeyMsg, rows int) event {
	switch msg.String() {
	case "esc":
		return eventBack
	case "enter":
		return eventSubmit
	case "up", "k":
		p.offset--
	case "down", "j":
		p.offset++
	case "pgup", "b":
		p.offset -= rows
	case "pgdown", "f":
		p.offset += rows
	case "home", "g":
		p.offset = 0
	case "end", "G":
		p.offset = 1 << 20
	}
	return eventNone
}

func (p *pane) lines(width int) []string {
	if p.cached != nil && p.cachedWidth == width {
		return p.cached
	}

	if p.markdown {
		p.cached = strings.Split(renderMarkdown(p.body, width), "\n")
	} else {
		p.cached = strings.Split(lipgloss.NewStyle().Width(max(width, 20)).Render(p.body), "\n")
	}
	p.cachedWidth = width

	return p.cached
}

func (p *pane) view(width, rows int) string {
	lines := p.lines(width)
	rows = max(rows-2, 1)

	p.offset = clamp(p.offset, 0, max(len(lines)-rows, 0))
	end := min(p.offset+rows, len(lines))

	header := sectionStyle.Render(p.title)
	if len(lines) > rows {
		header += dimStyle.Render(fmt.Sprintf("   (%d-%d of %d)", p.offset+1, end, len(lines)))
	}

	return header + "\n\n" + strings.Join(lines[p.offset:end], "\n")
}
