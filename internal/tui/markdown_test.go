package tui

import (
	"strings"
	"testing"

	"github.com/spaaleks/gitlift"
)

func TestNoRawMarkdownSurvivesInHelp(t *testing.T) {
	rendered := renderMarkdown(gitlift.Help(), 86)

	for _, bad := range []string{"**", "```", "| --- |", "`"} {
		for _, line := range strings.Split(rendered, "\n") {
			if strings.Contains(line, bad) {
				t.Errorf("raw markdown %q survived rendering: %q", bad, line)
				break
			}
		}
	}
}

func TestHeadingsAreStyledNotHashed(t *testing.T) {
	out := renderMarkdown("# gitlift\n\n## Install\n\n### Token scopes\n", 60)

	if strings.Contains(out, "#") {
		t.Errorf("hash markers survived:\n%s", out)
	}
	if !strings.Contains(out, "GITLIFT") {
		t.Errorf("a level-one heading should be shouted:\n%s", out)
	}
	if !strings.Contains(out, "─") {
		t.Errorf("a level-two heading should get a rule:\n%s", out)
	}
}

func TestFencedBlocksKeepTheirLayout(t *testing.T) {
	out := renderMarkdown("text\n\n```yaml\nrepo:\n  visibility: \"private\"\n```\n\nmore\n", 60)

	if strings.Contains(out, "```") {
		t.Errorf("fence markers survived:\n%s", out)
	}
	if !strings.Contains(out, "│ repo:") {
		t.Errorf("code should be gutter-marked and unwrapped:\n%s", out)
	}
	if !strings.Contains(out, "│   visibility:") {
		t.Errorf("indentation inside a fence must survive:\n%s", out)
	}
}

func TestTablesAreAlignedIntoColumns(t *testing.T) {
	src := "| Key | GitHub | GitLab |\n| --- | --- | --- |\n| `issues` | ✓ | ✓ |\n| `duo` | | ✓ |\n"
	out := renderMarkdown(src, 60)

	if strings.Contains(out, "|") {
		t.Errorf("pipes survived:\n%s", out)
	}

	var rows []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "issues") || strings.Contains(line, "duo") || strings.Contains(line, "Key") {
			rows = append(rows, line)
		}
	}
	if len(rows) != 3 {
		t.Fatalf("want header plus two rows, got %v", rows)
	}

	gitlab := runeIndex(rows[0], "GitLab")
	for _, row := range rows[1:] {
		if idx := lastRuneIndex(row, "✓"); idx != gitlab {
			t.Errorf("GitLab column not aligned: %q has ✓ at column %d, header at %d", row, idx, gitlab)
		}
	}
}

func TestWideTablesAreSqueezedNotOverflowed(t *testing.T) {
	src := "| Key | Applies to | Values and notes |\n| --- | --- | --- |\n" +
		"| `branch_rules.rules[].unprotect_access` | GitLab | `no_one`, `developers`, `maintainers`, `admins` |\n"

	for _, width := range []int{40, 60, 86} {
		for _, line := range strings.Split(renderMarkdown(src, width), "\n") {
			if visibleLen(line) > width {
				t.Errorf("width %d: line is %d columns: %q", width, visibleLen(line), line)
			}
		}
	}
}

func TestParagraphsWrapToWidth(t *testing.T) {
	src := "A single binary that applies a YAML template to a git forge, with `inline code` and **bold words** in it."

	for _, width := range []int{30, 50, 80} {
		for _, line := range strings.Split(renderMarkdown(src, width), "\n") {
			if visibleLen(line) > width {
				t.Errorf("width %d: line is %d columns: %q", width, visibleLen(line), line)
			}
		}
	}
}

func TestListsBecomeBullets(t *testing.T) {
	out := renderMarkdown("- first\n- second\n\n1. one\n2. two\n", 40)

	if !strings.Contains(out, "• first") {
		t.Errorf("dash lists should become bullets:\n%s", out)
	}
	if !strings.Contains(out, "1. one") {
		t.Errorf("numbered lists should keep their numbers:\n%s", out)
	}
}

func TestPaneCachesPerWidth(t *testing.T) {
	p := newMarkdownPane("Help", gitlift.Help())

	first := p.lines(86)
	again := p.lines(86)
	if &first[0] != &again[0] {
		t.Error("the same width should reuse the cached render")
	}

	narrow := p.lines(50)
	if len(narrow) == len(first) {
		t.Error("a different width should re-render to a different line count")
	}
}

func runeIndex(haystack, needle string) int {
	at := strings.Index(haystack, needle)
	if at < 0 {
		return -1
	}
	return len([]rune(haystack[:at]))
}

func lastRuneIndex(haystack, needle string) int {
	at := strings.LastIndex(haystack, needle)
	if at < 0 {
		return -1
	}
	return len([]rune(haystack[:at]))
}

func TestBoldAndItalicOnTheSameLine(t *testing.T) {
	src := "**Bold lead.** Then a *quiet aside* and `code` after it."
	out := renderMarkdown(src, 100)

	if strings.Contains(out, "*") {
		t.Errorf("markers survived when bold and italic share a line: %q", out)
	}
	if strings.Contains(out, "`") {
		t.Errorf("code markers survived: %q", out)
	}
	for _, want := range []string{"Bold lead.", "quiet aside", "code"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q was lost: %q", want, out)
		}
	}
}

func TestLiteralStarsInProseAreKept(t *testing.T) {
	src := "Glob patterns like dev-* stay, and `*` means every scope."
	out := renderMarkdown(src, 100)

	if !strings.Contains(out, "dev-*") {
		t.Errorf("a lone star is content, not emphasis: %q", out)
	}
}
