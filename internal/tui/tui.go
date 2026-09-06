package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/spaaleks/gitlift"
	"github.com/spaaleks/gitlift/internal/config"
	"github.com/spaaleks/gitlift/internal/engine"
	"github.com/spaaleks/gitlift/internal/logx"
	"github.com/spaaleks/gitlift/internal/provider"
	"github.com/spaaleks/gitlift/internal/registry"
	"github.com/spaaleks/gitlift/internal/tmpl"
)

type stage int

const (
	stageLoading stage = iota
	stageSetup
	stageMenu
	stageProviders
	stagePickProvider
	stagePickTemplate
	stagePickProject
	stagePickProjects
	stagePickMode
	stageForm
	stageReview
	stageRun
	stageReport
	stageHelp
	stageAuthorSource
	stageAuthorKind
	stageAuthorPickRepo
	stageAuthorPickTemplate
	stageAuthorForm
	stageAuthorDone
)

type flowKind int

const (
	flowNone flowKind = iota
	flowCreate
	flowManage
	flowBulk
	flowAuthor
)

type loadedMsg struct {
	projects   []provider.Project
	owners     []provider.Provider
	namespaces map[string][]provider.Namespace
	problems   []string
}

type nameCheckedMsg struct {
	taken bool
	err   error
}

type runStepMsg struct {
	target string
	step   provider.Step
}

type runDoneMsg struct {
	results []engine.Result
}

type tickMsg struct{}

type app struct {
	width  int
	height int
	stage  stage
	flow   flowKind

	cfg       config.Config
	cfgErr    error
	providers []provider.Provider
	templates []tmpl.Template
	tmplErr   error

	projects   []provider.Project
	owners     []provider.Provider
	namespaces map[string][]provider.Namespace
	problems   []string
	loading    bool
	frame      int

	list list
	form form
	pane pane

	status    string
	statusBad bool

	activeProvider provider.Provider
	activeSpec     tmpl.Spec
	activeProject  provider.Project
	ruleMode       provider.RuleMode

	pending      int
	formReady    bool
	authorSource authorSource
	authorKind   string
	authorScope  string
	authorKinds  []string
	authorSpec   tmpl.Spec
	authorSteps  []provider.Step
	authorPath   string
	bulkTargets  []int
	bulkKinds    []string
	bulkKindPos  int
	bulkSpecs    map[string]tmpl.Spec
	bulkTemplate map[string]string

	plans   []engine.Plan
	steps   []runStepMsg
	results []engine.Result
	events  chan tea.Msg

	quitting bool
}

func newApp() *app {
	return &app{
		stage:        stageLoading,
		loading:      true,
		width:        100,
		height:       32,
		ruleMode:     provider.ModeAppend,
		namespaces:   map[string][]provider.Namespace{},
		bulkSpecs:    map[string]tmpl.Spec{},
		bulkTemplate: map[string]string{},
	}
}

func Run() error {
	_, err := tea.NewProgram(newApp(), tea.WithAltScreen()).Run()
	return err
}

func (m *app) Init() tea.Cmd {
	m.cfg, m.cfgErr = config.Load()
	m.templates, m.tmplErr = tmpl.Load(tmpl.SearchDirs())

	if m.cfgErr != nil {
		m.loading = false
		m.stage = stageSetup
		return nil
	}

	var errs []error
	m.providers, errs = registry.All(m.cfg)
	for _, err := range errs {
		m.problems = append(m.problems, err.Error())
	}

	if len(m.providers) == 0 {
		m.loading = false
		m.stage = stageSetup
		return nil
	}

	m.openMenu()
	return tea.Batch(loadProjects(m.providers), tick())
}

func tick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func loadProjects(providers []provider.Provider) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		type bucket struct {
			projects []provider.Project
			spaces   []provider.Namespace
			err      error
		}

		buckets := make([]bucket, len(providers))
		var wg sync.WaitGroup

		for i, p := range providers {
			wg.Add(1)
			go func(index int, current provider.Provider) {
				defer wg.Done()
				found, err := current.ListProjects(ctx)
				spaces, spaceErr := current.Namespaces(ctx)
				if spaceErr != nil {
					logx.Err("namespaces "+current.Name(), spaceErr)
				}
				buckets[index] = bucket{projects: found, spaces: spaces, err: err}
			}(i, p)
		}
		wg.Wait()

		msg := loadedMsg{namespaces: map[string][]provider.Namespace{}}
		for i, b := range buckets {
			if len(b.spaces) > 0 {
				msg.namespaces[providers[i].Name()] = b.spaces
			}
			if b.err != nil {
				msg.problems = append(msg.problems,
					fmt.Sprintf("%s: %s", providers[i].Name(), provider.Explain(b.err)))
				continue
			}
			for _, project := range b.projects {
				msg.projects = append(msg.projects, project)
				msg.owners = append(msg.owners, providers[i])
			}
		}
		return msg
	}
}

func (m *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.WindowSizeMsg:
		if typed.Width > 0 {
			m.width = typed.Width
		}
		if typed.Height > 0 {
			m.height = typed.Height
		}
		return m, nil

	case tickMsg:
		m.frame++
		if m.loading || m.stage == stageRun {
			return m, tick()
		}
		return m, nil

	case loadedMsg:
		m.loading = false
		m.projects = typed.projects
		m.owners = typed.owners
		if len(typed.namespaces) > 0 {
			m.namespaces = typed.namespaces
		}
		m.problems = append(m.problems, typed.problems...)

		if m.stage == stageLoading {
			m.openMenu()
		} else if m.stage == stageMenu {
			m.refreshMenu()
		}

		if choice := m.pending; choice != 0 && m.stage == stageMenu {
			m.pending = 0
			m.setStatus("", false)
			return m, m.chooseMenu(choice)
		}
		return m, nil

	case nameCheckedMsg:
		return m, m.handleNameChecked(typed)

	case capturedMsg:
		return m, m.handleCaptured(typed)

	case runStepMsg:
		m.steps = append(m.steps, typed)
		return m, waitFor(m.events)

	case runDoneMsg:
		m.results = typed.results
		return m, m.openReport()

	case tea.KeyMsg:
		return m, m.handleKey(typed)
	}

	return m, nil
}

func waitFor(events chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-events }
}

func (m *app) showLogo() bool {
	switch m.stage {
	case stageMenu, stageLoading, stageSetup:
		return m.width >= logoWidth()+2 && m.height >= 18
	}
	return false
}

func (m *app) headerHeight() int {
	return lineCount(m.header())
}

func (m *app) rows() int {
	return max(m.height-m.headerHeight()-6, 3)
}

func (m *app) handleKey(msg tea.KeyMsg) tea.Cmd {
	if msg.String() == "ctrl+c" {
		m.quitting = true
		return tea.Quit
	}

	switch m.stage {
	case stageLoading:
		return nil

	case stageSetup:
		return m.keySetup(msg)

	case stageRun:
		return nil

	case stageProviders, stageHelp, stageReport, stageAuthorDone:
		if m.pane.update(msg, m.rows()) != eventNone {
			m.openMenu()
		}
		return nil

	case stageReview:
		return m.keyReview(msg)

	case stageForm, stageAuthorForm:
		switch m.form.update(msg) {
		case eventSubmit:
			return m.submitForm()
		case eventBack:
			m.goBack()
		}
		return nil

	default:
		switch m.list.update(msg, m.rows()) {
		case eventSubmit:
			return m.submitList()
		case eventBack:
			m.goBack()
		}
		return nil
	}
}

func (m *app) keySetup(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "q", "esc":
		m.quitting = true
		return tea.Quit
	case "w":
		path := config.DefaultPath()
		if err := config.WriteExample(path); err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		m.setStatus("wrote "+path+", fill in your tokens, then restart gitlift", false)
	}
	return nil
}

func (m *app) keyReview(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.goBack()
		return nil
	case "enter":
		return m.startRun()
	case "e":
		if m.formReady {
			m.stage = stageForm
			return nil
		}
		m.setStatus("this template declares no editable values, only branch rules and other blocks", false)
		return nil
	case "q":
		m.quitting = true
		return tea.Quit
	}
	m.pane.update(msg, m.rows())
	return nil
}

func (m *app) setStatus(text string, bad bool) {
	m.status, m.statusBad = text, bad
}

func (m *app) openMenu() {
	m.flow = flowNone
	m.stage = stageMenu
	m.pending = 0
	m.formReady = false
	m.form = form{}
	m.setStatus("", false)

	m.list = newList("Gitlift", m.menuItems(), false)
	m.list.filter = false
}

func (m *app) refreshMenu() {
	cursor, top := m.list.cursor, m.list.top
	m.list = newList("Gitlift", m.menuItems(), false)
	m.list.filter = false
	m.list.cursor, m.list.top = cursor, top
}

func (m *app) menuItems() []listItem {
	count := fmt.Sprintf("(%d)", len(m.projects))
	if m.loading {
		count = "…"
	}

	return []listItem{
		{title: "Create a new repository", desc: "from a template", ref: 0},
		{title: "Configure an existing repository " + count, desc: "apply a template", ref: 1},
		{title: "Bulk sync settings " + count, desc: "features, rules, merge and CI settings", ref: 2},
		{title: "Create a template", desc: "from a repository, from scratch, or from another template", ref: 3},
		{title: "Providers", desc: fmt.Sprintf("%d configured", len(m.cfg.Providers)), ref: 4},
		{title: "Help", ref: 5},
		{title: "Quit", ref: 6},
	}
}

func (m *app) waitForProjects(choice int) bool {
	if !m.loading {
		return false
	}
	m.pending = choice
	m.setStatus("fetching repositories…", false)
	return true
}

func (m *app) submitList() tea.Cmd {
	results := m.list.results()
	if len(results) == 0 {
		return nil
	}

	switch m.stage {
	case stageMenu:
		return m.chooseMenu(results[0])
	case stagePickProvider:
		m.activeProvider = m.providers[results[0]]
		m.openTemplatePicker(m.activeProvider.Kind())
		return nil
	case stagePickProject:
		m.activeProject = m.projects[results[0]]
		m.activeProvider = m.owners[results[0]]
		m.openTemplatePicker(m.activeProvider.Kind())
		return nil
	case stagePickProjects:
		return m.chooseBulkTargets(results)
	case stagePickTemplate:
		return m.chooseTemplate(results[0])
	case stageAuthorSource:
		return m.chooseAuthorSource(results[0])
	case stageAuthorKind:
		m.authorKind = m.authorKinds[results[0]]
		m.openAuthorForm()
		return nil
	case stageAuthorPickRepo:
		return m.captureRepository()
	case stageAuthorPickTemplate:
		return m.chooseAuthorTemplate(results[0])
	case stagePickMode:
		m.ruleMode = provider.ModeAppend
		if results[0] == 1 {
			m.ruleMode = provider.ModeReplace
		}
		return m.afterMode()
	}
	return nil
}

func (m *app) chooseMenu(choice int) tea.Cmd {
	switch choice {
	case 0:
		m.flow = flowCreate
		return m.openProviderPicker()
	case 1:
		if m.waitForProjects(choice) {
			return nil
		}
		m.flow = flowManage
		m.openProjectPicker(false)
		return nil
	case 2:
		if m.waitForProjects(choice) {
			return nil
		}
		m.flow = flowBulk
		m.openProjectPicker(true)
		return nil
	case 3:
		return m.openAuthorPicker()
	case 4:
		m.stage = stageProviders
		m.pane = newPane("Providers", m.providerReport())
		return nil
	case 5:
		m.stage = stageHelp
		m.pane = newMarkdownPane("Help", gitlift.Help())
		return nil
	default:
		m.quitting = true
		return tea.Quit
	}
}

func (m *app) openProviderPicker() tea.Cmd {
	if len(m.providers) == 1 {
		m.activeProvider = m.providers[0]
		m.openTemplatePicker(m.activeProvider.Kind())
		return nil
	}

	items := make([]listItem, 0, len(m.providers))
	for i, p := range m.providers {
		items = append(items, listItem{title: p.Name(), desc: p.Kind() + " · " + p.Host(), ref: i})
	}

	m.stage = stagePickProvider
	m.list = newList("Where should the repository be created?", items, false)
	return nil
}

func (m *app) openProjectPicker(multi bool) {
	items := make([]listItem, 0, len(m.projects))
	for i, project := range m.projects {
		details := []string{project.ProviderName, project.Visibility}
		if project.Archived {
			details = append(details, "archived")
		}
		if project.Empty {
			details = append(details, "empty")
		}
		items = append(items, listItem{
			title: project.FullName,
			desc:  strings.Join(details, " · "),
			ref:   i,
		})
	}

	title := "Pick a repository"
	if multi {
		title = "Bulk sync: pick repositories"
		m.stage = stagePickProjects
	} else {
		m.stage = stagePickProject
	}

	m.list = newList(title, items, multi)
	m.list.emptyMsg = "no repositories loaded, check Providers on the main menu"
}

func (m *app) openTemplatePicker(kind string) {
	items := make([]listItem, 0, len(m.templates))
	for i, template := range m.templates {
		if !template.Supports(kind) {
			continue
		}
		details := template.Description
		if details == "" {
			details = template.ProviderLabel()
		}
		details += " · " + template.Scope
		items = append(items, listItem{title: template.Name, desc: details, ref: i})
	}

	title := "Pick a template"
	if m.flow == flowBulk {
		title = fmt.Sprintf("Template for %s (%d of %d)", kind, m.bulkKindPos+1, len(m.bulkKinds))
	}

	m.stage = stagePickTemplate
	m.list = newList(title, items, false)
	m.list.emptyMsg = "no template supports " + kind
}

func (m *app) chooseTemplate(index int) tea.Cmd {
	template := m.templates[index]

	var kind string
	if m.flow == flowBulk {
		kind = m.bulkKinds[m.bulkKindPos]
	} else {
		kind = m.activeProvider.Kind()
	}

	spec, err := template.For(kind)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}

	if m.flow == flowBulk {
		m.bulkSpecs[kind] = spec
		m.bulkTemplate[kind] = template.Name
		m.bulkKindPos++
		if m.bulkKindPos < len(m.bulkKinds) {
			m.openTemplatePicker(m.bulkKinds[m.bulkKindPos])
			return nil
		}
		m.openModePicker()
		return nil
	}

	m.activeSpec = spec

	if m.flow == flowManage {
		m.openModePicker()
		return nil
	}

	m.openCreateForm()
	return nil
}

func (m *app) chooseBulkTargets(targets []int) tea.Cmd {
	m.bulkTargets = targets
	m.bulkKinds = nil
	m.bulkSpecs = map[string]tmpl.Spec{}
	m.bulkTemplate = map[string]string{}
	m.bulkKindPos = 0

	seen := map[string]bool{}
	for _, index := range targets {
		kind := m.owners[index].Kind()
		if !seen[kind] {
			seen[kind] = true
			m.bulkKinds = append(m.bulkKinds, kind)
		}
	}

	m.openTemplatePicker(m.bulkKinds[0])
	return nil
}

func (m *app) openModePicker() {
	rules := m.activeSpec.Rules()
	if m.flow == flowBulk {
		rules = nil
		for _, kind := range m.bulkKinds {
			rules = append(rules, m.bulkSpecs[kind].Rules()...)
		}
	}

	if len(rules) == 0 {
		m.ruleMode = provider.ModeAppend
		m.afterMode()
		return
	}

	names := make([]string, 0, len(rules))
	for _, rule := range rules {
		names = append(names, rule.Name)
	}

	m.stage = stagePickMode
	m.list = newList("Branch rules: "+strings.Join(names, ", "), []listItem{
		{title: "Append", desc: "add or update the template's rules, leave any others alone", ref: 0},
		{title: "Replace", desc: "the template becomes the only branch rules, others are removed", ref: 1},
	}, false)
	m.list.filter = false
}

func (m *app) afterMode() tea.Cmd {
	if m.flow == flowBulk {
		m.openBulkForm()
		return nil
	}
	m.openManageForm()
	return nil
}

func (m *app) openBulkForm() {
	var fields []field
	multi := len(m.bulkKinds) > 1

	var only provider.Project
	if len(m.bulkTargets) == 1 {
		only = m.projects[m.bulkTargets[0]]
	}

	for _, kind := range m.bulkKinds {
		spec := m.bulkSpecs[kind]
		if spec.Repo == nil {
			continue
		}

		rows, _ := provider.FeatureRows(spec.Repo.Features, only.Features, kind)
		if len(rows) == 0 {
			continue
		}

		label := "features"
		if multi {
			label = kind + " features"
		}
		fields = append(fields, field{id: "_features:" + kind, label: label, kind: fieldHeading})

		for _, row := range rows {
			hint := ""
			if row.HasLive && row.Live != row.Want {
				hint = "currently: " + boolWord(row.Live)
			}
			fields = append(fields, field{
				id: "feature:" + kind + ":" + row.Key, label: row.Label,
				kind: fieldToggle, on: row.Want, diff: hint,
			})
		}
	}

	if len(fields) == 0 {
		m.form = form{}
		m.formReady = false
		m.buildBulkPlans()
		m.openReview()
		return
	}

	intro := fmt.Sprintf("applied to all %d repositories. Branch rules, merge and CI settings come from the template unchanged",
		len(m.bulkTargets))
	if only.FullName != "" {
		intro = "applied to " + only.FullName + ". Where it differs today is noted below the field"
	}

	m.form = newForm("Bulk sync: features", intro, fields)
	m.formReady = true
	m.stage = stageForm
}

func (m *app) goBack() {
	switch m.stage {
	case stagePickProvider, stagePickProject, stagePickProjects, stageAuthorSource:
		m.openMenu()

	case stageAuthorKind, stageAuthorPickRepo, stageAuthorPickTemplate:
		m.openAuthorPicker()

	case stageAuthorForm:
		switch m.authorSource {
		case authorFromRepo:
			m.openProjectPicker(false)
			m.stage = stageAuthorPickRepo
		case authorFromTemplate:
			m.openAuthorTemplatePicker()
		default:
			m.openAuthorPicker()
		}

	case stagePickTemplate:
		switch m.flow {
		case flowCreate:
			if len(m.providers) == 1 {
				m.openMenu()
				return
			}
			m.openProviderPicker()
		case flowManage:
			m.openProjectPicker(false)
		case flowBulk:
			if m.bulkKindPos > 0 {
				m.bulkKindPos--
				m.openTemplatePicker(m.bulkKinds[m.bulkKindPos])
				return
			}
			m.openProjectPicker(true)
		}

	case stagePickMode:
		m.openTemplatePicker(m.kindForTemplatePicker())

	case stageForm:
		if m.flow == flowBulk {
			m.openModePicker()
			return
		}
		if len(m.activeSpec.Rules()) > 0 && m.flow == flowManage {
			m.openModePicker()
			return
		}
		m.openTemplatePicker(m.kindForTemplatePicker())

	case stageReview:
		if m.formReady {
			m.stage = stageForm
			return
		}
		m.openModePicker()

	default:
		m.openMenu()
	}
}

func (m *app) kindForTemplatePicker() string {
	if m.flow == flowBulk && len(m.bulkKinds) > 0 {
		return m.bulkKinds[max(m.bulkKindPos-1, 0)]
	}
	if m.activeProvider != nil {
		return m.activeProvider.Kind()
	}
	return "github"
}

func (m *app) openCreateForm() {
	spec := m.activeSpec
	kind := m.activeProvider.Kind()

	fields := []field{
		{id: "_repo", label: "repository", kind: fieldHeading},
		{id: "name", label: "name", kind: fieldText, required: true, note: "the path segment, e.g. my-service"},
	}

	label := "owner"
	wanted := spec.Context.Owner
	if kind == "gitlab" {
		label = "namespace"
		wanted = spec.Context.Namespace
		fields = append(fields, field{id: "display", label: "display name", kind: fieldText,
			note: "shown in the GitLab UI, defaults to the name"})
	}

	fields = append(fields, m.namespaceField(label, wanted))

	fields = append(fields, settingsFields(spec, provider.Project{}, kind, false)...)
	fields = append(fields, variableFields(spec, kind)...)

	m.form = newForm("New repository: "+spec.Name,
		"only the settings this template declares are shown, everything else is left to the provider's defaults",
		fields)
	m.formReady = true
	m.stage = stageForm
}

func (m *app) namespaceField(label, wanted string) field {
	spaces := m.namespaces[m.activeProvider.Name()]

	if len(spaces) == 0 {
		note := "the user or organization that will own the repository"
		if m.activeProvider.Kind() == "gitlab" {
			note = "group path, leave empty for your personal namespace"
		}
		return field{id: "owner", label: label, kind: fieldText, text: wanted, note: note}
	}

	choices := make([]string, 0, len(spaces))
	for _, space := range spaces {
		choices = append(choices, space.Label())
	}

	chosen := 0
	for i, space := range spaces {
		if space.Path == wanted {
			chosen = i
		}
	}

	return field{id: "owner", label: label, kind: fieldChoice, choices: choices, choice: chosen,
		note: fmt.Sprintf("%d available on %s", len(spaces), m.activeProvider.Name())}
}

func (m *app) selectedNamespace() provider.Namespace {
	chosen := m.form.get("owner")

	for _, space := range m.namespaces[m.activeProvider.Name()] {
		if space.Label() == chosen || space.Path == chosen {
			return space
		}
	}
	return provider.Namespace{Path: strings.TrimSpace(chosen)}
}

func (m *app) openManageForm() {
	spec := m.activeSpec
	kind := m.activeProvider.Kind()

	fields := settingsFields(spec, m.activeProject, kind, true)
	fields = append(fields, variableFields(spec, kind)...)

	if len(fields) == 0 {
		m.form = form{}
		m.formReady = false
		m.buildManagePlan()
		m.openReview()
		return
	}

	m.form = newForm("Configure "+m.activeProject.FullName+": "+spec.Name,
		"values come from the template. Where the repository differs today it is noted below the field",
		fields)
	m.formReady = true
	m.stage = stageForm
}

func settingsFields(spec tmpl.Spec, current provider.Project, kind string, compare bool) []field {
	if spec.Repo == nil {
		return nil
	}

	fields := []field{{id: "_settings", label: "settings", kind: fieldHeading}}
	added := false

	note := func(live, want string) string {
		if compare && live != want {
			return "currently: " + live
		}
		return ""
	}

	if spec.Repo.Visibility != nil {
		choices := tmpl.Visibilities()
		fields = append(fields, field{
			id: "visibility", label: "visibility", kind: fieldChoice,
			choices: choices, choice: indexOf(choices, *spec.Repo.Visibility),
			diff: note(current.Visibility, *spec.Repo.Visibility),
		})
		added = true
	}

	if spec.Repo.Description != nil {
		value := *spec.Repo.Description
		if compare && value == "" && current.Description != "" {
			value = current.Description
		}
		fields = append(fields, field{
			id: "description", label: "description", kind: fieldText, text: value,
			diff: note(current.Description, value),
		})
		added = true
	}

	if spec.Repo.DefaultBranch != nil {
		fields = append(fields, field{
			id: "default_branch", label: "default branch", kind: fieldText, text: *spec.Repo.DefaultBranch,
			diff: note(current.DefaultBranch, *spec.Repo.DefaultBranch),
		})
		added = true
	}

	if len(spec.Repo.Topics) > 0 {
		fields = append(fields, field{
			id: "topics", label: "topics", kind: fieldText,
			text: strings.Join(spec.Repo.Topics, ", "),
			note: "comma separated",
		})
		added = true
	}

	rows, _ := provider.FeatureRows(spec.Repo.Features, current.Features, kind)
	for _, row := range rows {
		hint := ""
		if compare && row.HasLive && row.Live != row.Want {
			hint = "currently: " + boolWord(row.Live)
		}
		fields = append(fields, field{
			id: "feature:" + row.Key, label: row.Label, kind: fieldToggle, on: row.Want, diff: hint,
		})
		added = true
	}

	if !added {
		return nil
	}
	return fields
}

func variableFields(spec tmpl.Spec, kind string) []field {
	if len(spec.Variables) == 0 {
		return nil
	}

	fields := []field{{id: "_vars", label: "ci/cd variables", kind: fieldHeading}}
	for _, variable := range spec.Variables {
		label := variable.Key
		if variable.EnvironmentScope != "" {
			label += " [" + variable.EnvironmentScope + "]"
		}

		entry := field{
			id: variableFieldID(variable), label: label, kind: fieldText, text: variable.Value,
		}
		switch {
		case kind == "github" && variable.Secret():
			entry.kind = fieldSecret
			entry.note = "encrypted Actions secret, leave empty to keep the current one"
		case kind == "gitlab" && provider.DerefBool(variable.Masked, false):
			entry.kind = fieldSecret
			entry.note = "masked in job logs, leave empty to keep the current one"
		}
		fields = append(fields, entry)
	}
	return fields
}

func (m *app) submitForm() tea.Cmd {
	if m.stage == stageAuthorForm {
		return m.submitAuthorForm()
	}
	if m.flow == flowBulk {
		m.buildBulkPlans()
		m.openReview()
		return nil
	}
	if m.flow == flowCreate {
		return m.checkName()
	}
	m.buildManagePlan()
	m.openReview()
	return nil
}

func (m *app) checkName() tea.Cmd {
	name := strings.TrimSpace(m.form.get("name"))
	owner := m.selectedNamespace().Path
	current := m.activeProvider

	m.setStatus("checking whether "+name+" is free…", false)
	m.loading = true

	return tea.Batch(tick(), func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		if current.Kind() == "gitlab" && owner == "" {
			return nameCheckedMsg{}
		}

		_, found, err := current.Lookup(ctx, provider.Target{Owner: owner}, name)
		return nameCheckedMsg{taken: found, err: err}
	})
}

func (m *app) handleNameChecked(msg nameCheckedMsg) tea.Cmd {
	m.loading = false
	m.setStatus("", false)

	if msg.err != nil {
		logx.Err("lookup", msg.err)
		m.form.err = provider.Explain(msg.err)
		return nil
	}
	if msg.taken {
		m.form.err = "a repository with that name already exists here, pick another"
		return nil
	}

	m.buildCreatePlan()
	m.openReview()
	return nil
}

func (m *app) formSettings(spec tmpl.Spec, name, display string) provider.Settings {
	settings := engine.Settings(spec, name, display)

	if m.form.has("visibility") {
		settings.Visibility = provider.String(m.form.get("visibility"))
	}
	if m.form.has("description") {
		settings.Description = provider.String(m.form.get("description"))
	}
	if m.form.has("default_branch") {
		settings.DefaultBranch = provider.String(m.form.get("default_branch"))
	}
	if m.form.has("topics") {
		settings.Topics = splitList(m.form.get("topics"))
	}

	for _, key := range spec.Features().Keys() {
		if m.form.has("feature:" + key) {
			settings.Features.Set(key, m.form.getBool("feature:"+key))
		}
	}

	return settings
}

func splitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func variableFieldID(variable tmpl.Variable) string {
	return "var:" + variable.Scope() + ":" + variable.Key
}

func (m *app) formVariables(spec tmpl.Spec) []tmpl.Variable {
	variables := make([]tmpl.Variable, 0, len(spec.Variables))
	for _, variable := range spec.Variables {
		id := variableFieldID(variable)
		if m.form.has(id) {
			variable.Value = m.form.get(id)
		}
		variables = append(variables, variable)
	}
	return variables
}

func (m *app) buildCreatePlan() {
	spec := m.activeSpec
	name := strings.TrimSpace(m.form.get("name"))
	display := strings.TrimSpace(m.form.get("display"))
	if display == "" {
		display = name
	}

	settings := m.formSettings(spec, name, display)
	if settings.Description != nil && *settings.Description == "" {
		settings.Description = provider.String(display)
	}

	space := m.selectedNamespace()

	m.plans = []engine.Plan{engine.FromSpec(engine.Plan{
		Provider: m.activeProvider,
		Template: spec.Name,
		Create:   true,
		Target: provider.Target{
			Owner:     space.Path,
			OwnerType: orDefault(space.Kind, "user"),
		},
		Settings:  settings,
		RuleMode:  provider.ModeAppend,
		Variables: m.formVariables(spec),
	}, spec)}
}

func (m *app) buildManagePlan() {
	spec := m.activeSpec

	m.plans = []engine.Plan{engine.FromSpec(engine.Plan{
		Provider:  m.activeProvider,
		Template:  spec.Name,
		Project:   m.activeProject,
		Settings:  m.formSettings(spec, m.activeProject.Name, m.activeProject.Name),
		RuleMode:  m.ruleMode,
		Variables: m.formVariables(spec),
	}, spec)}
}

func (m *app) buildBulkPlans() {
	m.plans = nil

	for _, index := range m.bulkTargets {
		target := m.projects[index]
		current := m.owners[index]
		spec := m.bulkSpecs[current.Kind()]

		settings := provider.Settings{Name: target.Name, DisplayName: target.Name}
		if spec.Repo != nil {
			settings.Features = spec.Repo.Features.Clone()
			for _, key := range settings.Features.Keys() {
				id := "feature:" + current.Kind() + ":" + key
				if m.form.has(id) {
					settings.Features.Set(key, m.form.getBool(id))
				}
			}
		}

		plan := engine.FromSpec(engine.Plan{
			Provider: current,
			Template: m.bulkTemplate[current.Kind()],
			Project:  target,
			Settings: settings,
			RuleMode: m.ruleMode,
		}, spec)

		plan.Webhooks = nil
		plan.Integrations = nil

		m.plans = append(m.plans, plan)
	}
}

func (m *app) openReview() {
	m.stage = stageReview

	if m.flow == flowBulk {
		var b strings.Builder
		fmt.Fprintf(&b, "%d repositories will be updated.\n\n", len(m.plans))
		b.WriteString("Features, branch and tag rules, merge settings and CI settings are applied.\n")
		b.WriteString("Descriptions, visibility, CI/CD variables, webhooks and integrations are left untouched.\n\n")
		for _, plan := range m.plans {
			fmt.Fprintf(&b, "  %s\n", plan.Project.FullName)
			fmt.Fprintf(&b, "      provider  %s\n", plan.Provider.Name())
			fmt.Fprintf(&b, "      template  %s\n", plan.Template)
			b.WriteString(featureLines(plan.Settings.Features, m.width))
			fmt.Fprintf(&b, "      rules     %s\n", ruleNames(plan.Rules))
			if len(plan.TagRules) > 0 {
				fmt.Fprintf(&b, "      tags      %s\n", tagRuleNames(plan.TagRules))
			}
		}
		if m.ruleMode == provider.ModeReplace {
			b.WriteString("\nBranch and tag rules: REPLACE. Rules not in the template are removed from every repository above.\n")
		} else {
			b.WriteString("\nBranch and tag rules: append. Rules not in the template are left alone.\n")
		}
		m.pane = newPane("Review: bulk sync", b.String())
		return
	}

	m.pane = newPane("Review", m.plans[0].Summary())
}

func (m *app) startRun() tea.Cmd {
	m.stage = stageRun
	m.steps = nil
	m.results = nil
	m.events = make(chan tea.Msg, 128)

	plans := m.plans
	events := m.events

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		results := make([]engine.Result, 0, len(plans))
		for _, plan := range plans {
			label := plan.Settings.Name
			if !plan.Create {
				label = plan.Project.FullName
			}

			result := engine.Run(ctx, plan, func(step provider.Step) {
				events <- runStepMsg{target: label, step: step}
			})
			results = append(results, result)
		}

		events <- runDoneMsg{results: results}
	}()

	return tea.Batch(waitFor(events), tick())
}

func (m *app) openReport() tea.Cmd {
	m.stage = stageReport

	var b strings.Builder
	var ok, skipped, failed int

	for i, result := range m.results {
		plan := m.plans[i]

		name := result.Project.FullName
		if name == "" {
			name = plan.Settings.Name
		}
		fmt.Fprintf(&b, "%s\n", name)
		if result.Project.URL != "" {
			fmt.Fprintf(&b, "  %s\n", result.Project.URL)
		}

		for _, step := range result.Steps {
			switch step.Status {
			case provider.StatusOK:
				ok++
			case provider.StatusSkipped:
				skipped++
			case provider.StatusFailed:
				failed++
			}
			b.WriteString(stepBlock(step))
		}
		b.WriteString("\n")
	}

	title := fmt.Sprintf("Done: %d applied, %d skipped, %d failed", ok, skipped, failed)
	if failed > 0 && logx.Path() != "" {
		fmt.Fprintf(&b, "Full request details are in %s\n", logx.Path())
	}

	m.pane = newPane(title, b.String())
	m.loading = true
	m.setStatus("refreshing repositories…", false)

	return tea.Batch(loadProjects(m.providers), tick())
}

func stepBlock(step provider.Step) string {
	var b strings.Builder

	compact := step
	compact.Items = nil
	fmt.Fprintf(&b, "  %s\n", stepLine(compact, 0))

	for _, item := range step.Items {
		fmt.Fprintf(&b, "      %s %s\n", dimStyle.Render("·"), itemStyle.Render(item))
	}

	return b.String()
}

func stepLine(step provider.Step, width int) string {
	var mark string
	switch step.Status {
	case provider.StatusOK:
		mark = okStyle.Render("✓")
	case provider.StatusSkipped:
		mark = warnStyle.Render("⚠")
	case provider.StatusFailed:
		mark = errStyle.Render("✗")
	default:
		mark = dimStyle.Render("·")
	}

	label := step.Scope + ": " + step.Label
	detail := ""
	if step.Detail != "" {
		detail = "  " + step.Detail
	}
	if len(step.Items) > 0 {
		detail += fmt.Sprintf("  (%d)", len(step.Items))
	}

	if width <= 0 {
		return mark + " " + itemStyle.Render(label) + dimStyle.Render(detail)
	}

	room := max(width, 30) - 2
	line := mark + " " + itemStyle.Render(truncate(label, room))
	room -= min(len([]rune(label)), room)

	if detail != "" && room > 8 {
		line += dimStyle.Render(truncate(detail, room))
	}
	return line
}

func (m *app) providerReport() string {
	var b strings.Builder

	if m.cfg.Path != "" {
		fmt.Fprintf(&b, "Config     %s\n", m.cfg.Path)
	}
	if logx.Path() != "" {
		fmt.Fprintf(&b, "Log        %s\n", logx.Path())
	}
	fmt.Fprintf(&b, "Templates  %d loaded\n", len(m.templates))
	if m.tmplErr != nil {
		fmt.Fprintf(&b, "           %s\n", m.tmplErr)
	}
	b.WriteString("\n")

	for _, entry := range m.cfg.Providers {
		fmt.Fprintf(&b, "%s\n", entry.Name)
		fmt.Fprintf(&b, "  type     %s\n", orDefault(entry.Type, "(unset)"))
		fmt.Fprintf(&b, "  base url %s\n", orDefault(entry.Auth.BaseURL, "(default)"))

		count := 0
		for _, owner := range m.owners {
			if owner.Name() == entry.Name {
				count++
			}
		}

		switch {
		case entry.Err != nil:
			fmt.Fprintf(&b, "  status   unusable: %s\n", entry.Err)
		default:
			fmt.Fprintf(&b, "  status   %d repositories loaded\n", count)
		}
		b.WriteString("\n")
	}

	if len(m.problems) > 0 {
		b.WriteString("Problems\n")
		for _, problem := range m.problems {
			fmt.Fprintf(&b, "  %s\n", problem)
		}
	}

	return b.String()
}

func (m *app) View() string {
	if m.quitting {
		return ""
	}

	header := m.header()
	footer := m.footer()
	body := m.body()

	inner := header + "\n\n" + body
	pad := m.height - lineCount(inner) - lineCount(footer)
	if pad < 1 {
		pad = 1
	}
	return inner + strings.Repeat("\n", pad) + footer
}

func (m *app) header() string {
	var status string
	switch {
	case m.loading:
		status = fmt.Sprintf("%d providers · %s loading", len(m.providers), m.spinner())
	case m.activeProvider != nil && m.stage != stageMenu:
		status = m.activeProvider.Name() + " · " + m.activeProvider.Kind()
	default:
		status = fmt.Sprintf("%d providers · %d repositories", len(m.providers), len(m.projects))
	}

	rule := dimStyle.Render(strings.Repeat("─", max(m.width, 1)))

	status = truncate(status, max(m.width-len("gitlift")-1, 0))
	rendered := dimStyle.Render(status)
	if status == "" {
		rendered = ""
	}

	if m.showLogo() {
		last := len(logoLines) - 1
		lines := make([]string, last)
		for i := 0; i < last; i++ {
			lines[i] = titleStyle.Render(logoLines[i])
		}

		tail := titleStyle.Render(logoLines[last])
		gap := m.width - len([]rune(logoLines[last])) - lineWidth(rendered)

		if gap >= 2 {
			lines = append(lines, tail+strings.Repeat(" ", gap)+rendered)
		} else {
			indent := max(m.width-lineWidth(rendered), 0)
			lines = append(lines, tail, strings.Repeat(" ", indent)+rendered)
		}

		return strings.Join(lines, "\n") + "\n" + rule
	}

	left := titleStyle.Render("gitlift")
	gap := m.width - lineWidth(left) - lineWidth(rendered)
	if gap < 1 {
		gap = 1
	}

	return left + strings.Repeat(" ", gap) + rendered + "\n" + rule
}

func (m *app) body() string {
	switch m.stage {
	case stageLoading:
		return "  " + m.spinner() + " " +
			itemStyle.Render("talking to your providers…")

	case stageSetup:
		return m.setupBody()

	case stageForm, stageAuthorForm:
		return m.form.view(m.width, m.rows())

	case stageReview, stageReport, stageProviders, stageHelp, stageAuthorDone:
		return m.pane.view(m.width, m.rows())

	case stageRun:
		return m.runBody()

	default:
		return m.list.view(m.width, m.rows())
	}
}

func (m *app) setupBody() string {
	var b strings.Builder

	b.WriteString(sectionStyle.Render("No usable provider yet") + "\n\n")

	if m.cfgErr != nil {
		b.WriteString(itemStyle.Render("  "+m.cfgErr.Error()) + "\n\n")
	}
	for _, entry := range m.cfg.Providers {
		if entry.Err != nil {
			b.WriteString(errStyle.Render("  "+entry.Name+": ") + itemStyle.Render(entry.Err.Error()) + "\n")
		}
	}

	b.WriteString("\n" + itemStyle.Render("  gitlift looks for a config in:") + "\n")
	for _, path := range config.SearchPaths() {
		b.WriteString(dimStyle.Render("    "+path) + "\n")
	}

	b.WriteString("\n" + itemStyle.Render("  Press ") + cursorStyle.Render("w") +
		itemStyle.Render(" to write a starter config to "+config.DefaultPath()) + "\n")

	return b.String()
}

func (m *app) runBody() string {
	var b strings.Builder

	b.WriteString(sectionStyle.Render("Applying") + "  " +
		dimStyle.Render(m.spinner()) + "\n\n")

	rows := m.rows() - 2
	start := max(len(m.steps)-rows, 0)
	grouped := len(m.plans) > 1
	shown := ""

	for _, entry := range m.steps[start:] {
		if grouped && entry.target != shown {
			shown = entry.target
			b.WriteString("  " + sectionStyle.Render(truncate(shown, m.width-4)) + "\n")
		}
		b.WriteString("  " + stepLine(entry.step, m.width-4) + "\n")
	}

	if len(m.steps) == 0 {
		b.WriteString(dimStyle.Render("  working…"))
	}

	return b.String()
}

func (m *app) spinner() string {
	return spinnerFrames[m.frame%len(spinnerFrames)]
}

func (m *app) footer() string {
	rule := dimStyle.Render(strings.Repeat("─", max(m.width, 1)))

	var lines []string
	if m.status != "" {
		note := m.status
		if m.loading && !m.statusBad {
			note = m.spinner() + " " + note
		}
		note = truncate(note, m.width)
		if m.statusBad {
			lines = append(lines, errStyle.Render(note))
		} else {
			lines = append(lines, dimStyle.Render(note))
		}
	}

	var keys string
	switch m.stage {
	case stageLoading:
		keys = "ctrl+c quit"
	case stageSetup:
		keys = "w write starter config · q quit"
	case stageForm, stageAuthorForm:
		keys = m.form.keys()
	case stageReview:
		if !m.formReady {
			keys = "enter apply · ↑↓ scroll · esc back"
		} else {
			keys = "enter apply · e edit values · ↑↓ scroll · esc back"
		}
	case stageRun:
		keys = "working, ctrl+c aborts"
	case stageReport, stageProviders, stageHelp, stageAuthorDone:
		keys = "↑↓ scroll · enter/esc back to menu"
	default:
		keys = m.list.keys()
	}

	lines = append(lines, dimStyle.Render(truncate(keys, m.width)))
	return rule + "\n" + strings.Join(lines, "\n")
}

func featureLines(features provider.Features, width int) string {
	keys := features.Keys()
	if len(keys) == 0 {
		return "      features  (none in template)\n"
	}

	var on, off []string
	for _, key := range keys {
		if value, _ := features.Get(key); value {
			on = append(on, key)
		} else {
			off = append(off, key)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "      features  %d declared\n", len(keys))
	b.WriteString(wrapList("                on ", on, width))
	b.WriteString(wrapList("                off", off, width))
	return b.String()
}

func wrapList(label string, keys []string, width int) string {
	if len(keys) == 0 {
		return ""
	}

	room := max(width-len(label)-2, 20)
	var b strings.Builder
	line := ""

	flush := func() {
		if line != "" {
			fmt.Fprintf(&b, "%s  %s\n", label, line)
			line = ""
			label = strings.Repeat(" ", len(label))
		}
	}

	for _, key := range keys {
		candidate := key
		if line != "" {
			candidate = line + ", " + key
		}
		if len(candidate) > room {
			flush()
			line = key
			continue
		}
		line = candidate
	}
	flush()

	return b.String()
}

func ruleNames(rules []tmpl.Rule) string {
	if len(rules) == 0 {
		return "(none in template)"
	}
	names := make([]string, 0, len(rules))
	for _, rule := range rules {
		names = append(names, rule.Name)
	}
	return strings.Join(names, ", ")
}

func tagRuleNames(rules []tmpl.TagRule) string {
	names := make([]string, 0, len(rules))
	for _, rule := range rules {
		names = append(names, rule.Name)
	}
	return strings.Join(names, ", ")
}

func boolWord(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func indexOf(values []string, want string) int {
	for i, value := range values {
		if value == want {
			return i
		}
	}
	return 0
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func lineCount(s string) int {
	return strings.Count(s, "\n") + 1
}

func lineWidth(s string) int {
	width := 0
	for _, line := range strings.Split(s, "\n") {
		stripped := 0
		inEscape := false
		for _, r := range line {
			switch {
			case r == '\x1b':
				inEscape = true
			case inEscape && r == 'm':
				inEscape = false
			case !inEscape:
				stripped++
			}
		}
		width = max(width, stripped)
	}
	return width
}
