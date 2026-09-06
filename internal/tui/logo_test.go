package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestLogoShowsOnEntryScreensAndFitsElsewhere(t *testing.T) {
	gh := githubRoutes(t)
	gl := gitlabRoutes(t)
	writeConfig(t, gh.server.URL, gl.server.URL)

	d := &driver{t: t, m: newApp()}
	d.m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	d.start()

	check := func(name string, wantLogo bool) {
		view := d.view()
		has := strings.Contains(view, logoLines[0])
		if has != wantLogo {
			t.Errorf("%s: logo present = %v, want %v", name, has, wantLogo)
		}
		if lines := strings.Split(view, "\n"); len(lines) > 30 {
			t.Errorf("%s renders %d lines into 30", name, len(lines))
		}
		t.Logf("\n=== %s ===\n%s", name, view)
	}

	check("menu", true)
	d.key("enter")
	check("provider picker", false)
}

func TestHeaderFitsEveryTerminalSize(t *testing.T) {
	gh := githubRoutes(t)
	gl := gitlabRoutes(t)
	writeConfig(t, gh.server.URL, gl.server.URL)

	cases := []struct {
		size     tea.WindowSizeMsg
		wantLogo bool
	}{
		{tea.WindowSizeMsg{Width: 16, Height: 30}, false},
		{tea.WindowSizeMsg{Width: 30, Height: 24}, true},
		{tea.WindowSizeMsg{Width: 100, Height: 14}, false},
		{tea.WindowSizeMsg{Width: 100, Height: 30}, true},
	}

	for _, tc := range cases {
		d := &driver{t: t, m: newApp()}
		d.m.Update(tc.size)
		d.start()

		view := d.view()
		if got := strings.Contains(view, logoLines[0]); got != tc.wantLogo {
			t.Errorf("%dx%d: logo present = %v, want %v",
				tc.size.Width, tc.size.Height, got, tc.wantLogo)
		}

		lines := strings.Split(view, "\n")
		for i, line := range lines {
			if lineWidth(line) > tc.size.Width {
				t.Errorf("%dx%d: line %d is %d columns wide",
					tc.size.Width, tc.size.Height, i, lineWidth(line))
			}
		}
		if len(lines) > tc.size.Height {
			t.Errorf("%dx%d: renders %d lines", tc.size.Width, tc.size.Height, len(lines))
		}
		t.Logf("\n=== %dx%d ===\n%s", tc.size.Width, tc.size.Height, view)
	}
}
