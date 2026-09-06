package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/spaaleks/gitlift/internal/fuzzy"
)

type listItem struct {
	title string
	desc  string
	ref   int
}

type event int

const (
	eventNone event = iota
	eventSubmit
	eventBack
)

type list struct {
	title    string
	items    []listItem
	order    []int
	query    string
	cursor   int
	top      int
	multi    bool
	picked   map[int]bool
	filter   bool
	emptyMsg string
}

func newList(title string, items []listItem, multi bool) list {
	l := list{
		title:  title,
		items:  items,
		multi:  multi,
		filter: true,
		picked: map[int]bool{},
	}
	l.refilter()
	return l
}

func (l *list) refilter() {
	if l.query == "" {
		l.order = l.order[:0]
		for i := range l.items {
			l.order = append(l.order, i)
		}
	} else {
		haystack := make([]string, len(l.items))
		for i, item := range l.items {
			haystack[i] = item.title + " " + item.desc
		}
		l.order = fuzzy.Filter(l.query, haystack)
	}
	l.cursor = 0
	l.top = 0
}

func (l *list) selected() (listItem, bool) {
	if l.cursor < 0 || l.cursor >= len(l.order) {
		return listItem{}, false
	}
	return l.items[l.order[l.cursor]], true
}

func (l *list) results() []int {
	if !l.multi {
		if item, ok := l.selected(); ok {
			return []int{item.ref}
		}
		return nil
	}

	if len(l.picked) == 0 {
		if item, ok := l.selected(); ok {
			return []int{item.ref}
		}
		return nil
	}

	var out []int
	for i, item := range l.items {
		if l.picked[i] {
			out = append(out, item.ref)
		}
	}
	return out
}

func (l *list) move(delta int) {
	if len(l.order) == 0 {
		return
	}
	l.cursor = clamp(l.cursor+delta, 0, len(l.order)-1)
}

func (l *list) update(msg tea.KeyMsg, rows int) event {
	switch msg.String() {
	case "esc":
		return eventBack
	case "enter":
		if len(l.order) == 0 && !l.multi {
			return eventNone
		}
		return eventSubmit
	case "up", "ctrl+p":
		l.move(-1)
	case "down", "ctrl+n":
		l.move(1)
	case "pgup":
		l.move(-max(rows-1, 1))
	case "pgdown":
		l.move(max(rows-1, 1))
	case "home":
		l.cursor = 0
	case "end":
		l.cursor = max(len(l.order)-1, 0)
	case "tab":
		if l.multi && len(l.order) > 0 {
			index := l.order[l.cursor]
			if l.picked[index] {
				delete(l.picked, index)
			} else {
				l.picked[index] = true
			}
			l.move(1)
		}
	case "ctrl+a":
		if l.multi {
			all := true
			for _, index := range l.order {
				if !l.picked[index] {
					all = false
					break
				}
			}
			for _, index := range l.order {
				if all {
					delete(l.picked, index)
				} else {
					l.picked[index] = true
				}
			}
		}
	case "backspace":
		if l.filter && l.query != "" {
			l.query = l.query[:len(l.query)-1]
			l.refilter()
		}
	case "ctrl+u":
		if l.filter && l.query != "" {
			l.query = ""
			l.refilter()
		}
	default:
		if l.filter && msg.Type == tea.KeyRunes && !msg.Alt {
			l.query += string(msg.Runes)
			l.refilter()
		} else if l.filter && msg.Type == tea.KeySpace {
			l.query += " "
			l.refilter()
		}
	}

	return eventNone
}

func (l *list) view(width, rows int) string {
	var b strings.Builder

	width = max(width, 10)

	header := sectionStyle.Render(l.title)
	switch {
	case l.multi:
		header += "  " + dimStyle.Render(fmt.Sprintf("%d picked · %d/%d", len(l.picked), len(l.order), len(l.items)))
	case l.filter:
		header += "  " + dimStyle.Render(fmt.Sprintf("%d/%d", len(l.order), len(l.items)))
	}
	b.WriteString(header + "\n")

	if l.filter {
		prompt := cursorStyle.Render(">") + " "
		if l.query == "" {
			b.WriteString(prompt + dimStyle.Render("type to filter") + "\n\n")
		} else {
			b.WriteString(prompt + itemStyle.Render(l.query) + "\n\n")
		}
	} else {
		b.WriteString("\n")
	}

	if len(l.order) == 0 {
		message := l.emptyMsg
		if message == "" {
			message = "nothing matches"
		}
		b.WriteString(dimStyle.Render("  " + message))
		return b.String()
	}

	rows = max(rows, 1)
	if l.cursor < l.top {
		l.top = l.cursor
	}
	if l.cursor >= l.top+rows {
		l.top = l.cursor - rows + 1
	}
	l.top = clamp(l.top, 0, max(len(l.order)-rows, 0))

	end := min(l.top+rows, len(l.order))
	for i := l.top; i < end; i++ {
		index := l.order[i]
		item := l.items[index]

		mark := ""
		if l.multi {
			mark = "[ ] "
			if l.picked[index] {
				mark = "[x] "
			}
		}

		line := mark + item.title
		room := width - 6

		if i == l.cursor {
			title := truncate(line, room)
			room -= len([]rune(title))
			b.WriteString(cursorStyle.Render("  > " + title))
			if item.desc != "" && room > 6 {
				b.WriteString(dimStyle.Render(truncate("  "+item.desc, room)))
			}
		} else {
			b.WriteString("    " + itemStyle.Render(truncate(line, room)))
		}
		b.WriteByte('\n')
	}

	if end < len(l.order) || l.top > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("    showing %d-%d of %d", l.top+1, end, len(l.order))))
	}

	return b.String()
}

func (l *list) keys() string {
	switch {
	case l.multi:
		return "type filter · tab pick · ctrl+a all · enter confirm · esc back"
	case l.filter:
		return "type filter · ↑↓ move · enter select · esc back"
	default:
		return "↑↓ move · enter select · esc back"
	}
}
