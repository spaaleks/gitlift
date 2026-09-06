package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/spaaleks/gitlift/internal/provider"
	"github.com/spaaleks/gitlift/internal/tmpl"
)

type route struct {
	status int
	body   string
}

func serve(t *testing.T, routes map[string]route, seen *[]string) *GitHub {
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
			io.WriteString(w, `{"message":"no route in test"}`)
			return
		}

		w.WriteHeader(match.status)
		io.WriteString(w, match.body)
	}))
	t.Cleanup(server.Close)

	return New("test", "token", server.URL)
}

func target() provider.Project {
	return provider.Project{Owner: "octocat", Name: "demo", FullName: "octocat/demo", Visibility: "private"}
}

func statuses(steps []provider.Step) map[provider.Status]int {
	counts := map[provider.Status]int{}
	for _, step := range steps {
		counts[step.Status]++
	}
	return counts
}

func TestBuildRulesetPrefixesBareBranchNames(t *testing.T) {
	built := buildRuleset(tmpl.Rule{Name: "main", Patterns: []string{"main", "refs/heads/dev"}})

	want := []string{"refs/heads/main", "refs/heads/dev"}
	got := built.Conditions.RefName.Include
	if len(got) != len(want) {
		t.Fatalf("include = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("include[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildRulesetOmitsNonFastForwardWhenForcePushIsAllowed(t *testing.T) {
	built := buildRuleset(tmpl.Rule{Name: "dev", Patterns: []string{"dev"}, AllowForcePush: provider.Bool(true)})

	for _, rule := range built.Rules {
		if rule["type"] == "non_fast_forward" {
			t.Fatal("non_fast_forward must not be sent when force pushes are allowed")
		}
	}
}

func TestBranchRulesFallBackToClassicProtectionOnAPlanLimit(t *testing.T) {
	upgrade := `{"message":"Upgrade to GitHub Pro or make this repository public to enable this feature."}`

	var seen []string
	client := serve(t, map[string]route{
		"GET /repos/octocat/demo/rulesets":                    {http.StatusOK, `[]`},
		"POST /repos/octocat/demo/rulesets":                   {http.StatusForbidden, upgrade},
		"PUT /repos/octocat/demo/branches/main/protection":    {http.StatusOK, `{"url":"x"}`},
		"PUT /repos/octocat/demo/branches/dev/protection":     {http.StatusOK, `{"url":"x"}`},
		"PUT /repos/octocat/demo/branches/dev-%2A/protection": {http.StatusOK, `{"url":"x"}`},
	}, &seen)

	rules := []tmpl.Rule{{Name: "core", Patterns: []string{"main", "dev", "dev-*"}}}
	steps := client.ApplyBranchRules(context.Background(), target(), rules, provider.ModeAppend)

	counts := statuses(steps)
	if counts[provider.StatusFailed] != 0 {
		t.Fatalf("a plan limitation must not fail the run: %+v", steps)
	}
	if counts[provider.StatusOK] != 2 {
		t.Errorf("ok = %d, want main and dev protected classically: %+v", counts[provider.StatusOK], steps)
	}

	var globSkipped bool
	for _, step := range steps {
		if strings.Contains(step.Label, "dev-*") && step.Status == provider.StatusSkipped {
			globSkipped = true
		}
	}
	if !globSkipped {
		t.Error("a glob pattern has no classic equivalent and must be reported as skipped")
	}

	for _, request := range seen {
		if strings.Contains(request, "branches/dev-") && strings.Contains(request, "protection") {
			t.Error("a glob pattern must not be sent to the classic protection endpoint")
		}
	}
}

func TestBranchRulesReportMissingBranchesRatherThanFailing(t *testing.T) {
	client := serve(t, map[string]route{
		"GET /repos/octocat/demo/rulesets":  {http.StatusOK, `[]`},
		"POST /repos/octocat/demo/rulesets": {http.StatusForbidden, `{"message":"Upgrade to GitHub Pro"}`},
	}, nil)

	steps := client.ApplyBranchRules(context.Background(), target(),
		[]tmpl.Rule{{Name: "core", Patterns: []string{"main"}}}, provider.ModeAppend)

	if statuses(steps)[provider.StatusFailed] != 0 {
		t.Fatalf("a branch that does not exist yet must be skipped, not failed: %+v", steps)
	}
	if !strings.Contains(steps[0].Detail, "does not exist") {
		t.Errorf("detail = %q, want it to explain the branch is missing", steps[0].Detail)
	}
}

func TestBranchRulesRetryWithoutTheMergeMethodRule(t *testing.T) {
	var attempts int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `[]`)
			return
		}

		body, _ := io.ReadAll(r.Body)
		attempts++

		if strings.Contains(string(body), "pull_request") {
			w.WriteHeader(http.StatusUnprocessableEntity)
			io.WriteString(w, `{"message":"Invalid request.","errors":["Invalid property /rules/2"]}`)
			return
		}
		io.WriteString(w, `{"id":7}`)
	}))
	defer server.Close()

	client := New("test", "token", server.URL)
	steps := client.ApplyBranchRules(context.Background(), target(),
		[]tmpl.Rule{{Name: "main", Patterns: []string{"main"}, MergeMethods: []string{"merge"}}},
		provider.ModeAppend)

	if attempts != 2 {
		t.Fatalf("attempts = %d, want the rule to be retried without pull_request", attempts)
	}
	if statuses(steps)[provider.StatusFailed] != 0 {
		t.Fatalf("the retry succeeded, so nothing should be marked failed: %+v", steps)
	}
	if !strings.Contains(steps[0].Detail, "merge-method") {
		t.Errorf("detail = %q, want it to say the merge-method restriction was dropped", steps[0].Detail)
	}
}

func TestReplaceModeRemovesRulesetsTheTemplateDoesNotDeclare(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"GET /repos/octocat/demo/rulesets":      {http.StatusOK, `[{"id":1,"name":"main"},{"id":2,"name":"legacy"}]`},
		"PUT /repos/octocat/demo/rulesets/1":    {http.StatusOK, `{"id":1}`},
		"DELETE /repos/octocat/demo/rulesets/2": {http.StatusNoContent, ``},
	}, &seen)

	steps := client.ApplyBranchRules(context.Background(), target(),
		[]tmpl.Rule{{Name: "main", Patterns: []string{"main"}}}, provider.ModeReplace)

	if statuses(steps)[provider.StatusFailed] != 0 {
		t.Fatalf("unexpected failures: %+v", steps)
	}

	var deleted bool
	for _, request := range seen {
		if strings.HasPrefix(request, "DELETE /repos/octocat/demo/rulesets/2") {
			deleted = true
		}
	}
	if !deleted {
		t.Error("replace mode must delete the ruleset the template does not declare")
	}
}

func TestAppendModeLeavesOtherRulesetsAlone(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"GET /repos/octocat/demo/rulesets":   {http.StatusOK, `[{"id":1,"name":"main"},{"id":2,"name":"legacy"}]`},
		"PUT /repos/octocat/demo/rulesets/1": {http.StatusOK, `{"id":1}`},
	}, &seen)

	client.ApplyBranchRules(context.Background(), target(),
		[]tmpl.Rule{{Name: "main", Patterns: []string{"main"}}}, provider.ModeAppend)

	for _, request := range seen {
		if strings.HasPrefix(request, "DELETE") {
			t.Errorf("append mode must not delete anything, but sent %q", request)
		}
	}
}

func TestActionsSettingsThatDoNotApplyAreSkipped(t *testing.T) {
	client := serve(t, map[string]route{
		"PUT /repos/octocat/demo/actions/permissions/fork-pr-contributor-approval": {
			http.StatusUnprocessableEntity,
			`{"message":"Validation Failed","errors":"Fork PR approval is not allowed for private repositories."}`,
		},
		"PUT /repos/octocat/demo/actions/permissions/workflow": {http.StatusForbidden, `{"message":"Resource not accessible by personal access token"}`},
	}, nil)

	steps := client.ApplyActions(context.Background(), target(), &tmpl.Actions{
		ForkPRApproval:             provider.String("all_external_contributors"),
		DefaultWorkflowPermissions: provider.String("read"),
	})

	if statuses(steps)[provider.StatusFailed] != 0 {
		t.Fatalf("settings the provider refuses must be skipped, not failed: %+v", steps)
	}
	if statuses(steps)[provider.StatusSkipped] != 2 {
		t.Errorf("skipped = %d, want 2: %+v", statuses(steps)[provider.StatusSkipped], steps)
	}
	if !strings.Contains(steps[0].Detail, "private repositories") {
		t.Errorf("detail = %q, want the provider's own reason", steps[0].Detail)
	}
}

func TestOutsideAccessIsSkippedOnPublicRepositories(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{}, &seen)

	public := target()
	public.Visibility = "public"

	steps := client.ApplyActions(context.Background(), public,
		&tmpl.Actions{OutsideAccess: provider.String("none")})

	if len(steps) != 1 || steps[0].Status != provider.StatusSkipped {
		t.Fatalf("steps = %+v, want a single skip", steps)
	}
	if len(seen) != 0 {
		t.Errorf("no request should be sent for a setting that cannot apply, got %v", seen)
	}
}

func TestSecretsAreSkippedWhenTheTokenCannotReadThePublicKey(t *testing.T) {
	client := serve(t, map[string]route{
		"GET /repos/octocat/demo/actions/secrets/public-key": {http.StatusForbidden, `{"message":"Resource not accessible by personal access token"}`},
		"POST /repos/octocat/demo/actions/variables":         {http.StatusCreated, `{}`},
	}, nil)

	steps := client.ApplyVariables(context.Background(), target(), []tmpl.Variable{
		{Key: "PLAIN", Value: "1", Visibility: "all"},
		{Key: "SECRET", Value: "shh", Visibility: "private"},
	})

	counts := statuses(steps)
	if counts[provider.StatusFailed] != 0 {
		t.Fatalf("a missing scope must not fail the run: %+v", steps)
	}
	if counts[provider.StatusOK] != 1 {
		t.Errorf("the plain variable should still be applied: %+v", steps)
	}
	if counts[provider.StatusSkipped] != 1 {
		t.Errorf("the secret should be skipped: %+v", steps)
	}
}

func TestVariablesFallBackToPatchWhenTheyAlreadyExist(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"POST /repos/octocat/demo/actions/variables":       {http.StatusConflict, `{"message":"variable already exists"}`},
		"PATCH /repos/octocat/demo/actions/variables/FLAG": {http.StatusNoContent, ``},
	}, &seen)

	steps := client.ApplyVariables(context.Background(), target(),
		[]tmpl.Variable{{Key: "FLAG", Value: "2", Visibility: "all"}})

	if len(steps) != 1 || steps[0].Status != provider.StatusOK {
		t.Fatalf("steps = %+v, want the existing variable to be updated", steps)
	}
	if len(seen) != 2 {
		t.Errorf("requests = %v, want a create attempt followed by an update", seen)
	}
}

func TestCreateSurfacesATakenName(t *testing.T) {
	client := serve(t, map[string]route{
		"POST /user/repos": {
			http.StatusUnprocessableEntity,
			`{"message":"Repository creation failed.","errors":[{"message":"name already exists on this account"}]}`,
		},
	}, nil)

	_, err := client.Create(context.Background(), provider.Target{Owner: "octocat", OwnerType: "user"},
		provider.Settings{Name: "demo"})

	if err == nil {
		t.Fatal("expected an error")
	}
	if !provider.NameTaken(err) {
		t.Errorf("NameTaken(%v) = false, want true so the form can ask for another name", err)
	}
}

func TestLookupReportsAbsenceRatherThanAnError(t *testing.T) {
	client := serve(t, map[string]route{}, nil)

	_, found, err := client.Lookup(context.Background(), provider.Target{Owner: "octocat"}, "missing")
	if err != nil {
		t.Fatalf("a 404 means the name is free, not an error: %v", err)
	}
	if found {
		t.Error("found = true, want false")
	}
}

func TestUpdateSettingsOnlySendsDeclaredKeys(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"PATCH /repos/octocat/demo": {http.StatusOK, `{}`},
	}, &seen)

	client.UpdateSettings(context.Background(), target(), provider.Settings{
		Name:     "demo",
		Features: tmpl.NewFeatures(map[string]bool{"issues": true}),
	})

	if len(seen) != 1 {
		t.Fatalf("requests = %v, want exactly one", seen)
	}

	body := seen[0][strings.Index(seen[0], "{"):]
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}

	if _, ok := payload["has_issues"]; !ok {
		t.Error("has_issues was declared and must be sent")
	}
	for _, key := range []string{"has_wiki", "has_projects", "description", "visibility"} {
		if _, ok := payload[key]; ok {
			t.Errorf("%s was not declared by the template and must not be sent", key)
		}
	}
}

func TestEmptyValuesLeaveTheExistingVariableAlone(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"GET /repos/octocat/demo/actions/secrets/public-key": {http.StatusOK,
			`{"key_id":"1","key":"BLuGkV4wV9GXaAGCZmSFwLYr0OYCnAcxbmZ0lQpVnTM="}`},
		"PUT /repos/octocat/demo/actions/secrets/KEPT": {http.StatusNoContent, ``},
		"POST /repos/octocat/demo/actions/variables":   {http.StatusCreated, `{}`},
	}, &seen)

	steps := client.ApplyVariables(context.Background(), target(), []tmpl.Variable{
		{Key: "BLANK_SECRET", Value: "", Visibility: "private"},
		{Key: "BLANK_VAR", Value: "", Visibility: "all"},
		{Key: "KEPT", Value: "real", Visibility: "private"},
	})

	counts := statuses(steps)
	if counts[provider.StatusSkipped] != 2 {
		t.Errorf("skipped = %d, want the two blank entries skipped: %+v", counts[provider.StatusSkipped], steps)
	}
	if counts[provider.StatusOK] != 1 {
		t.Errorf("ok = %d, want the one real value written: %+v", counts[provider.StatusOK], steps)
	}

	for _, request := range seen {
		if strings.Contains(request, "BLANK_SECRET") || strings.Contains(request, "BLANK_VAR") {
			t.Errorf("a blank value must never be written, but sent %q", request)
		}
	}
	if !strings.Contains(strings.Join(seen, "\n"), "secrets/KEPT") {
		t.Error("the secret with a real value should still be written")
	}
}

func TestTagRulesTargetTags(t *testing.T) {
	built := buildTagRuleset(tmpl.TagRule{Name: "releases", Patterns: []string{"v*", "refs/tags/rc-*"}})

	if built.Target != "tag" {
		t.Errorf("target = %q, want tag", built.Target)
	}
	want := []string{"refs/tags/v*", "refs/tags/rc-*"}
	if len(built.Conditions.RefName.Include) != 2 {
		t.Fatalf("include = %v, want %v", built.Conditions.RefName.Include, want)
	}
	for i, pattern := range want {
		if built.Conditions.RefName.Include[i] != pattern {
			t.Errorf("include[%d] = %q, want %q", i, built.Conditions.RefName.Include[i], pattern)
		}
	}
}

func TestRequiredApprovalsBecomeAPullRequestRule(t *testing.T) {
	built := buildRuleset(tmpl.Rule{
		Name: "main", Patterns: []string{"main"},
		RequiredApprovals: provider.Int(2), CodeOwnerApproval: provider.Bool(true),
	})

	for _, rule := range built.Rules {
		if rule["type"] != "pull_request" {
			continue
		}
		parameters := rule["parameters"].(map[string]any)
		if parameters["required_approving_review_count"] != 2 {
			t.Errorf("approvals = %v, want 2", parameters["required_approving_review_count"])
		}
		if parameters["require_code_owner_review"] != true {
			t.Error("code owner review should be required")
		}
		return
	}
	t.Errorf("rules = %+v, want a pull_request rule", built.Rules)
}

func TestMergeMethodMapsOntoRepositoryToggles(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"PATCH /repos/octocat/demo": {http.StatusOK, `{}`},
	}, &seen)

	steps := client.ApplyMergeRequests(context.Background(), target(), &tmpl.MergeRequests{
		MergeMethod:        provider.String("rebase"),
		Squash:             provider.String("never"),
		DeleteSourceBranch: provider.Bool(true),
	})

	if statuses(steps)[provider.StatusFailed] != 0 {
		t.Fatalf("unexpected failures: %+v", steps)
	}

	body := seen[0][strings.Index(seen[0], "{"):]
	for _, want := range []string{
		`"allow_merge_commit":false`,
		`"allow_rebase_merge":true`,
		`"allow_squash_merge":false`,
		`"delete_branch_on_merge":true`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body = %q, want %s", body, want)
		}
	}
}

func TestGitLabOnlyMergeKeysAreReported(t *testing.T) {
	client := serve(t, map[string]route{}, nil)

	steps := client.ApplyMergeRequests(context.Background(), target(), &tmpl.MergeRequests{
		ApprovalsRequired:   provider.Int(2),
		PipelineMustSucceed: provider.Bool(true),
	})

	if len(steps) != 2 {
		t.Fatalf("steps = %+v, want both keys reported", steps)
	}
	for _, step := range steps {
		if step.Status != provider.StatusInfo {
			t.Errorf("status = %v, want info", step.Status)
		}
		if !strings.Contains(step.Detail, "branch_rules") {
			t.Errorf("detail = %q, want it to point at branch_rules", step.Detail)
		}
	}
}

func TestWebhookEventsAreTranslated(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"GET /repos/octocat/demo/hooks":  {http.StatusOK, `[]`},
		"POST /repos/octocat/demo/hooks": {http.StatusCreated, `{"id":1}`},
	}, &seen)

	steps := client.ApplyWebhooks(context.Background(), target(), []tmpl.Webhook{{
		URL:    "https://example.test/hook",
		Events: []string{"merge_request", "note", "pipeline"},
	}})

	if statuses(steps)[provider.StatusFailed] != 0 {
		t.Fatalf("unexpected failures: %+v", steps)
	}

	body := seen[len(seen)-1]
	for _, want := range []string{"pull_request", "issue_comment", "workflow_run"} {
		if !strings.Contains(body, want) {
			t.Errorf("body = %q, want the GitHub name %s", body, want)
		}
	}
}

func TestSecurityTogglesSplitAcrossTheirEndpoints(t *testing.T) {
	var seen []string
	client := serve(t, map[string]route{
		"PATCH /repos/octocat/demo":                           {http.StatusOK, `{}`},
		"PUT /repos/octocat/demo/vulnerability-alerts":        {http.StatusNoContent, ``},
		"DELETE /repos/octocat/demo/automated-security-fixes": {http.StatusNoContent, ``},
	}, &seen)

	steps := client.UpdateSettings(context.Background(), target(), provider.Settings{
		Name: "demo",
		Features: tmpl.NewFeatures(map[string]bool{
			"issues":                      true,
			"secret_scanning":             true,
			"dependabot_alerts":           true,
			"dependabot_security_updates": false,
		}),
	})

	if statuses(steps)[provider.StatusFailed] != 0 {
		t.Fatalf("unexpected failures: %+v", steps)
	}

	var patch string
	for _, request := range seen {
		if strings.HasPrefix(request, "PATCH ") {
			patch = request
		}
	}
	if !strings.Contains(patch, `"has_issues":true`) {
		t.Errorf("patch = %q, want the plain field", patch)
	}
	if !strings.Contains(patch, `"security_and_analysis":{"secret_scanning":{"status":"enabled"}}`) {
		t.Errorf("patch = %q, want secret scanning nested under security_and_analysis", patch)
	}
	if strings.Contains(patch, "dependabot") {
		t.Errorf("patch = %q, want the dependabot toggles kept off the repository payload", patch)
	}

	var put, del bool
	for _, request := range seen {
		if strings.HasPrefix(request, "PUT /repos/octocat/demo/vulnerability-alerts") {
			put = true
		}
		if strings.HasPrefix(request, "DELETE /repos/octocat/demo/automated-security-fixes") {
			del = true
		}
	}
	if !put || !del {
		t.Errorf("requests = %v, want an enable via PUT and a disable via DELETE", seen)
	}
}

func TestGitHubFeatureCountGrew(t *testing.T) {
	keys := tmpl.FeatureKeys("github")
	for _, want := range []string{
		"issues", "wiki", "projects", "discussions", "downloads", "forking",
		"template", "web_commit_signoff", "advanced_security", "secret_scanning",
		"secret_scanning_push_protection", "dependabot_alerts", "dependabot_security_updates",
	} {
		if !slices.Contains(keys, want) {
			t.Errorf("%q missing from the GitHub catalog", want)
		}
	}
}

func TestNamespacesListsSelfThenOrgs(t *testing.T) {
	client := serve(t, map[string]route{
		"GET /user":      {http.StatusOK, `{"login":"octocat","name":"Dev User"}`},
		"GET /user/orgs": {http.StatusOK, `[{"login":"acme"},{"login":"other"}]`},
	}, nil)

	spaces, err := client.Namespaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(spaces) != 3 {
		t.Fatalf("spaces = %+v, want the user plus two orgs", spaces)
	}
	if spaces[0].Kind != "user" || spaces[0].Path != "octocat" {
		t.Errorf("first entry = %+v, want the personal account first", spaces[0])
	}
	for _, space := range spaces[1:] {
		if space.Kind != "org" {
			t.Errorf("%+v should be an org", space)
		}
	}
}
