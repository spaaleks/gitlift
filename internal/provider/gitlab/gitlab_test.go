package gitlab

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spaaleks/gitlift/internal/provider"
	"github.com/spaaleks/gitlift/internal/tmpl"
)

type route struct {
	status int
	body   string
}

func serve(t *testing.T, routes map[string]route, seen *[]string) *GitLab {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		key := r.Method + " " + r.URL.Path
		if seen != nil {
			*seen = append(*seen, key+" "+string(body))
		}

		match, ok := routes[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message":"404 Not Found"}`)
			return
		}

		w.WriteHeader(match.status)
		io.WriteString(w, match.body)
	}))
	t.Cleanup(server.Close)

	return New("test", "token", server.URL)
}

func target() provider.Project {
	return provider.Project{ID: "42", Owner: "group", Name: "demo", FullName: "group/demo", Visibility: "private"}
}

func statuses(steps []provider.Step) map[provider.Status]int {
	counts := map[provider.Status]int{}
	for _, step := range steps {
		counts[step.Status]++
	}
	return counts
}

func TestExistingProtectionIsPatchedRatherThanRecreated(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"GET /projects/42/protected_branches": {http.StatusOK, `[{
			"id": 1, "name": "main",
			"merge_access_levels": [{"id": 11, "access_level": 30}]
		}]`},
		"PATCH /projects/42/protected_branches/main": {http.StatusOK, `{"id":1}`},
	}, &seen)

	steps := client.ApplyBranchRules(context.Background(), target(),
		[]tmpl.Rule{{Name: "core", Patterns: []string{"main"}, MergeAccess: "maintainers"}},
		provider.ModeAppend)

	if statuses(steps)[provider.StatusFailed] != 0 {
		t.Fatalf("unexpected failures: %+v", steps)
	}

	for _, request := range seen {
		if strings.HasPrefix(request, "DELETE ") {
			t.Fatalf("the rule must not be deleted, but sent %q", request)
		}
	}

	var patched string
	for _, request := range seen {
		if strings.HasPrefix(request, "PATCH ") {
			patched = request
		}
	}
	if patched == "" {
		t.Fatalf("requests = %v, want a PATCH", seen)
	}
	if !strings.Contains(patched, `"access_level":40`) {
		t.Errorf("patch = %q, want it to add the maintainers level", patched)
	}
}

func TestProtectionThatAlreadyMatchesIsLeftAlone(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"GET /projects/42/protected_branches": {http.StatusOK, `[{
			"id": 1, "name": "main", "allow_force_push": false,
			"merge_access_levels": [{"id": 11, "access_level": 40}]
		}]`},
	}, &seen)

	steps := client.ApplyBranchRules(context.Background(), target(),
		[]tmpl.Rule{{
			Name: "core", Patterns: []string{"main"},
			MergeAccess: "maintainers", AllowForcePush: provider.Bool(false),
		}}, provider.ModeAppend)

	if len(steps) != 1 || steps[0].Status != provider.StatusOK {
		t.Fatalf("steps = %+v, want a single ok", steps)
	}
	if len(seen) != 1 {
		t.Errorf("requests = %v, want only the read, nothing needed changing", seen)
	}
}

func TestNamedAllowancesSurviveAPatch(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"GET /projects/42/protected_branches": {http.StatusOK, `[{
			"id": 1, "name": "main",
			"merge_access_levels": [
				{"id": 11, "access_level": 30},
				{"id": 12, "access_level": 40, "user_id": 7},
				{"id": 13, "access_level": 40, "group_id": 9}
			]
		}]`},
		"PATCH /projects/42/protected_branches/main": {http.StatusOK, `{"id":1}`},
	}, &seen)

	client.ApplyBranchRules(context.Background(), target(),
		[]tmpl.Rule{{Name: "core", Patterns: []string{"main"}, MergeAccess: "maintainers"}},
		provider.ModeAppend)

	var patched string
	for _, request := range seen {
		if strings.HasPrefix(request, "PATCH ") {
			patched = request
		}
	}

	if !strings.Contains(patched, `{"_destroy":true,"id":11}`) {
		t.Errorf("patch = %q, want the stale role row removed", patched)
	}
	for _, id := range []string{`"id":12`, `"id":13`} {
		if strings.Contains(patched, id) {
			t.Errorf("patch = %q, want the user and group rows left alone (%s)", patched, id)
		}
	}
}

func TestRecreatingAfterAFailedPatchReportsLostAllowances(t *testing.T) {
	client := serve(t, map[string]route{
		"GET /projects/42/protected_branches": {http.StatusOK, `[{
			"id": 1, "name": "main",
			"merge_access_levels": [{"id": 11, "access_level": 30}, {"id": 12, "access_level": 40, "user_id": 7}]
		}]`},
		"PATCH /projects/42/protected_branches/main":  {http.StatusNotFound, `{"message":"404 Not Found"}`},
		"DELETE /projects/42/protected_branches/main": {http.StatusNoContent, ``},
		"POST /projects/42/protected_branches":        {http.StatusCreated, `{"id":9}`},
	}, nil)

	steps := client.ApplyBranchRules(context.Background(), target(),
		[]tmpl.Rule{{Name: "core", Patterns: []string{"main"}, MergeAccess: "maintainers"}},
		provider.ModeAppend)

	if len(steps) != 1 {
		t.Fatalf("steps = %+v, want one", steps)
	}
	if steps[0].Status != provider.StatusSkipped {
		t.Errorf("status = %v, want a skip so the loss is visible", steps[0].Status)
	}
	if !strings.Contains(steps[0].Detail, "lost") {
		t.Errorf("detail = %q, want it to say the allowances were lost", steps[0].Detail)
	}
}

func TestPushAccessIsNotInferredFromMergeAccess(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"GET /projects/42/protected_branches":  {http.StatusOK, `[]`},
		"POST /projects/42/protected_branches": {http.StatusCreated, `{"id":9}`},
	}, &seen)

	client.ApplyBranchRules(context.Background(), target(),
		[]tmpl.Rule{{Name: "core", Patterns: []string{"main"}, MergeAccess: "developers"}},
		provider.ModeAppend)

	var created string
	for _, request := range seen {
		if strings.HasPrefix(request, "POST ") {
			created = request
		}
	}
	if !strings.Contains(created, `"merge_access_level":30`) {
		t.Errorf("body = %q, want the declared merge level", created)
	}
	if strings.Contains(created, "push_access_level") {
		t.Errorf("body = %q, want no push level, the template did not declare one", created)
	}
}

func TestPushAccessIsSentWhenDeclared(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"GET /projects/42/protected_branches":  {http.StatusOK, `[]`},
		"POST /projects/42/protected_branches": {http.StatusCreated, `{"id":9}`},
	}, &seen)

	client.ApplyBranchRules(context.Background(), target(),
		[]tmpl.Rule{{
			Name: "core", Patterns: []string{"main"},
			PushAccess: "no_one", MergeAccess: "maintainers", UnprotectAccess: "admins",
		}}, provider.ModeAppend)

	created := seen[len(seen)-1]
	for _, want := range []string{`"push_access_level":0`, `"merge_access_level":40`, `"unprotect_access_level":60`} {
		if !strings.Contains(created, want) {
			t.Errorf("body = %q, want %s", created, want)
		}
	}
}

func TestReplaceModeUnprotectsBranchesTheTemplateDoesNotDeclare(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"GET /projects/42/protected_branches":           {http.StatusOK, `[{"id":1,"name":"legacy"}]`},
		"DELETE /projects/42/protected_branches/legacy": {http.StatusNoContent, ``},
		"POST /projects/42/protected_branches":          {http.StatusCreated, `{"id":9}`},
	}, &seen)

	steps := client.ApplyBranchRules(context.Background(), target(),
		[]tmpl.Rule{{Name: "core", Patterns: []string{"main"}}}, provider.ModeReplace)

	if statuses(steps)[provider.StatusFailed] != 0 {
		t.Fatalf("unexpected failures: %+v", steps)
	}

	var unprotected bool
	for _, request := range seen {
		if strings.HasPrefix(request, "DELETE /projects/42/protected_branches/legacy") {
			unprotected = true
		}
	}
	if !unprotected {
		t.Error("replace mode must unprotect a branch the template does not declare")
	}
}

func TestInsufficientRightsAreSkippedNotFailed(t *testing.T) {
	client := serve(t, map[string]route{
		"GET /projects/42/protected_branches":  {http.StatusOK, `[]`},
		"POST /projects/42/protected_branches": {http.StatusForbidden, `{"message":"403 Forbidden"}`},
	}, nil)

	steps := client.ApplyBranchRules(context.Background(), target(),
		[]tmpl.Rule{{Name: "core", Patterns: []string{"main"}}}, provider.ModeAppend)

	if statuses(steps)[provider.StatusFailed] != 0 {
		t.Fatalf("a permissions problem must be reported as skipped: %+v", steps)
	}
	if !strings.Contains(steps[0].Detail, "Maintainer") {
		t.Errorf("detail = %q, want it to name the access level needed", steps[0].Detail)
	}
}

func TestMergeMethodsAreReportedAsIgnored(t *testing.T) {
	client := serve(t, map[string]route{
		"GET /projects/42/protected_branches":  {http.StatusOK, `[]`},
		"POST /projects/42/protected_branches": {http.StatusCreated, `{"id":9}`},
	}, nil)

	steps := client.ApplyBranchRules(context.Background(), target(),
		[]tmpl.Rule{{Name: "main", Patterns: []string{"main"}, MergeMethods: []string{"merge"}}},
		provider.ModeAppend)

	var noted bool
	for _, step := range steps {
		if step.Status == provider.StatusInfo && strings.Contains(step.Detail, "project-wide") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("a GitHub-only key must be reported as ignored, got %+v", steps)
	}
}

func TestVariablesFallBackToUpdateWhenTheyAlreadyExist(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"GET /projects/42/variables":       {http.StatusOK, `[]`},
		"POST /projects/42/variables":      {http.StatusBadRequest, `{"message":{"key":["has already been taken"]}}`},
		"PUT /projects/42/variables/TOKEN": {http.StatusOK, `{}`},
	}, &seen)

	steps := client.ApplyVariables(context.Background(), target(),
		[]tmpl.Variable{{Key: "TOKEN", Value: "v", Masked: provider.Bool(true)}})

	if len(steps) != 1 || steps[0].Status != provider.StatusOK {
		t.Fatalf("steps = %+v, want the existing variable to be updated", steps)
	}

	var methods []string
	for _, request := range seen {
		methods = append(methods, strings.Fields(request)[0])
	}
	if strings.Join(methods, ",") != "GET,POST,PUT" {
		t.Errorf("requests = %v, want a read, a create attempt, then an update", methods)
	}
}

func TestVariablesAreScopedToTheirEnvironment(t *testing.T) {
	var raw []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw = append(raw, r.Method+" "+r.RequestURI)
		if r.Method == http.MethodGet {
			io.WriteString(w, `[{"key":"TOKEN","environment_scope":"production"}]`)
			return
		}
		io.WriteString(w, `{}`)
	}))
	defer server.Close()

	client := New("test", "token", server.URL)
	steps := client.ApplyVariables(context.Background(), target(),
		[]tmpl.Variable{{Key: "TOKEN", Value: "v", EnvironmentScope: "production"}})

	if len(steps) != 1 || steps[0].Status != provider.StatusOK {
		t.Fatalf("steps = %+v", steps)
	}

	last := raw[len(raw)-1]
	if !strings.HasPrefix(last, "PUT ") {
		t.Fatalf("requests = %v, want the known variable to be updated directly", raw)
	}
	if !strings.Contains(last, "filter%5Benvironment_scope%5D=production") {
		t.Errorf("request = %q, want the environment scope in the filter", last)
	}
}

func TestWebhooksAreKeyedByURL(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"GET /projects/42/hooks": {http.StatusOK,
			`[{"id":3,"url":"https://example.test/hook"}]`},
		"PUT /projects/42/hooks/3": {http.StatusOK, `{}`},
	}, &seen)

	steps := client.ApplyWebhooks(context.Background(), target(), []tmpl.Webhook{{
		URL:    "https://example.test/hook",
		Events: []string{"push", "merge_request"},
		Secret: "s3cret",
	}})

	if len(steps) != 1 || steps[0].Status != provider.StatusOK {
		t.Fatalf("steps = %+v, want the existing hook updated", steps)
	}

	body := seen[len(seen)-1]
	if !strings.Contains(body, `"push_events":true`) || !strings.Contains(body, `"merge_requests_events":true`) {
		t.Errorf("body = %q, want the declared events on", body)
	}
	if !strings.Contains(body, `"pipeline_events":false`) {
		t.Errorf("body = %q, want undeclared events explicitly off", body)
	}
	if !strings.Contains(body, `"token":"s3cret"`) {
		t.Errorf("body = %q, want the secret sent", body)
	}
}

func TestMergeRequestSettingsMapOntoTheProjectEndpoint(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"PUT /projects/42": {http.StatusOK, `{}`},
	}, &seen)

	steps := client.ApplyMergeRequests(context.Background(), target(), &tmpl.MergeRequests{
		MergeMethod:         provider.String("fast_forward"),
		Squash:              provider.String("default_on"),
		PipelineMustSucceed: provider.Bool(true),
		DeleteSourceBranch:  provider.Bool(true),
	})

	if statuses(steps)[provider.StatusFailed] != 0 {
		t.Fatalf("unexpected failures: %+v", steps)
	}

	body := seen[0]
	for _, want := range []string{
		`"merge_method":"ff"`,
		`"squash_option":"default_on"`,
		`"only_allow_merge_if_pipeline_succeeds":true`,
		`"remove_source_branch_after_merge":true`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body = %q, want %s", body, want)
		}
	}
}

func TestApprovalSettingsAreSkippedWithoutPremium(t *testing.T) {
	client := serve(t, map[string]route{
		"POST /projects/42/approvals": {http.StatusPaymentRequired, `{"message":"402"}`},
	}, nil)

	steps := client.ApplyMergeRequests(context.Background(), target(), &tmpl.MergeRequests{
		ResetApprovalsOnPush: provider.Bool(true),
	})

	if len(steps) != 1 || steps[0].Status != provider.StatusSkipped {
		t.Fatalf("steps = %+v, want a skip rather than a failure", steps)
	}
	if !strings.Contains(steps[0].Detail, "Premium") {
		t.Errorf("detail = %q, want it to name the reason", steps[0].Detail)
	}
}

func TestPipelineTimeoutAcceptsADuration(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"PUT /projects/42": {http.StatusOK, `{}`},
	}, &seen)

	steps := client.ApplyPipelines(context.Background(), target(), &tmpl.Pipelines{
		Timeout:     provider.String("1h30m"),
		GitStrategy: provider.String("fetch"),
	})

	if statuses(steps)[provider.StatusFailed] != 0 {
		t.Fatalf("unexpected failures: %+v", steps)
	}
	if !strings.Contains(seen[0], `"build_timeout":5400`) {
		t.Errorf("body = %q, want the duration in seconds", seen[0])
	}
}

func TestProtectedTagsAreLeftAloneWhenTheyMatch(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"GET /projects/42/protected_tags": {http.StatusOK,
			`[{"name":"v*","create_access_levels":[{"id":1,"access_level":40}]}]`},
	}, &seen)

	steps := client.ApplyTagRules(context.Background(), target(),
		[]tmpl.TagRule{{Name: "releases", Patterns: []string{"v*"}, CreateAccess: "maintainers"}},
		provider.ModeAppend)

	if len(steps) != 1 || steps[0].Status != provider.StatusOK {
		t.Fatalf("steps = %+v", steps)
	}
	if len(seen) != 1 {
		t.Errorf("requests = %v, want only the read", seen)
	}
}

func TestUnsupportedFeaturesAreReportedNotSent(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"PUT /projects/42": {http.StatusOK, `{}`},
	}, &seen)

	steps := client.UpdateSettings(context.Background(), target(), provider.Settings{
		Name:     "demo",
		Features: tmpl.NewFeatures(map[string]bool{"issues": true, "projects": true}),
	})

	var noted bool
	for _, step := range steps {
		if step.Status == provider.StatusInfo && strings.Contains(step.Label, "projects") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("steps = %+v, want the GitHub-only toggle reported", steps)
	}
	body := seen[0][strings.Index(seen[0], "{"):]
	if strings.Contains(body, "projects") {
		t.Errorf("body = %q, want no GitHub-only key on the wire", body)
	}
}

func TestNamespaceFallsBackToTheGroupsEndpoint(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"GET /groups/team/sub": {http.StatusOK, `{"id":5}`},
		"POST /projects":       {http.StatusCreated, `{"id":7,"path":"demo","path_with_namespace":"team/sub/demo"}`},
	}, &seen)

	created, err := client.Create(context.Background(), provider.Target{Owner: "team/sub"},
		provider.Settings{Name: "demo", DisplayName: "Demo"})
	if err != nil {
		t.Fatal(err)
	}
	if created.FullName != "team/sub/demo" {
		t.Errorf("full name = %q", created.FullName)
	}

	if len(seen) < 2 || !strings.HasPrefix(seen[0], "GET /namespaces/team/sub") {
		t.Errorf("requests = %v, want /namespaces to be tried before /groups", seen)
	}
}

func TestNestedNamespacesAreURLEncodedOnTheWire(t *testing.T) {
	var raw []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw = append(raw, r.RequestURI)
		io.WriteString(w, `{"id":5}`)
	}))
	defer server.Close()

	client := New("test", "token", server.URL)
	if _, err := client.namespaceID(context.Background(), "team/sub"); err != nil {
		t.Fatal(err)
	}

	if len(raw) == 0 || !strings.Contains(raw[0], "team%2Fsub") {
		t.Errorf("request = %v, want the nested path encoded as team%%2Fsub", raw)
	}
}

func TestUpdateSettingsOnlySendsDeclaredKeys(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"PUT /projects/42": {http.StatusOK, `{}`},
	}, &seen)

	client.UpdateSettings(context.Background(), target(), provider.Settings{
		Name:     "demo",
		Features: tmpl.NewFeatures(map[string]bool{"wiki": false}),
	})

	var wrote string
	for _, request := range seen {
		if strings.HasPrefix(request, "PUT ") {
			wrote = request
		}
	}
	if wrote == "" {
		t.Fatalf("requests = %v, want a write", seen)
	}
	if !strings.Contains(wrote, "wiki_access_level") {
		t.Error("wiki was declared and must be sent")
	}
	for _, key := range []string{"issues_access_level", "description", "visibility"} {
		if strings.Contains(wrote, key) {
			t.Errorf("%s was not declared by the template and must not be sent", key)
		}
	}
}

func TestListProjectsSurfacesABadToken(t *testing.T) {
	client := serve(t, map[string]route{
		"GET /projects": {http.StatusUnauthorized, `{"message":"401 Unauthorized"}`},
	}, nil)

	_, err := client.ListProjects(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(provider.Explain(err), "token was rejected") {
		t.Errorf("Explain(%v) = %q, want a readable reason", err, provider.Explain(err))
	}
}

func TestEmptyValuesLeaveTheExistingVariableAlone(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"POST /projects/42/variables": {http.StatusCreated, `{}`},
	}, &seen)

	steps := client.ApplyVariables(context.Background(), target(), []tmpl.Variable{
		{Key: "BLANK", Value: "", Masked: provider.Bool(true)},
		{Key: "KEPT", Value: "real"},
	})

	if statuses(steps)[provider.StatusSkipped] != 1 {
		t.Errorf("the blank entry should be skipped: %+v", steps)
	}
	for _, request := range seen {
		if strings.Contains(request, "BLANK") {
			t.Errorf("a blank value must never be written, but sent %q", request)
		}
	}
}

func TestAdminsMapsToALevelGitLabAccepts(t *testing.T) {
	valid := map[int]bool{0: true, 30: true, 40: true, 60: true}
	for _, name := range tmpl.AccessLevels() {
		level, ok := provider.AccessLevel(name)
		if !ok {
			t.Fatalf("%q is offered by the template but has no level", name)
		}
		if !valid[level] {
			t.Errorf("%q = %d, which protected branches reject (accepts 0, 30, 40, 60)", name, level)
		}
	}
}

func TestARejectedPatchNeverDeletesTheRule(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"GET /projects/42/protected_branches": {http.StatusOK, `[{
			"id": 1, "name": "main",
			"merge_access_levels": [{"id": 11, "access_level": 30}]
		}]`},
		"PATCH /projects/42/protected_branches/main": {http.StatusBadRequest,
			`{"message":"allowed_to_merge[access_level] does not have a valid value"}`},
	}, &seen)

	steps := client.ApplyBranchRules(context.Background(), target(),
		[]tmpl.Rule{{Name: "core", Patterns: []string{"main"}, MergeAccess: "maintainers"}},
		provider.ModeAppend)

	for _, request := range seen {
		if strings.HasPrefix(request, "DELETE ") {
			t.Fatalf("a malformed request must not destroy protection, but sent %q", request)
		}
	}
	if len(steps) != 1 || steps[0].Status != provider.StatusFailed {
		t.Errorf("steps = %+v, want a plain failure with the rule left in place", steps)
	}
}

func TestAFailedRecreateRestoresThePreviousRule(t *testing.T) {
	var seen []string
	posts := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.Path+" "+string(body))

		switch {
		case r.Method == http.MethodGet:
			io.WriteString(w, `[{
				"id": 1, "name": "main", "allow_force_push": false,
				"push_access_levels":  [{"id": 10, "access_level": 40}],
				"merge_access_levels": [{"id": 11, "access_level": 30}]
			}]`)
		case r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message":"404 Not Found"}`)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost:
			posts++
			if posts == 1 {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"message":"unprotect_access_level does not have a valid value"}`)
				return
			}
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":1}`)
		}
	}))
	defer server.Close()

	client := New("test", "token", server.URL)
	steps := client.ApplyBranchRules(context.Background(), target(),
		[]tmpl.Rule{{Name: "core", Patterns: []string{"main"}, MergeAccess: "maintainers"}},
		provider.ModeAppend)

	if posts != 2 {
		t.Fatalf("posts = %d, want the failed create followed by a restore", posts)
	}

	restore := seen[len(seen)-1]
	for _, want := range []string{`"merge_access_level":30`, `"push_access_level":40`} {
		if !strings.Contains(restore, want) {
			t.Errorf("restore = %q, want the original %s", restore, want)
		}
	}
	if len(steps) != 1 || steps[0].Status != provider.StatusFailed {
		t.Fatalf("steps = %+v, want a failure", steps)
	}
	if !strings.Contains(steps[0].Detail, "restored") {
		t.Errorf("detail = %q, want it to say the previous rule came back", steps[0].Detail)
	}
}

func TestReplaceModeKeepsARuleThatFailedToApply(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"GET /projects/42/protected_branches": {http.StatusOK, `[
			{"id": 1, "name": "main", "merge_access_levels": [{"id": 11, "access_level": 30}]},
			{"id": 2, "name": "legacy"}
		]`},
		"PATCH /projects/42/protected_branches/main":    {http.StatusBadRequest, `{"message":"bad"}`},
		"DELETE /projects/42/protected_branches/legacy": {http.StatusNoContent, ``},
	}, &seen)

	client.ApplyBranchRules(context.Background(), target(),
		[]tmpl.Rule{{Name: "core", Patterns: []string{"main"}, MergeAccess: "maintainers"}},
		provider.ModeReplace)

	for _, request := range seen {
		if strings.HasPrefix(request, "DELETE /projects/42/protected_branches/main") {
			t.Error("replace must not unprotect a branch the template declares, even when its update failed")
		}
	}

	var sweptLegacy bool
	for _, request := range seen {
		if strings.HasPrefix(request, "DELETE /projects/42/protected_branches/legacy") {
			sweptLegacy = true
		}
	}
	if !sweptLegacy {
		t.Error("replace must still remove a rule the template does not declare")
	}
}

func TestTogglesTheInstanceIgnoresAreReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"id":42,
				"issues_access_level":"enabled",
				"container_registry_access_level":"enabled"}`)
			return
		}
		io.WriteString(w, `{}`)
	}))
	defer server.Close()

	client := New("test", "token", server.URL)
	steps := client.UpdateSettings(context.Background(), target(), provider.Settings{
		Name: "demo",
		Features: tmpl.NewFeatures(map[string]bool{
			"issues":             true,
			"container_registry": false,
			"pages":              false,
		}),
	})

	var warned provider.Step
	for _, step := range steps {
		if step.Status == provider.StatusSkipped {
			warned = step
		}
	}
	if warned.Label == "" {
		t.Fatalf("steps = %+v, want a warning about ignored toggles", steps)
	}

	joined := strings.Join(warned.Items, " | ")
	if !strings.Contains(joined, "container_registry (asked false, still true)") {
		t.Errorf("items = %q, want the toggle that did not take", joined)
	}
	if !strings.Contains(joined, "pages (not reported") {
		t.Errorf("items = %q, want the toggle this GitLab does not report", joined)
	}
	if strings.Contains(joined, "issues") {
		t.Errorf("items = %q, want a toggle that did apply left out", joined)
	}
}

func TestVerificationIsSkippedWhenNoFeaturesAreDeclared(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"PUT /projects/42": {http.StatusOK, `{}`},
	}, &seen)

	client.UpdateSettings(context.Background(), target(), provider.Settings{
		Name:        "demo",
		Description: provider.String("changed"),
	})

	for _, request := range seen {
		if strings.HasPrefix(request, "GET ") {
			t.Errorf("no features were declared, so nothing needs verifying: %q", request)
		}
	}
}

func TestGraphQLURLIsDerivedFromTheRESTBase(t *testing.T) {
	cases := map[string]string{
		"https://gitlab.example.com/api/v4":  "https://gitlab.example.com/api/graphql",
		"https://gitlab.example.com/api/v4/": "https://gitlab.example.com/api/graphql",
		"https://gitlab.com/api/v4":          "https://gitlab.com/api/graphql",
	}
	for input, want := range cases {
		if got := graphqlURL(input); got != want {
			t.Errorf("graphqlURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDuoGoesThroughGraphQLNotTheProjectPayload(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.Path+" "+string(body))

		if r.URL.Path == "/api/graphql" {
			io.WriteString(w, `{"data":{"projectSettingsUpdate":{"errors":[],
				"projectSettings":{"duoFeaturesEnabled":false}}}}`)
			return
		}
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"id":42,"issues_access_level":"enabled"}`)
			return
		}
		io.WriteString(w, `{}`)
	}))
	defer server.Close()

	client := New("test", "token", server.URL+"/api/v4")
	target := provider.Project{ID: "42", FullName: "group/demo"}

	steps := client.UpdateSettings(context.Background(), target, provider.Settings{
		Name:     "demo",
		Features: tmpl.NewFeatures(map[string]bool{"duo": false, "issues": true}),
	})

	var mutation, put string
	for _, request := range seen {
		if strings.Contains(request, "/api/graphql") {
			mutation = request
		}
		if strings.HasPrefix(request, "PUT ") {
			put = request
		}
	}

	if mutation == "" {
		t.Fatalf("requests = %v, want a GraphQL mutation", seen)
	}
	if !strings.Contains(mutation, "projectSettingsUpdate") ||
		!strings.Contains(mutation, "duoFeaturesEnabled") ||
		!strings.Contains(mutation, `"fullPath":"group/demo"`) {
		t.Errorf("mutation = %q, want the project settings mutation", mutation)
	}
	if strings.Contains(put, "duo") {
		t.Errorf("PUT = %q, want duo kept off the REST payload", put)
	}

	var ok bool
	for _, step := range steps {
		if step.Status == provider.StatusOK && strings.Contains(step.Label, "GitLab Duo") {
			ok = true
		}
	}
	if !ok {
		t.Errorf("steps = %+v, want the Duo change reported", steps)
	}
}

func TestDuoReportsAGraphQLRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/graphql" {
			io.WriteString(w, `{"errors":[{"message":"Field 'duoFeaturesEnabled' doesn't exist on type 'ProjectSettingsUpdateInput'"}]}`)
			return
		}
		io.WriteString(w, `{}`)
	}))
	defer server.Close()

	client := New("test", "token", server.URL+"/api/v4")
	step := client.applyDuo(context.Background(),
		provider.Project{ID: "42", FullName: "group/demo"}, false)

	if step.Status != provider.StatusSkipped {
		t.Fatalf("status = %v, want a skip", step.Status)
	}
	if !strings.Contains(step.Detail, "doesn't exist") {
		t.Errorf("detail = %q, want the GraphQL error passed through verbatim", step.Detail)
	}
}

func TestDuoReportsWhenAPolicyLocksIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":{"projectSettingsUpdate":{"errors":[],
			"projectSettings":{"duoFeaturesEnabled":true}}}}`)
	}))
	defer server.Close()

	client := New("test", "token", server.URL+"/api/v4")
	step := client.applyDuo(context.Background(),
		provider.Project{ID: "42", FullName: "group/demo"}, false)

	if step.Status != provider.StatusSkipped {
		t.Fatalf("status = %v, want a skip when the value did not change", step.Status)
	}
	if !strings.Contains(step.Detail, "still true") {
		t.Errorf("detail = %q, want it to say the setting did not move", step.Detail)
	}
}

func duoServer(t *testing.T, diagnosis string) *GitLab {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "userPermissions") {
			io.WriteString(w, diagnosis)
			return
		}
		io.WriteString(w, `{"errors":[{"message":"The resource that you are attempting to access does not exist or you don't have permission to perform this action"}]}`)
	}))
	t.Cleanup(server.Close)

	return New("test", "token", server.URL+"/api/v4")
}

func TestDuoNamesABotTokenWithoutAdminRights(t *testing.T) {
	client := duoServer(t, `{"data":{
		"currentUser":{"username":"project_42_bot","bot":true},
		"project":{"id":"gid://gitlab/Project/42","userPermissions":{"adminProject":false}}}}`)

	step := client.applyDuo(context.Background(),
		provider.Project{ID: "42", FullName: "group/demo"}, false)

	if step.Status != provider.StatusSkipped {
		t.Fatalf("status = %v, want a skip", step.Status)
	}
	if !strings.Contains(step.Detail, "Owner-level") {
		t.Errorf("detail = %q, want it to name the missing right", step.Detail)
	}
	joined := strings.Join(step.Items, " | ")
	if !strings.Contains(joined, "project_42_bot (a bot user, not a person)") {
		t.Errorf("items = %q, want the token identity", joined)
	}
	if !strings.Contains(joined, "adminProject permission: false") {
		t.Errorf("items = %q, want the permission it checked", joined)
	}
}

func TestDuoNamesAnUnresolvablePath(t *testing.T) {
	client := duoServer(t, `{"data":{"currentUser":{"username":"devuser","bot":false},"project":null}}`)

	step := client.applyDuo(context.Background(),
		provider.Project{ID: "42", FullName: "wrong/path"}, false)

	if !strings.Contains(step.Detail, "cannot see wrong/path") {
		t.Errorf("detail = %q, want it to name the path that failed", step.Detail)
	}
}

func TestDuoPointsAtAPolicyWhenRightsAreFine(t *testing.T) {
	client := duoServer(t, `{"data":{
		"currentUser":{"username":"devuser","bot":false},
		"project":{"id":"gid://gitlab/Project/42","userPermissions":{"adminProject":true}}}}`)

	step := client.applyDuo(context.Background(),
		provider.Project{ID: "42", FullName: "group/demo"}, false)

	if !strings.Contains(step.Detail, "policy is locking") {
		t.Errorf("detail = %q, want it to blame a policy when the rights are there", step.Detail)
	}
}

func TestNamespacesListsSelfThenGroups(t *testing.T) {
	client := serve(t, map[string]route{
		"GET /user":   {http.StatusOK, `{"username":"devuser","name":"Dev User"}`},
		"GET /groups": {http.StatusOK, `[{"full_path":"team","name":"Team"},{"full_path":"team/sub","name":"Sub"}]`},
	}, nil)

	spaces, err := client.Namespaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(spaces) != 3 {
		t.Fatalf("spaces = %+v, want the user plus two groups", spaces)
	}
	if spaces[0].Path != "devuser" || spaces[0].Kind != "user" {
		t.Errorf("first entry = %+v, want the personal namespace first", spaces[0])
	}
	if spaces[2].Path != "team/sub" || spaces[2].Kind != "group" {
		t.Errorf("nested group = %+v", spaces[2])
	}
}

func TestNamespacesSurviveAGroupsFailure(t *testing.T) {
	client := serve(t, map[string]route{
		"GET /user": {http.StatusOK, `{"username":"devuser"}`},
	}, nil)

	spaces, err := client.Namespaces(context.Background())
	if err != nil {
		t.Fatalf("a groups failure must not lose the personal namespace: %v", err)
	}
	if len(spaces) != 1 || spaces[0].Path != "devuser" {
		t.Errorf("spaces = %+v, want just the personal namespace", spaces)
	}
}
