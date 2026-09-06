package tui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/spaaleks/gitlift/internal/provider"
	"github.com/spaaleks/gitlift/internal/tmpl"
)

type forge struct {
	server *httptest.Server
	mu     sync.Mutex
	seen   []string
}

func (f *forge) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *forge) sent(method, path string) bool {
	for _, request := range f.requests() {
		if request == method+" "+path {
			return true
		}
	}
	return false
}

func newForge(t *testing.T, prefix string, handle func(method, path string) (int, string)) *forge {
	t.Helper()

	f := &forge{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, prefix)

		f.mu.Lock()
		f.seen = append(f.seen, r.Method+" "+path)
		f.mu.Unlock()

		status, body := handle(r.Method, path)
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(f.server.Close)

	return f
}

func writeConfig(t *testing.T, githubURL, gitlabURL string) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "providers.yaml")
	body := fmt.Sprintf(`providers:
  - name: "GitHub"
    type: "github"
    auth:
      token: "gh-token"
      base_url: "%s/api/v3"
  - name: "GitLab"
    type: "gitlab"
    auth:
      token: "gl-token"
      base_url: "%s"
`, githubURL, gitlabURL)

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GITLIFT_CONFIG", path)
	t.Setenv("GITLIFT_TEMPLATES", filepath.Join(dir, "none"))
	t.Setenv("XDG_CONFIG_HOME", dir)
}

type driver struct {
	t *testing.T
	m *app
}

func (d *driver) run(cmd tea.Cmd) {
	d.t.Helper()

	for depth := 0; cmd != nil && depth < 200; depth++ {
		msg := cmd()

		switch typed := msg.(type) {
		case nil:
			return
		case tea.BatchMsg:
			cmd = nil
			for _, next := range typed {
				d.run(next)
			}
			return
		case tickMsg:
			return
		}

		_, cmd = d.m.Update(msg)
	}
}

func (d *driver) key(name string) {
	d.t.Helper()

	var msg tea.KeyMsg
	switch name {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		msg = tea.KeyMsg{Type: tea.KeyTab}
	case "ctrl+s":
		msg = tea.KeyMsg{Type: tea.KeyCtrlS}
	case "left":
		msg = tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		msg = tea.KeyMsg{Type: tea.KeyRight}
	default:
		d.t.Fatalf("unknown key %q", name)
	}

	_, cmd := d.m.Update(msg)
	d.run(cmd)
}

func (d *driver) typeText(s string) {
	d.t.Helper()
	_, cmd := d.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	d.run(cmd)
}

func (d *driver) start() {
	d.t.Helper()
	d.run(d.m.Init())
}

func (d *driver) view() string {
	return d.m.View()
}

func githubRoutes(t *testing.T) *forge {
	return newForge(t, "/api/v3", func(method, path string) (int, string) {
		switch {
		case method == "GET" && path == "/user/repos":
			return http.StatusOK, `[{"name":"existing","full_name":"octocat/existing","owner":{"login":"octocat"},"private":true,"default_branch":"main","has_issues":true}]`
		case method == "GET" && path == "/repos/octocat/demo":
			return http.StatusNotFound, `{"message":"Not Found"}`
		case method == "POST" && path == "/user/repos":
			return http.StatusCreated, `{"name":"demo","full_name":"octocat/demo","owner":{"login":"octocat"},"private":true,"html_url":"https://github.com/octocat/demo"}`
		case method == "GET" && path == "/repos/octocat/demo/rulesets":
			return http.StatusOK, `[]`
		case method == "POST" && path == "/repos/octocat/demo/rulesets":
			return http.StatusCreated, `{"id":1}`
		case strings.HasPrefix(path, "/repos/octocat/demo/actions/permissions"):
			return http.StatusNoContent, ``
		}
		return http.StatusNotFound, `{"message":"unhandled"}`
	})
}

func gitlabRoutes(t *testing.T) *forge {
	return newForge(t, "/api/v4", func(method, path string) (int, string) {
		if method == "GET" && path == "/projects" {
			return http.StatusOK, `[{"id":9,"path":"svc","path_with_namespace":"team/svc","namespace":{"full_path":"team"},"visibility":"private","default_branch":"main","issues_enabled":true}]`
		}
		return http.StatusNotFound, `{"message":"404 Not Found"}`
	})
}

func TestLoadsBothProvidersAndOpensTheMenu(t *testing.T) {
	gh := githubRoutes(t)
	gl := gitlabRoutes(t)
	writeConfig(t, gh.server.URL, gl.server.URL)

	d := &driver{t: t, m: newApp()}
	d.start()

	if d.m.stage != stageMenu {
		t.Fatalf("stage = %v, want the menu after loading", d.m.stage)
	}
	if len(d.m.projects) != 2 {
		t.Fatalf("projects = %d, want one from each provider", len(d.m.projects))
	}
	if len(d.m.problems) != 0 {
		t.Errorf("problems = %v, want none", d.m.problems)
	}

	view := d.view()
	for _, want := range []string{logoLines[0], "Create a new repository", "Bulk sync settings (2)"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
}

func TestOneUnreachableProviderDoesNotHideTheOther(t *testing.T) {
	gh := githubRoutes(t)
	broken := newForge(t, "/api/v4", func(string, string) (int, string) {
		return http.StatusUnauthorized, `{"message":"401 Unauthorized"}`
	})
	writeConfig(t, gh.server.URL, broken.server.URL)

	d := &driver{t: t, m: newApp()}
	d.start()

	if d.m.stage != stageMenu {
		t.Fatalf("stage = %v, want the menu", d.m.stage)
	}
	if len(d.m.projects) != 1 {
		t.Errorf("projects = %d, want the working provider's repository", len(d.m.projects))
	}
	if len(d.m.problems) != 1 || !strings.Contains(d.m.problems[0], "token was rejected") {
		t.Errorf("problems = %v, want a readable reason for the failed provider", d.m.problems)
	}
}

func TestMissingConfigOpensTheSetupScreen(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GITLIFT_CONFIG", filepath.Join(dir, "absent.yaml"))
	t.Setenv("XDG_CONFIG_HOME", dir)

	d := &driver{t: t, m: newApp()}
	d.start()

	if d.m.stage != stageSetup {
		t.Fatalf("stage = %v, want the setup screen", d.m.stage)
	}

	view := d.view()
	if !strings.Contains(view, "No usable provider yet") {
		t.Errorf("view should explain the situation:\n%s", view)
	}
	if !strings.Contains(view, "write a starter config") {
		t.Errorf("view should offer a way out:\n%s", view)
	}
}

func TestCreateFlowAppliesTheWholeTemplate(t *testing.T) {
	gh := githubRoutes(t)
	gl := gitlabRoutes(t)
	writeConfig(t, gh.server.URL, gl.server.URL)

	d := &driver{t: t, m: newApp()}
	d.start()

	d.key("enter")
	if d.m.stage != stagePickProvider {
		t.Fatalf("stage = %v, want the provider picker", d.m.stage)
	}

	d.key("enter")
	if d.m.stage != stagePickTemplate {
		t.Fatalf("stage = %v, want the template picker", d.m.stage)
	}

	d.typeText("GitHub base")
	d.key("enter")
	if d.m.stage != stageForm {
		t.Fatalf("stage = %v, want the form", d.m.stage)
	}

	d.typeText("demo")
	d.key("down")
	d.typeText("octocat")
	d.key("ctrl+s")

	if d.m.stage != stageReview {
		t.Fatalf("stage = %v, want the review screen, form error = %q", d.m.stage, d.m.form.err)
	}

	summary := d.m.pane.body
	for _, want := range []string{"octocat", "demo", "Branch rules", "fork PR approval"} {
		if !strings.Contains(summary, want) {
			t.Errorf("review is missing %q:\n%s", want, summary)
		}
	}

	d.key("enter")

	if d.m.stage != stageReport {
		t.Fatalf("stage = %v, want the report", d.m.stage)
	}
	if !gh.sent("POST", "/user/repos") {
		t.Error("the repository was never created")
	}
	if !gh.sent("POST", "/repos/octocat/demo/rulesets") {
		t.Error("branch rules were never applied")
	}
	if !gh.sent("PUT", "/repos/octocat/demo/actions/permissions/workflow") {
		t.Error("actions settings were never applied")
	}

	report := d.m.pane.body
	if !strings.Contains(report, "octocat/demo") {
		t.Errorf("report should name the repository:\n%s", report)
	}
	if strings.Contains(d.m.pane.title, "failed") && !strings.Contains(d.m.pane.title, "0 failed") {
		t.Errorf("title = %q, want no failures", d.m.pane.title)
	}
}

func TestCreateFlowRefusesANameThatIsTaken(t *testing.T) {
	gh := newForge(t, "/api/v3", func(method, path string) (int, string) {
		switch {
		case method == "GET" && path == "/user/repos":
			return http.StatusOK, `[]`
		case method == "GET" && path == "/repos/octocat/demo":
			return http.StatusOK, `{"name":"demo","full_name":"octocat/demo","owner":{"login":"octocat"}}`
		}
		return http.StatusNotFound, `{"message":"unhandled"}`
	})
	gl := gitlabRoutes(t)
	writeConfig(t, gh.server.URL, gl.server.URL)

	d := &driver{t: t, m: newApp()}
	d.start()

	d.key("enter")
	d.key("enter")
	d.typeText("GitHub base")
	d.key("enter")

	d.typeText("demo")
	d.key("down")
	d.typeText("octocat")
	d.key("ctrl+s")

	if d.m.stage != stageForm {
		t.Fatalf("stage = %v, want to stay on the form", d.m.stage)
	}
	if !strings.Contains(d.m.form.err, "already exists") {
		t.Errorf("form error = %q, want it to say the name is taken", d.m.form.err)
	}
	if gh.sent("POST", "/user/repos") {
		t.Error("nothing should have been created")
	}
}

func TestEscapeWalksBackThroughTheCreateFlow(t *testing.T) {
	gh := githubRoutes(t)
	gl := gitlabRoutes(t)
	writeConfig(t, gh.server.URL, gl.server.URL)

	d := &driver{t: t, m: newApp()}
	d.start()

	d.key("enter")
	d.key("enter")
	d.typeText("GitHub base")
	d.key("enter")

	want := []stage{stagePickTemplate, stagePickProvider, stageMenu}
	for _, expected := range want {
		d.key("esc")
		if d.m.stage != expected {
			t.Fatalf("stage = %v, want %v", d.m.stage, expected)
		}
	}
}

func TestBulkFlowOnlyTouchesFeaturesRulesAndActions(t *testing.T) {
	gh := newForge(t, "/api/v3", func(method, path string) (int, string) {
		switch {
		case method == "GET" && path == "/user/repos":
			return http.StatusOK, `[{"name":"existing","full_name":"octocat/existing","owner":{"login":"octocat"},"private":true,"default_branch":"main"}]`
		case method == "PATCH" && path == "/repos/octocat/existing":
			return http.StatusOK, `{}`
		case method == "GET" && path == "/repos/octocat/existing/rulesets":
			return http.StatusOK, `[]`
		case method == "POST" && path == "/repos/octocat/existing/rulesets":
			return http.StatusCreated, `{"id":1}`
		case strings.HasPrefix(path, "/repos/octocat/existing/actions/permissions"):
			return http.StatusNoContent, ``
		}
		return http.StatusNotFound, `{"message":"unhandled"}`
	})
	gl := newForge(t, "/api/v4", func(string, string) (int, string) {
		return http.StatusOK, `[]`
	})
	writeConfig(t, gh.server.URL, gl.server.URL)

	d := &driver{t: t, m: newApp()}
	d.start()

	d.key("down")
	d.key("down")
	d.key("enter")
	if d.m.stage != stagePickProjects {
		t.Fatalf("stage = %v, want the multi-select picker", d.m.stage)
	}

	d.key("tab")
	d.key("enter")
	if d.m.stage != stagePickTemplate {
		t.Fatalf("stage = %v, want the template picker", d.m.stage)
	}

	d.typeText("GitHub base")
	d.key("enter")
	if d.m.stage != stagePickMode {
		t.Fatalf("stage = %v, want the append/replace picker", d.m.stage)
	}

	d.key("enter")
	if d.m.stage != stageForm {
		t.Fatalf("stage = %v, want the feature form before review", d.m.stage)
	}

	var toggles int
	for _, entry := range d.m.form.fields {
		if entry.kind == fieldToggle {
			toggles++
		}
	}
	if toggles == 0 {
		t.Fatalf("bulk should offer the template's feature toggles: %+v", d.m.form.fields)
	}

	d.key("ctrl+s")
	if d.m.stage != stageReview {
		t.Fatalf("stage = %v, want the review screen", d.m.stage)
	}
	if !strings.Contains(d.m.pane.body, "left untouched") {
		t.Errorf("review should state what bulk sync does not touch:\n%s", d.m.pane.body)
	}

	d.key("enter")
	if d.m.stage != stageReport {
		t.Fatalf("stage = %v, want the report", d.m.stage)
	}

	for _, request := range gh.requests() {
		if strings.HasPrefix(request, "PUT /repos/octocat/existing/actions/variables") ||
			strings.HasPrefix(request, "POST /repos/octocat/existing/actions/variables") {
			t.Errorf("bulk sync must not touch CI/CD variables, but sent %q", request)
		}
	}
	if !gh.sent("POST", "/repos/octocat/existing/rulesets") {
		t.Error("bulk sync should have applied the branch rules")
	}
}

func TestManageFlowPrefillsFromTheLiveRepository(t *testing.T) {
	gh := newForge(t, "/api/v3", func(method, path string) (int, string) {
		switch {
		case method == "GET" && path == "/user/repos":
			return http.StatusOK, `[{"name":"existing","full_name":"octocat/existing","owner":{"login":"octocat"},"private":true,"description":"live text","default_branch":"main","has_issues":false}]`
		}
		return http.StatusNotFound, `{"message":"unhandled"}`
	})
	gl := newForge(t, "/api/v4", func(string, string) (int, string) {
		return http.StatusOK, `[]`
	})
	writeConfig(t, gh.server.URL, gl.server.URL)

	d := &driver{t: t, m: newApp()}
	d.start()

	d.key("down")
	d.key("enter")
	if d.m.stage != stagePickProject {
		t.Fatalf("stage = %v, want the repository picker", d.m.stage)
	}

	d.key("enter")
	d.typeText("GitHub base")
	d.key("enter")
	if d.m.stage != stagePickMode {
		t.Fatalf("stage = %v, want the append/replace picker", d.m.stage)
	}

	d.key("enter")
	if d.m.stage != stageForm {
		t.Fatalf("stage = %v, want the form", d.m.stage)
	}

	view := d.m.form.view(100, 30)
	if !strings.Contains(view, "live text") {
		t.Errorf("the form should carry the live description over:\n%s", view)
	}
	if !strings.Contains(view, "currently: no") {
		t.Errorf("the form should flag where the repository differs from the template:\n%s", view)
	}
}

func TestEveryScreenRendersWithinTheTerminal(t *testing.T) {
	gh := githubRoutes(t)
	gl := gitlabRoutes(t)
	writeConfig(t, gh.server.URL, gl.server.URL)

	d := &driver{t: t, m: newApp()}
	d.m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	d.start()

	shot := func(name string) {
		view := d.view()
		lines := strings.Split(view, "\n")

		if len(lines) > 30 {
			t.Errorf("%s renders %d lines into a 30-line terminal", name, len(lines))
		}
		for i, line := range lines {
			if width := lineWidth(line); width > 100 {
				t.Errorf("%s line %d is %d columns wide, terminal is 100", name, i, width)
			}
		}
		if !strings.Contains(lines[0], "gitlift") && !strings.Contains(lines[0], logoLines[0]) {
			t.Errorf("%s should start with the header, got %q", name, truncate(lines[0], 40))
		}
		t.Logf("\n=== %s ===\n%s", name, view)
	}

	shot("menu")
	d.key("enter")
	shot("provider picker")
	d.key("enter")
	shot("template picker")
	d.typeText("GitHub base")
	d.key("enter")
	shot("create form")
	d.typeText("demo")
	d.key("down")
	d.typeText("octocat")
	d.key("ctrl+s")
	shot("review")
	d.key("enter")
	shot("report")
}

func TestATemplateWithNothingEditableSkipsTheForm(t *testing.T) {
	spec := tmpl.Spec{
		Name:        "rules only",
		BranchRules: &tmpl.BranchRules{Rules: []tmpl.Rule{{Name: "main", Patterns: []string{"main"}}}},
	}

	fields := settingsFields(spec, provider.Project{}, "gitlab", true)
	if len(fields) != 0 {
		t.Errorf("fields = %+v, want none for a template that declares no repo block", fields)
	}
}

func TestATemplateWithFeaturesGetsAForm(t *testing.T) {
	spec := tmpl.Spec{
		Name: "with features",
		Repo: &tmpl.Repo{Features: tmpl.NewFeatures(map[string]bool{"issues": true, "wiki": false})},
	}

	fields := settingsFields(spec, provider.Project{}, "gitlab", true)

	var toggles int
	for _, entry := range fields {
		if entry.kind == fieldToggle {
			toggles++
		}
	}
	if toggles != 2 {
		t.Errorf("toggles = %d, want one per declared feature", toggles)
	}
}

func TestOnlyGitLabSupportedFeaturesBecomeFields(t *testing.T) {
	spec := tmpl.Spec{
		Repo: &tmpl.Repo{Features: tmpl.NewFeatures(map[string]bool{"projects": true})},
	}

	if fields := settingsFields(spec, provider.Project{}, "gitlab", true); len(fields) != 0 {
		t.Errorf("fields = %+v, want none, projects is GitHub-only", fields)
	}
	if fields := settingsFields(spec, provider.Project{}, "github", true); len(fields) == 0 {
		t.Error("projects is a GitHub toggle and must get a field there")
	}
}

func TestEditIsNotOfferedWhenNoFormWasBuilt(t *testing.T) {
	m := newApp()
	m.formReady = false
	m.flow = flowManage
	m.stage = stageReview

	m.keyReview(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})

	if m.stage != stageReview {
		t.Errorf("stage = %v, want to stay on review when there is nothing to edit", m.stage)
	}
	if m.status == "" {
		t.Error("pressing e with no form should say why nothing happened")
	}
}

func TestReturningToTheMenuDropsAStaleForm(t *testing.T) {
	m := newApp()
	m.form = newForm("old", "", []field{{id: "visibility", label: "visibility", kind: fieldText, text: "public"}})
	m.formReady = true

	m.openMenu()

	if m.formReady {
		t.Error("formReady must not survive a return to the menu")
	}
	if m.form.has("visibility") {
		t.Error("a stale form must not be readable by the next flow")
	}
}

func TestBulkFormEditsReachEveryPlan(t *testing.T) {
	m := newApp()
	m.flow = flowBulk

	gl := &stubProvider{kind: "gitlab", name: "GitLab"}
	m.projects = []provider.Project{
		{Name: "one", FullName: "g/one", Kind: "gitlab"},
		{Name: "two", FullName: "g/two", Kind: "gitlab"},
	}
	m.owners = []provider.Provider{gl, gl}
	m.bulkTargets = []int{0, 1}
	m.bulkKinds = []string{"gitlab"}
	m.bulkTemplate = map[string]string{"gitlab": "t"}
	m.bulkSpecs = map[string]tmpl.Spec{"gitlab": {
		Name: "t",
		Repo: &tmpl.Repo{Features: tmpl.NewFeatures(map[string]bool{"issues": true, "wiki": true})},
	}}

	m.openBulkForm()
	if m.stage != stageForm {
		t.Fatalf("stage = %v, want a form for the bulk feature toggles", m.stage)
	}

	for i, entry := range m.form.fields {
		if entry.id == "feature:gitlab:wiki" {
			m.form.fields[i].on = false
		}
	}

	m.buildBulkPlans()

	if len(m.plans) != 2 {
		t.Fatalf("plans = %d, want one per selected repository", len(m.plans))
	}
	for _, plan := range m.plans {
		if on, _ := plan.Settings.Features.Get("wiki"); on {
			t.Errorf("%s: the form turned wiki off and the plan must follow", plan.Project.FullName)
		}
		if on, _ := plan.Settings.Features.Get("issues"); !on {
			t.Errorf("%s: issues was left on and must stay on", plan.Project.FullName)
		}
	}
}

func TestBulkFormIsSkippedWhenTheTemplateHasNoFeatures(t *testing.T) {
	m := newApp()
	m.flow = flowBulk

	gl := &stubProvider{kind: "gitlab", name: "GitLab"}
	m.projects = []provider.Project{{Name: "one", FullName: "g/one", Kind: "gitlab"}}
	m.owners = []provider.Provider{gl}
	m.bulkTargets = []int{0}
	m.bulkKinds = []string{"gitlab"}
	m.bulkTemplate = map[string]string{"gitlab": "t"}
	m.bulkSpecs = map[string]tmpl.Spec{"gitlab": {
		Name:        "t",
		BranchRules: &tmpl.BranchRules{Rules: []tmpl.Rule{{Name: "m", Patterns: []string{"main"}}}},
	}}

	m.openBulkForm()

	if m.stage != stageReview {
		t.Errorf("stage = %v, want review when there are no toggles to edit", m.stage)
	}
	if m.formReady {
		t.Error("no form was built, so review must not offer the edit key")
	}
}

type stubProvider struct {
	kind   string
	name   string
	spaces []provider.Namespace
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Kind() string { return s.kind }
func (s *stubProvider) Host() string { return "example.test" }

func (s *stubProvider) ListProjects(context.Context) ([]provider.Project, error) { return nil, nil }

func (s *stubProvider) Lookup(context.Context, provider.Target, string) (provider.Project, bool, error) {
	return provider.Project{}, false, nil
}

func (s *stubProvider) Create(context.Context, provider.Target, provider.Settings) (provider.Project, error) {
	return provider.Project{}, nil
}

func (s *stubProvider) Namespaces(context.Context) ([]provider.Namespace, error) {
	return s.spaces, nil
}

func (s *stubProvider) Capture(context.Context, provider.Project) (tmpl.Spec, []provider.Step) {
	return tmpl.Spec{}, nil
}

func (s *stubProvider) UpdateSettings(context.Context, provider.Project, provider.Settings) []provider.Step {
	return nil
}

func (s *stubProvider) ApplyBranchRules(context.Context, provider.Project, []tmpl.Rule, provider.RuleMode) []provider.Step {
	return nil
}

func (s *stubProvider) ApplyTagRules(context.Context, provider.Project, []tmpl.TagRule, provider.RuleMode) []provider.Step {
	return nil
}

func (s *stubProvider) ApplyMergeRequests(context.Context, provider.Project, *tmpl.MergeRequests) []provider.Step {
	return nil
}

func (s *stubProvider) ApplyActions(context.Context, provider.Project, *tmpl.Actions) []provider.Step {
	return nil
}

func (s *stubProvider) ApplyPipelines(context.Context, provider.Project, *tmpl.Pipelines) []provider.Step {
	return nil
}

func (s *stubProvider) ApplyWebhooks(context.Context, provider.Project, []tmpl.Webhook) []provider.Step {
	return nil
}

func (s *stubProvider) ApplyIntegrations(context.Context, provider.Project, []tmpl.Integration) []provider.Step {
	return nil
}

func (s *stubProvider) ApplyVariables(context.Context, provider.Project, []tmpl.Variable) []provider.Step {
	return nil
}

func TestAuthorFromScratchReachesTheForm(t *testing.T) {
	gh := newForge(t, "", func(string, string) (int, string) { return http.StatusOK, `[]` })
	gl := newForge(t, "/api/v4", func(string, string) (int, string) { return http.StatusOK, `[]` })
	writeConfig(t, gh.server.URL, gl.server.URL)

	d := &driver{t: t, m: newApp()}
	d.start()

	d.key("down")
	d.key("down")
	d.key("down")
	d.key("enter")
	if d.m.stage != stageAuthorSource {
		t.Fatalf("stage = %v, want the template source picker", d.m.stage)
	}

	d.key("down")
	d.key("enter")
	if d.m.stage != stageAuthorKind {
		t.Fatalf("stage = %v, want the provider kind picker", d.m.stage)
	}
	d.key("enter")
	if d.m.stage != stageAuthorForm {
		t.Fatalf("stage = %v, want the author form", d.m.stage)
	}

	view := d.view()
	if !strings.Contains(view, "New template: github") {
		t.Fatalf("the screen must show the form, not the picker it came from:\n%s", view)
	}
	if !strings.Contains(view, "ctrl+s continue") {
		t.Errorf("the footer must show the form keys:\n%s", view)
	}

	d.typeText("scratch base")
	d.key("ctrl+s")

	if d.m.stage != stageAuthorDone {
		t.Fatalf("stage = %v, want the written-template pane", d.m.stage)
	}

	done := d.view()
	if !strings.Contains(done, "scratch-base.yaml") {
		t.Errorf("the done screen must show where it was written:\n%s", done)
	}
	if !strings.Contains(done, "name: scratch base") {
		t.Errorf("the done screen must preview the template:\n%s", done)
	}
}

func TestEveryStageRendersItsOwnScreen(t *testing.T) {
	m := newApp()
	m.form = newForm("A FORM", "", []field{{id: "x", label: "x", kind: fieldText}})
	m.pane = newPane("A PANE", "pane body")
	m.list = newList("A LIST", []listItem{{title: "item", ref: 0}}, false)

	forms := []stage{stageForm, stageAuthorForm}
	panes := []stage{stageReview, stageReport, stageProviders, stageHelp, stageAuthorDone}
	lists := []stage{
		stageMenu, stagePickProvider, stagePickProject, stagePickProjects,
		stagePickTemplate, stagePickMode, stageAuthorSource, stageAuthorKind,
		stageAuthorPickRepo, stageAuthorPickTemplate,
	}

	check := func(stages []stage, want string) {
		for _, st := range stages {
			m.stage = st
			if body := m.body(); !strings.Contains(body, want) {
				t.Errorf("stage %d rendered the wrong widget, wanted %q in:\n%s", st, want, body)
			}
		}
	}

	check(forms, "A FORM")
	check(panes, "A PANE")
	check(lists, "A LIST")
}

func TestTemplatePickerShowsTheScope(t *testing.T) {
	m := newApp()
	m.templates = []tmpl.Template{
		{Name: "GitLab base", Scope: "builtin", Builtin: true},
		{Name: "team thing", Scope: "work"},
	}
	m.activeProvider = &stubProvider{kind: "gitlab", name: "GitLab"}

	m.openTemplatePicker("gitlab")

	var seen []string
	for _, item := range m.list.items {
		seen = append(seen, item.title+" => "+item.desc)
	}
	joined := strings.Join(seen, " | ")

	if !strings.Contains(joined, "builtin") {
		t.Errorf("items = %q, want the builtin scope shown", joined)
	}
	if !strings.Contains(joined, "work") {
		t.Errorf("items = %q, want the folder scope shown", joined)
	}
}

func TestAuthorFolderPrefillsFromAClonedTemplate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	m := newApp()
	m.templates = []tmpl.Template{{Name: "team thing", Scope: "work"}}

	m.authorScope = "work"
	m.authorKind = "gitlab"
	m.openAuthorForm()

	if got := m.form.get("folder"); got != "work" {
		t.Errorf("folder = %q, want it prefilled from the source template", got)
	}

	var note string
	for _, entry := range m.form.fields {
		if entry.id == "folder" {
			note = entry.note
		}
	}
	if !strings.Contains(note, "work") {
		t.Errorf("note = %q, want it to list the folders that already exist", note)
	}
}

func TestNamespaceFieldIsAChoiceWhenFetched(t *testing.T) {
	m := newApp()
	gl := &stubProvider{kind: "gitlab", name: "GitLab"}
	m.activeProvider = gl
	m.namespaces = map[string][]provider.Namespace{
		"GitLab": {
			{Path: "devuser", Name: "Dev User", Kind: "user"},
			{Path: "team", Kind: "group"},
			{Path: "team/sub", Kind: "group"},
		},
	}

	f := m.namespaceField("namespace", "team/sub")

	if f.kind != fieldChoice {
		t.Fatalf("kind = %v, want a left/right choice", f.kind)
	}
	if len(f.choices) != 3 {
		t.Errorf("choices = %v, want one per namespace", f.choices)
	}
	if f.choice != 2 {
		t.Errorf("choice = %d, want the template's namespace preselected", f.choice)
	}
	if !strings.Contains(f.choices[0], "Dev User") {
		t.Errorf("choices[0] = %q, want the display name alongside the path", f.choices[0])
	}
}

func TestNamespaceFieldFallsBackToTypingWhenNotFetched(t *testing.T) {
	m := newApp()
	m.activeProvider = &stubProvider{kind: "github", name: "GitHub"}

	f := m.namespaceField("owner", "acme")

	if f.kind != fieldText {
		t.Fatalf("kind = %v, want a text field when nothing was fetched", f.kind)
	}
	if f.text != "acme" {
		t.Errorf("text = %q, want the template value kept", f.text)
	}
}

func TestSelectedNamespaceCarriesTheOwnerType(t *testing.T) {
	m := newApp()
	gh := &stubProvider{kind: "github", name: "GitHub"}
	m.activeProvider = gh
	m.namespaces = map[string][]provider.Namespace{
		"GitHub": {{Path: "octocat", Kind: "user"}, {Path: "acme", Kind: "org"}},
	}

	m.form = newForm("x", "", []field{m.namespaceField("owner", "acme")})

	space := m.selectedNamespace()
	if space.Path != "acme" || space.Kind != "org" {
		t.Errorf("selected = %+v, want the org with its kind so ownertype is derived", space)
	}
}

func TestCreateFlowPicksNamespaceWithArrows(t *testing.T) {
	gh := newForge(t, "/api/v3", func(method, path string) (int, string) {
		switch {
		case path == "/user":
			return http.StatusOK, `{"login":"octocat","name":"Dev User"}`
		case path == "/user/orgs":
			return http.StatusOK, `[{"login":"acme"}]`
		case method == "GET" && path == "/repos/acme/demo":
			return http.StatusNotFound, `{"message":"Not Found"}`
		}
		return http.StatusOK, `[]`
	})
	gl := newForge(t, "/api/v4", func(string, string) (int, string) { return http.StatusOK, `[]` })
	writeConfig(t, gh.server.URL, gl.server.URL)

	d := &driver{t: t, m: newApp()}
	d.start()

	d.key("enter")
	d.typeText("GitHub")
	d.key("enter")
	d.typeText("GitHub base")
	d.key("enter")

	d.typeText("demo")
	d.key("down")

	owner := d.m.form.fields[d.m.form.cursor]
	if owner.id != "owner" {
		t.Fatalf("cursor is on %q, want the namespace field", owner.id)
	}
	if owner.kind != fieldChoice {
		t.Fatalf("namespace must be an arrow selection, got kind %v", owner.kind)
	}

	d.key("right")
	if got := d.m.form.get("owner"); got != "acme" {
		t.Fatalf("owner = %q, want the org after one right press", got)
	}

	d.key("ctrl+s")
	if d.m.stage != stageReview {
		t.Fatalf("stage = %v, want review, form error = %q", d.m.stage, d.m.form.err)
	}

	target := d.m.plans[0].Target
	if target.Owner != "acme" || target.OwnerType != "org" {
		t.Errorf("target = %+v, want the owner type derived from the chosen namespace", target)
	}

	var checked bool
	for _, request := range gh.requests() {
		if request == "GET /repos/acme/demo" {
			checked = true
		}
	}
	if !checked {
		t.Errorf("requests = %v, want the name checked against the chosen namespace", gh.requests())
	}
}
