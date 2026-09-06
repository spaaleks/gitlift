package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type fieldKind int

const (
	fieldText fieldKind = iota
	fieldSecret
	fieldToggle
	fieldChoice
	fieldHeading
)

type field struct {
	id       string
	label    string
	note     string
	diff     string
	kind     fieldKind
	text     string
	on       bool
	choices  []string
	choice   int
	required bool
}

func (f field) focusable() bool { return f.kind != fieldHeading }

func (f field) value() string {
	switch f.kind {
	case fieldToggle:
		if f.on {
			return "yes"
		}
		return "no"
	case fieldChoice:
		if f.choice >= 0 && f.choice < len(f.choices) {
			return f.choices[f.choice]
		}
		return ""
	default:
		return f.text
	}
}

func (f field) display() string {
	if f.kind == fieldSecret {
		if f.text == "" {
			return ""
		}
		return strings.Repeat("•", min(len(f.text), 24))
	}
	return f.value()
}

type form struct {
	title  string
	intro  string
	fields []field
	cursor int
	top    int
	err    string
}

func newForm(title, intro string, fields []field) form {
	f := form{title: title, intro: intro, fields: fields}
	f.cursor = f.next(-1, 1)
	return f
}

func (f *form) next(from, delta int) int {
	index := from + delta
	for index >= 0 && index < len(f.fields) {
		if f.fields[index].focusable() {
			return index
		}
		index += delta
	}
	return from
}

func (f *form) get(id string) string {
	for _, entry := range f.fields {
		if entry.id == id {
			return entry.value()
		}
	}
	return ""
}

func (f *form) getBool(id string) bool {
	for _, entry := range f.fields {
		if entry.id == id {
			return entry.on
		}
	}
	return false
}

func (f *form) has(id string) bool {
	for _, entry := range f.fields {
		if entry.id == id {
			return true
		}
	}
	return false
}

func (f *form) validate() bool {
	for i, entry := range f.fields {
		if entry.required && strings.TrimSpace(entry.text) == "" {
			f.cursor = i
			f.err = entry.label + " is required"
			return false
		}
	}
	f.err = ""
	return true
}

func (f *form) update(msg tea.KeyMsg) event {
	if f.cursor >= len(f.fields) {
		return eventNone
	}
	current := &f.fields[f.cursor]

	switch msg.String() {
	case "esc":
		return eventBack
	case "ctrl+s":
		if f.validate() {
			return eventSubmit
		}
		return eventNone
	case "enter":
		if f.cursor == f.next(f.cursor, 1) {
			if f.validate() {
				return eventSubmit
			}
			return eventNone
		}
		f.cursor = f.next(f.cursor, 1)
	case "up", "shift+tab":
		f.cursor = f.next(f.cursor, -1)
	case "down", "tab":
		f.cursor = f.next(f.cursor, 1)
	case "left":
		switch current.kind {
		case fieldChoice:
			if len(current.choices) > 0 {
				current.choice = (current.choice - 1 + len(current.choices)) % len(current.choices)
			}
		case fieldToggle:
			current.on = false
		}
	case "right":
		switch current.kind {
		case fieldChoice:
			if len(current.choices) > 0 {
				current.choice = (current.choice + 1) % len(current.choices)
			}
		case fieldToggle:
			current.on = true
		}
	case " ":
		switch current.kind {
		case fieldToggle:
			current.on = !current.on
		case fieldChoice:
			if len(current.choices) > 0 {
				current.choice = (current.choice + 1) % len(current.choices)
			}
		default:
			current.text += " "
		}
	case "backspace":
		if current.kind == fieldText || current.kind == fieldSecret {
			runes := []rune(current.text)
			if len(runes) > 0 {
				current.text = string(runes[:len(runes)-1])
			}
		}
	case "ctrl+u":
		if current.kind == fieldText || current.kind == fieldSecret {
			current.text = ""
		}
	default:
		if msg.Type == tea.KeyRunes && !msg.Alt {
			if current.kind == fieldText || current.kind == fieldSecret {
				current.text += string(msg.Runes)
			}
		}
	}

	f.err = ""
	return eventNone
}

func (f *form) view(width, rows int) string {
	var b strings.Builder

	width = max(width, 10)

	b.WriteString(sectionStyle.Render(truncate(f.title, width)) + "\n")
	if f.intro != "" {
		b.WriteString(dimStyle.Render(truncate(f.intro, width)) + "\n")
	}
	b.WriteString("\n")

	rows = max(rows-2, 1)
	if f.cursor < f.top {
		f.top = f.cursor
	}
	if f.cursor >= f.top+rows {
		f.top = f.cursor - rows + 1
	}
	f.top = clamp(f.top, 0, max(len(f.fields)-rows, 0))

	labelWidth := 0
	for _, entry := range f.fields {
		if entry.focusable() && len(entry.label) > labelWidth {
			labelWidth = len(entry.label)
		}
	}
	labelWidth = min(labelWidth, 24)

	end := min(f.top+rows, len(f.fields))
	for i := f.top; i < end; i++ {
		entry := f.fields[i]

		if entry.kind == fieldHeading {
			if i > f.top {
				b.WriteByte('\n')
			}
			b.WriteString("  " + dimStyle.Render(strings.ToUpper(entry.label)) + "\n")
			continue
		}

		marker := "   "
		if i == f.cursor {
			marker = cursorStyle.Render(" > ")
		}

		label := entry.label
		if len(label) < labelWidth {
			label += strings.Repeat(" ", labelWidth-len(label))
		}

		value := entry.display()
		switch {
		case entry.kind == fieldChoice:
			value = "‹ " + value + " ›"
		case entry.kind == fieldToggle:
			if entry.on {
				value = "[x] yes"
			} else {
				value = "[ ] no"
			}
		case i == f.cursor:
			value += "_"
		case value == "":
			value = dimStyle.Render("(empty)")
		}

		valueWidth := max(width-labelWidth-8, 8)
		if entry.diff != "" {
			valueWidth = max(valueWidth-len(entry.diff)-4, 8)
		}

		if i == f.cursor {
			b.WriteString(marker + fieldStyle.Render(label) + "  " + valueStyle.Render(truncate(value, valueWidth)))
		} else {
			b.WriteString(marker + dimStyle.Render(label) + "  " + itemStyle.Render(truncate(value, valueWidth)))
		}
		if entry.diff != "" {
			b.WriteString(dimStyle.Render("   " + entry.diff))
		}

		if i == f.cursor && entry.note != "" {
			b.WriteString("\n" + strings.Repeat(" ", labelWidth+5) + dimStyle.Render(entry.note))
		}
		b.WriteByte('\n')
	}

	if f.err != "" {
		b.WriteString("\n  " + errStyle.Render(f.err))
	}

	return b.String()
}

func (f *form) keys() string {
	return "↑↓ move · type to edit · ←→/space toggle · enter next · ctrl+s continue · esc back"
}
