package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/spaaleks/gitlift/internal/logx"
	"github.com/spaaleks/gitlift/internal/provider"
	"github.com/spaaleks/gitlift/internal/tmpl"
)

type authorSource int

const (
	authorFromRepo authorSource = iota
	authorFromScratch
	authorFromTemplate
)

type capturedMsg struct {
	spec  tmpl.Spec
	steps []provider.Step
}

func (m *app) openAuthorPicker() tea.Cmd {
	m.flow = flowAuthor
	m.stage = stageAuthorSource
	m.authorSpec = tmpl.Spec{}
	m.authorSteps = nil
	m.authorScope = ""

	items := []listItem{
		{title: "From an existing repository", desc: "read its current settings and write them out", ref: int(authorFromRepo)},
		{title: "From scratch", desc: "start with every toggle and fill in the rest", ref: int(authorFromScratch)},
		{title: "From another template", desc: "copy one under a new name", ref: int(authorFromTemplate)},
	}

	m.list = newList("Create a template", items, false)
	m.list.filter = false
	return nil
}

func (m *app) chooseAuthorSource(choice int) tea.Cmd {
	m.authorSource = authorSource(choice)

	switch m.authorSource {
	case authorFromRepo:
		if m.waitForProjects(3) {
			return nil
		}
		if len(m.projects) == 0 {
			m.setStatus("no repositories to read, check the Providers screen", true)
			return nil
		}
		m.openProjectPicker(false)
		m.stage = stageAuthorPickRepo
		return nil

	case authorFromTemplate:
		return m.openAuthorTemplatePicker()

	default:
		return m.openAuthorKindPicker()
	}
}

func (m *app) openAuthorKindPicker() tea.Cmd {
	kinds := map[string]bool{}
	for _, p := range m.providers {
		kinds[p.Kind()] = true
	}
	if len(kinds) == 0 {
		kinds["github"], kinds["gitlab"] = true, true
	}

	var items []listItem
	for _, kind := range []string{"github", "gitlab"} {
		if kinds[kind] {
			items = append(items, listItem{title: kind, desc: describeKind(kind), ref: len(items)})
		}
	}

	if len(items) == 1 {
		m.authorKind = items[0].title
		m.openAuthorForm()
		return nil
	}

	m.authorKinds = nil
	for _, item := range items {
		m.authorKinds = append(m.authorKinds, item.title)
	}

	m.stage = stageAuthorKind
	m.list = newList("Which provider is this template for?", items, false)
	m.list.filter = false
	return nil
}

func describeKind(kind string) string {
	return fmt.Sprintf("%d feature toggles available", len(tmpl.FeatureKeys(kind)))
}

func (m *app) openAuthorTemplatePicker() tea.Cmd {
	var items []listItem
	for i, template := range m.templates {
		details := template.ProviderLabel() + " · " + template.Scope
		items = append(items, listItem{title: template.Name, desc: details, ref: i})
	}

	if len(items) == 0 {
		m.setStatus("no templates to copy", true)
		return nil
	}

	m.stage = stageAuthorPickTemplate
	m.list = newList("Copy which template?", items, false)
	return nil
}

func (m *app) chooseAuthorTemplate(index int) tea.Cmd {
	template := m.templates[index]

	kind := "gitlab"
	for _, candidate := range []string{"github", "gitlab"} {
		if template.Supports(candidate) {
			kind = candidate
			break
		}
	}

	spec, err := template.For(kind)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}

	m.authorKind = kind
	m.authorSpec = spec
	m.authorSpec.Name = ""
	if !template.Builtin {
		m.authorScope = template.Scope
	}
	m.openAuthorForm()
	return nil
}

func (m *app) captureRepository() tea.Cmd {
	current := m.owners[m.list.items[m.list.order[m.list.cursor]].ref]
	target := m.projects[m.list.items[m.list.order[m.list.cursor]].ref]

	m.activeProvider = current
	m.activeProject = target
	m.authorKind = current.Kind()
	m.loading = true
	m.setStatus("reading "+target.FullName+"…", false)

	return tea.Batch(func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		spec, steps := current.Capture(ctx, target)
		return capturedMsg{spec: spec, steps: steps}
	}, tick())
}

func (m *app) handleCaptured(msg capturedMsg) tea.Cmd {
	m.loading = false
	m.setStatus("", false)

	for _, step := range msg.steps {
		if step.Status == provider.StatusFailed {
			logx.Err("capture", fmt.Errorf("%s: %s", step.Label, step.Detail))
			m.setStatus("could not read the repository: "+step.Detail, true)
			m.openAuthorPicker()
			return nil
		}
	}

	m.authorSpec = msg.spec
	m.authorSteps = msg.steps
	m.openAuthorForm()
	return nil
}

func (m *app) openAuthorForm() {
	spec := m.authorSpec
	kind := m.authorKind

	suggested := spec.Name
	if suggested == "" && m.authorSource == authorFromRepo {
		suggested = m.activeProject.Name
	}

	fields := []field{
		{id: "_identity", label: "template", kind: fieldHeading},
		{id: "name", label: "name", kind: fieldText, text: suggested, required: true,
			note: "shown in the template list, and the file name"},
		{id: "description", label: "description", kind: fieldText, text: spec.Description},
	}

	visibilities := tmpl.Visibilities()
	current := provider.DerefString(specVisibility(spec), "private")
	fields = append(fields,
		field{id: "_repo", label: "repository", kind: fieldHeading},
		field{id: "visibility", label: "visibility", kind: fieldChoice,
			choices: visibilities, choice: indexOf(visibilities, current)})

	fields = append(fields, field{id: "_features", label: "features", kind: fieldHeading})
	for _, key := range tmpl.FeatureKeys(kind) {
		def, _ := tmpl.FeatureDefFor(key)
		on, declared := spec.Features().Get(key)
		if !declared {
			on = defaultFeature(key)
		}
		fields = append(fields, field{
			id: "feature:" + key, label: def.Label, kind: fieldToggle, on: on,
		})
	}

	var dirs []string
	for _, dir := range tmpl.WriteDirs() {
		dirs = append(dirs, tmpl.Abbrev(dir))
	}
	folderNote := "optional subfolder. It becomes the group shown beside the template"
	if existing := tmpl.Scopes(m.templates); len(existing) > 0 {
		folderNote += " (existing: " + strings.Join(existing, ", ") + ")"
	}

	fields = append(fields,
		field{id: "_save", label: "save to", kind: fieldHeading},
		field{id: "dir", label: "directory", kind: fieldChoice, choices: dirs, choice: 0,
			note: "gitlift also loads templates from here, so it will appear in the list straight away"},
		field{id: "folder", label: "folder", kind: fieldText, text: m.authorScope, note: folderNote})

	intro := "every toggle below is written into the template, so nothing is left to chance later"
	if m.authorSource == authorFromRepo {
		intro = "read from " + m.activeProject.FullName + ". Branch rules and merge settings came across too"
	}

	m.form = newForm("New template: "+kind, intro, fields)
	m.formReady = true
	m.stage = stageAuthorForm
}

func defaultFeature(key string) bool {
	switch key {
	case "issues", "merge_requests", "repository", "builds", "releases",
		"environments", "analytics", "forking", "wiki", "projects":
		return true
	default:
		return false
	}
}

func specVisibility(spec tmpl.Spec) *string {
	if spec.Repo == nil {
		return nil
	}
	return spec.Repo.Visibility
}

func (m *app) submitAuthorForm() tea.Cmd {
	spec := m.authorSpec
	kind := m.authorKind

	spec.Name = strings.TrimSpace(m.form.get("name"))
	spec.Description = strings.TrimSpace(m.form.get("description"))

	if spec.Repo == nil {
		spec.Repo = &tmpl.Repo{}
	}
	spec.Repo.Visibility = provider.String(m.form.get("visibility"))

	features := tmpl.Features{}
	for _, key := range tmpl.FeatureKeys(kind) {
		if m.form.has("feature:" + key) {
			features.Set(key, m.form.getBool("feature:"+key))
		}
	}
	spec.Repo.Features = features

	dir := tmpl.Expand(m.form.get("dir"))
	if folder := tmpl.FolderName(m.form.get("folder")); folder != "" {
		dir = filepath.Join(dir, folder)
	}

	path, err := tmpl.Save(spec, kind, dir)
	if err != nil {
		m.form.err = err.Error()
		return nil
	}

	m.authorSpec = spec
	m.authorPath = path

	reloaded, loadErr := tmpl.Load(tmpl.SearchDirs())
	if loadErr == nil {
		m.templates = reloaded
	}

	m.stage = stageAuthorDone
	m.pane = newPane("Template written", m.authorReport(path, kind))
	return nil
}

func (m *app) authorReport(path, kind string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s\n\n", path)

	if len(m.authorSteps) > 0 {
		b.WriteString("Captured from " + m.activeProject.FullName + "\n")
		for _, step := range m.authorSteps {
			b.WriteString(stepBlock(step))
		}
		b.WriteString("\n")
	}

	body, err := tmpl.Encode(m.authorSpec, kind)
	if err != nil {
		fmt.Fprintf(&b, "could not render the template: %v\n", err)
		return b.String()
	}

	b.WriteString(string(body))
	b.WriteString("\nIt is in the template list now. Pick it from any of the three flows.\n")

	return b.String()
}
