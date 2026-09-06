package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type span struct {
	text  string
	style lipgloss.Style
}

var (
	inlineCode = regexp.MustCompile("`([^`]+)`")
	boldText   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	italicText = regexp.MustCompile(`\*([^*]+)\*`)
	linkText   = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	listBullet = regexp.MustCompile(`^(\s*)[-*]\s+`)
	listNumber = regexp.MustCompile(`^(\s*)(\d+)\.\s+`)
	tableRule  = regexp.MustCompile(`^\|[\s:|-]+\|$`)
)

func renderMarkdown(src string, width int) string {
	width = max(width, 24)

	var out []string
	lines := strings.Split(src, "\n")

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		if strings.HasPrefix(line, "```") {
			block, next := collectFence(lines, i)
			out = append(out, renderFence(block, width)...)
			i = next
			continue
		}

		if isTableRow(line) && i+1 < len(lines) && tableRule.MatchString(strings.TrimSpace(lines[i+1])) {
			rows, next := collectTable(lines, i)
			out = append(out, renderTable(rows, width)...)
			i = next
			continue
		}

		if heading, level := parseHeading(line); level > 0 {
			out = append(out, renderHeading(heading, level, width)...)
			continue
		}

		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}

		out = append(out, renderParagraph(line, width)...)
	}

	return strings.Join(trimBlanks(out), "\n")
}

func parseHeading(line string) (string, int) {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 4 || level >= len(line) || line[level] != ' ' {
		return "", 0
	}
	return strings.TrimSpace(line[level+1:]), level
}

func renderHeading(text string, level, width int) []string {
	switch level {
	case 1:
		return []string{titleStyle.Render(truncate(strings.ToUpper(text), width))}
	case 2:
		bar := dimStyle.Render(strings.Repeat("─", max(min(width, 60), 4)))
		return []string{sectionStyle.Render(truncate(text, width)), bar}
	default:
		return []string{sectionStyle.Render(truncate(text, width))}
	}
}

func collectFence(lines []string, start int) ([]string, int) {
	var block []string
	i := start + 1
	for ; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "```") {
			break
		}
		block = append(block, lines[i])
	}
	return block, i
}

func renderFence(block []string, width int) []string {
	out := make([]string, 0, len(block)+2)
	out = append(out, "")
	for _, line := range block {
		out = append(out, dimStyle.Render("  │ ")+valueStyle.Render(truncate(line, max(width-4, 8))))
	}
	return append(out, "")
}

func isTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|")
}

func splitRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")

	parts := strings.Split(trimmed, "|")
	for i, part := range parts {
		parts[i] = strings.TrimSpace(part)
	}
	return parts
}

func collectTable(lines []string, start int) ([][]string, int) {
	rows := [][]string{splitRow(lines[start])}

	i := start + 2
	for ; i < len(lines) && isTableRow(lines[i]); i++ {
		rows = append(rows, splitRow(lines[i]))
	}
	return rows, i - 1
}

func renderTable(rows [][]string, width int) []string {
	if len(rows) == 0 {
		return nil
	}

	columns := 0
	for _, row := range rows {
		columns = max(columns, len(row))
	}

	widths := make([]int, columns)
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], visibleLen(stripInline(cell)))
		}
	}

	const gap = 2
	budget := width - 2 - gap*(columns-1)
	for total(widths) > budget {
		widest := 0
		for i, w := range widths {
			if w > widths[widest] {
				widest = i
			}
		}
		if widths[widest] <= 6 {
			break
		}
		widths[widest]--
	}

	out := []string{""}
	for index, row := range rows {
		var cells []string
		for i := 0; i < columns; i++ {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}

			plain := truncate(stripInline(cell), widths[i])
			padded := plain + strings.Repeat(" ", max(widths[i]-visibleLen(plain), 0))

			if index == 0 {
				cells = append(cells, sectionStyle.Render(padded))
			} else {
				cells = append(cells, itemStyle.Render(padded))
			}
		}
		out = append(out, "  "+strings.Join(cells, strings.Repeat(" ", gap)))

		if index == 0 {
			var rule []string
			for i := 0; i < columns; i++ {
				rule = append(rule, strings.Repeat("─", widths[i]))
			}
			out = append(out, "  "+dimStyle.Render(strings.Join(rule, strings.Repeat(" ", gap))))
		}
	}

	return append(out, "")
}

func total(widths []int) int {
	sum := 0
	for _, w := range widths {
		sum += w
	}
	return sum
}

func renderParagraph(line string, width int) []string {
	indent := ""
	body := line

	if m := listBullet.FindStringSubmatch(line); m != nil {
		indent = m[1] + "  "
		body = listBullet.ReplaceAllString(line, "")
		body = "• " + body
	} else if m := listNumber.FindStringSubmatch(line); m != nil {
		indent = m[1] + "   "
		body = listNumber.ReplaceAllString(line, "")
		body = m[2] + ". " + body
	} else if strings.HasPrefix(line, "  ") {
		indent = "  "
		body = strings.TrimLeft(line, " ")
	}

	return wrapSpans(parseInline(body), width, indent)
}

func parseInline(s string) []span {
	s = linkText.ReplaceAllString(s, "$1")

	type marker struct {
		loc   []int
		style lipgloss.Style
		body  string
	}

	masked := []byte(s)
	hide := func(from, to int) {
		for i := from; i < to && i < len(masked); i++ {
			masked[i] = '\x01'
		}
	}

	var marks []marker
	for _, m := range inlineCode.FindAllStringSubmatchIndex(s, -1) {
		marks = append(marks, marker{loc: m[0:2], style: valueStyle, body: s[m[2]:m[3]]})
		hide(m[0], m[1])
	}
	for _, m := range boldText.FindAllStringSubmatchIndex(string(masked), -1) {
		marks = append(marks, marker{loc: m[0:2], style: sectionStyle, body: s[m[2]:m[3]]})
		hide(m[0], m[1])
	}
	for _, m := range italicText.FindAllStringSubmatchIndex(string(masked), -1) {
		marks = append(marks, marker{loc: m[0:2], style: itemStyle.Italic(true), body: s[m[2]:m[3]]})
	}

	for i := 1; i < len(marks); i++ {
		for j := i; j > 0 && marks[j-1].loc[0] > marks[j].loc[0]; j-- {
			marks[j-1], marks[j] = marks[j], marks[j-1]
		}
	}

	var spans []span
	cursor := 0
	for _, m := range marks {
		if m.loc[0] < cursor {
			continue
		}
		if m.loc[0] > cursor {
			spans = append(spans, span{text: s[cursor:m.loc[0]], style: itemStyle})
		}
		spans = append(spans, span{text: m.body, style: m.style})
		cursor = m.loc[1]
	}
	if cursor < len(s) {
		spans = append(spans, span{text: s[cursor:], style: itemStyle})
	}

	return spans
}

func stripInline(s string) string {
	s = linkText.ReplaceAllString(s, "$1")
	s = inlineCode.ReplaceAllString(s, "$1")
	s = boldText.ReplaceAllString(s, "$1")
	return italicText.ReplaceAllString(s, "$1")
}

func wrapSpans(spans []span, width int, indent string) []string {
	room := max(width-len(indent), 8)

	var out []string
	var line strings.Builder
	used := 0

	flush := func() {
		if used > 0 {
			out = append(out, strings.TrimRight(indent+line.String(), " "))
			line.Reset()
			used = 0
		}
	}

	for _, s := range spans {
		for _, word := range splitKeepSpace(s.text) {
			if word == " " {
				if used > 0 {
					line.WriteString(" ")
					used++
				}
				continue
			}

			length := visibleLen(word)
			if used > 0 && used+length > room {
				flush()
			}
			for length > room {
				runes := []rune(word)
				line.WriteString(s.style.Render(string(runes[:room])))
				used = room
				flush()
				word = string(runes[room:])
				length = visibleLen(word)
			}
			line.WriteString(s.style.Render(word))
			used += length
		}
	}
	flush()

	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func splitKeepSpace(s string) []string {
	var out []string
	current := strings.Builder{}

	for _, r := range s {
		if r == ' ' {
			if current.Len() > 0 {
				out = append(out, current.String())
				current.Reset()
			}
			out = append(out, " ")
			continue
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}

func visibleLen(s string) int { return lipgloss.Width(s) }

func trimBlanks(lines []string) []string {
	var out []string
	blank := 0

	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return out
}
