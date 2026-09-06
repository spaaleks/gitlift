package tmpl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func load(t *testing.T, body string) Template {
	t.Helper()
	template, err := parse([]byte(body), "test.yaml", "custom")
	if err != nil {
		t.Fatal(err)
	}
	return template
}

func TestProviderOverridesReplaceArraysAndMergeMaps(t *testing.T) {
	template := load(t, `
name: mixed
repo:
  visibility: private
  features:
    issues: true
    wiki: false
branch_rules:
  rules:
    - name: root
      patterns: ["main"]
providers:
  github:
    repo:
      features:
        wiki: true
    branch_rules:
      rules:
        - name: gh
          patterns: ["main", "dev"]
`)

	spec, err := template.For("github")
	if err != nil {
		t.Fatal(err)
	}

	if *spec.Repo.Visibility != "private" {
		t.Errorf("visibility = %q, want private", *spec.Repo.Visibility)
	}
	if on, ok := spec.Repo.Features.Get("issues"); !ok || !on {
		t.Error("issues should survive the merge as true")
	}
	if on, ok := spec.Repo.Features.Get("wiki"); !ok || !on {
		t.Error("wiki should be overridden to true")
	}

	rules := spec.Rules()
	if len(rules) != 1 || rules[0].Name != "gh" {
		t.Fatalf("rules = %+v, want the override list to replace the root list", rules)
	}
	if len(rules[0].Patterns) != 2 {
		t.Errorf("patterns = %v, want both from the override", rules[0].Patterns)
	}
}

func TestAbsentKeysStayAbsent(t *testing.T) {
	template := load(t, `
name: sparse
repo:
  features:
    issues: false
`)

	spec, err := template.For("github")
	if err != nil {
		t.Fatal(err)
	}

	if spec.Repo.Visibility != nil {
		t.Error("visibility was not declared and must stay nil")
	}
	if spec.Repo.Description != nil {
		t.Error("description was not declared and must stay nil")
	}
	if on, ok := spec.Repo.Features.Get("issues"); !ok || on {
		t.Error("issues: false must survive as a declared false, not as absence")
	}
	if spec.Repo.Features.Has("wiki") {
		t.Error("wiki was not declared and must stay absent")
	}
}

func TestForDoesNotMutateTheTemplate(t *testing.T) {
	template := load(t, `
name: reuse
repo:
  visibility: private
providers:
  github:
    repo:
      visibility: public
`)

	if _, err := template.For("github"); err != nil {
		t.Fatal(err)
	}

	spec, err := template.For("gitlab")
	if err != nil {
		t.Fatal(err)
	}
	if *spec.Repo.Visibility != "private" {
		t.Errorf("visibility = %q, want the github override not to leak into gitlab", *spec.Repo.Visibility)
	}
}

func TestSupportsOnlyDeclaredProviders(t *testing.T) {
	scoped := load(t, "name: scoped\nproviders:\n  gitlab:\n    context:\n      namespace: g\n")
	if scoped.Supports("github") {
		t.Error("a template with only a gitlab block must not be offered for github")
	}
	if !scoped.Supports("gitlab") {
		t.Error("a template with a gitlab block must be offered for gitlab")
	}

	open := load(t, "name: open\n")
	if !open.Supports("github") || !open.Supports("gitlab") {
		t.Error("a template with no providers block must be offered for every provider")
	}
}

func TestValidateRejectsUnknownEnumValues(t *testing.T) {
	cases := map[string]string{
		"visibility":    "name: t\nrepo:\n  visibility: secret\n",
		"merge_methods": "name: t\nbranch_rules:\n  rules:\n    - name: r\n      patterns: [main]\n      merge_methods: [fast-forward]\n",
		"merge_access":  "name: t\nbranch_rules:\n  rules:\n    - name: r\n      patterns: [main]\n      merge_access: everyone\n",
		"fork approval": "name: t\nactions:\n  fork_pr_approval: nobody\n",
		"outside":       "name: t\nactions:\n  outside_access: everyone\n",
		"workflow":      "name: t\nactions:\n  default_workflow_permissions: admin\n",
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := load(t, body).For("github"); err == nil {
				t.Fatal("expected the invalid value to be rejected")
			}
		})
	}
}

func TestLoadPrefersUserTemplatesOverBuiltins(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "github/base.yaml", "name: \"GitHub base\"\ndescription: mine\n")

	templates, err := Load([]string{dir})
	if err != nil {
		t.Fatal(err)
	}

	var found int
	for _, template := range templates {
		if template.Name == "GitHub base" {
			found++
			if template.Description != "mine" {
				t.Errorf("description = %q, want the user copy to win", template.Description)
			}
			if template.Builtin {
				t.Error("the user copy must not be marked builtin")
			}
			if template.Scope != "github" {
				t.Errorf("scope = %q, want github", template.Scope)
			}
		}
	}
	if found != 1 {
		t.Errorf("found %d templates named \"GitHub base\", want exactly 1", found)
	}
}

func TestLoadReportsBadFilesWithoutLosingGoodOnes(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "broken.yaml", "name: [unclosed\n")
	write(t, dir, "fine.yaml", "name: fine\n")

	templates, err := Load([]string{dir})
	if err == nil {
		t.Fatal("expected the broken file to be reported")
	}
	if !strings.Contains(err.Error(), "broken.yaml") {
		t.Errorf("error = %v, want it to name the broken file", err)
	}

	var hasFine bool
	for _, template := range templates {
		if template.Name == "fine" {
			hasFine = true
		}
	}
	if !hasFine {
		t.Error("the good template must still be loaded")
	}
}

func TestBuiltinTemplatesAreValid(t *testing.T) {
	templates, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) == 0 {
		t.Fatal("no builtin templates were embedded")
	}

	for _, template := range templates {
		for _, kind := range []string{"github", "gitlab"} {
			if !template.Supports(kind) {
				continue
			}
			if _, err := template.For(kind); err != nil {
				t.Errorf("%s for %s: %v", template.Name, kind, err)
			}
		}
	}
}

func TestNaturalOrdering(t *testing.T) {
	if !naturalLess("00 alpha", "10 alpha") {
		t.Error("00 should sort before 10")
	}
	if !naturalLess("item 9", "item 10") {
		t.Error("9 should sort before 10, not after")
	}
	if naturalLess("Zeta", "alpha") {
		t.Error("comparison should be case-insensitive")
	}
}

func TestUnknownFeatureKeysAreRejected(t *testing.T) {
	template := load(t, `
name: bad
repo:
  features:
    telepathy: true
`)

	if _, err := template.For("gitlab"); err == nil {
		t.Fatal("an unknown feature key must be an error")
	}
}

func TestFeaturesNarrowToTheProvider(t *testing.T) {
	template := load(t, `
name: mixed
repo:
  features:
    issues: true
    projects: true
    container_registry: false
`)

	spec, err := template.For("gitlab")
	if err != nil {
		t.Fatal(err)
	}

	kept, dropped := spec.Repo.Features.For("gitlab")
	if kept.Has("projects") {
		t.Error("projects is GitHub-only and must not survive narrowing")
	}
	if !kept.Has("container_registry") {
		t.Error("container_registry is a GitLab toggle and must survive")
	}
	if len(dropped) != 1 || dropped[0] != "projects" {
		t.Errorf("dropped = %v, want [projects]", dropped)
	}
}

func TestNewBlocksParseAndValidate(t *testing.T) {
	template := load(t, `
name: full
merge_requests:
  merge_method: "fast_forward"
  squash: "default_on"
  approvals_required: 2
tag_rules:
  rules:
    - name: "releases"
      patterns: ["v*"]
      create_access: "maintainers"
pipelines:
  timeout: "2h"
  git_strategy: "fetch"
webhooks:
  - url: "https://example.test/hook"
    events: ["push", "pipeline"]
integrations:
  - name: "slack"
    settings:
      webhook: "https://hooks.slack.test/x"
ci_cd_variables:
  - key: "TOKEN"
    value: "x"
    environment_scope: "production"
    type: "file"
`)

	spec, err := template.For("gitlab")
	if err != nil {
		t.Fatal(err)
	}

	if *spec.MergeRequests.MergeMethod != "fast_forward" {
		t.Errorf("merge method = %q", *spec.MergeRequests.MergeMethod)
	}
	if len(spec.Tags()) != 1 || spec.Tags()[0].CreateAccess != "maintainers" {
		t.Errorf("tag rules = %+v", spec.Tags())
	}
	if len(spec.Webhooks) != 1 || len(spec.Webhooks[0].Events) != 2 {
		t.Errorf("webhooks = %+v", spec.Webhooks)
	}
	if len(spec.Integrations) != 1 || spec.Integrations[0].Settings["webhook"] == nil {
		t.Errorf("integrations = %+v", spec.Integrations)
	}
	if spec.Variables[0].Scope() != "production" {
		t.Errorf("scope = %q", spec.Variables[0].Scope())
	}
}

func TestBadEnumsAreRejected(t *testing.T) {
	cases := map[string]string{
		"merge method": "merge_requests:\n  merge_method: \"octopus\"\n",
		"squash":       "merge_requests:\n  squash: \"sometimes\"\n",
		"git strategy": "pipelines:\n  git_strategy: \"teleport\"\n",
		"timeout":      "pipelines:\n  timeout: \"soon\"\n",
		"hook event":   "webhooks:\n  - url: \"https://x.test\"\n    events: [\"telepathy\"]\n",
		"push access":  "branch_rules:\n  rules:\n    - name: m\n      patterns: [main]\n      push_access: \"everyone\"\n",
		"tag access":   "tag_rules:\n  rules:\n    - name: r\n      patterns: [\"v*\"]\n      create_access: \"everyone\"\n",
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := load(t, "name: bad\n"+body).For("gitlab"); err == nil {
				t.Errorf("%s: an invalid value must be rejected", name)
			}
		})
	}
}

func TestParseTimeoutAcceptsSecondsAndDurations(t *testing.T) {
	cases := map[string]int{"3600": 3600, "1h": 3600, "1h30m": 5400, "90m": 5400}
	for input, want := range cases {
		got, err := ParseTimeout(input)
		if err != nil {
			t.Fatalf("ParseTimeout(%q): %v", input, err)
		}
		if got != want {
			t.Errorf("ParseTimeout(%q) = %d, want %d", input, got, want)
		}
	}
	for _, bad := range []string{"", "soon", "-5", "0"} {
		if _, err := ParseTimeout(bad); err == nil {
			t.Errorf("ParseTimeout(%q) should fail", bad)
		}
	}
}

func TestFeaturesCloneIsIndependent(t *testing.T) {
	original := NewFeatures(map[string]bool{"issues": true})
	copied := original.Clone()
	copied.Set("issues", false)

	if on, _ := original.Get("issues"); !on {
		t.Error("editing a clone must not reach back into the original")
	}
}
