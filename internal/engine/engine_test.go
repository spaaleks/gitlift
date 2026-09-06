package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/spaaleks/gitlift/internal/provider"
	"github.com/spaaleks/gitlift/internal/tmpl"
)

type recorder struct {
	calls []string
}

func (r *recorder) Name() string { return "test" }
func (r *recorder) Kind() string { return "gitlab" }
func (r *recorder) Host() string { return "example.test" }

func (r *recorder) ListProjects(context.Context) ([]provider.Project, error) { return nil, nil }

func (r *recorder) Lookup(context.Context, provider.Target, string) (provider.Project, bool, error) {
	return provider.Project{}, false, nil
}

func (r *recorder) Create(context.Context, provider.Target, provider.Settings) (provider.Project, error) {
	r.calls = append(r.calls, "create")
	return provider.Project{FullName: "group/demo"}, nil
}

func (r *recorder) note(name string) []provider.Step {
	r.calls = append(r.calls, name)
	return []provider.Step{provider.OK(name, name)}
}

func (r *recorder) Namespaces(context.Context) ([]provider.Namespace, error) { return nil, nil }

func (r *recorder) Capture(context.Context, provider.Project) (tmpl.Spec, []provider.Step) {
	r.calls = append(r.calls, "capture")
	return tmpl.Spec{}, nil
}

func (r *recorder) UpdateSettings(context.Context, provider.Project, provider.Settings) []provider.Step {
	return r.note("settings")
}

func (r *recorder) ApplyBranchRules(_ context.Context, _ provider.Project, rules []tmpl.Rule, _ provider.RuleMode) []provider.Step {
	if len(rules) == 0 {
		return nil
	}
	return r.note("branches")
}

func (r *recorder) ApplyTagRules(_ context.Context, _ provider.Project, rules []tmpl.TagRule, _ provider.RuleMode) []provider.Step {
	if len(rules) == 0 {
		return nil
	}
	return r.note("tags")
}

func (r *recorder) ApplyMergeRequests(_ context.Context, _ provider.Project, settings *tmpl.MergeRequests) []provider.Step {
	if settings.Empty() {
		return nil
	}
	return r.note("merge_requests")
}

func (r *recorder) ApplyActions(_ context.Context, _ provider.Project, actions *tmpl.Actions) []provider.Step {
	if actions.Empty() {
		return nil
	}
	return r.note("actions")
}

func (r *recorder) ApplyPipelines(_ context.Context, _ provider.Project, pipelines *tmpl.Pipelines) []provider.Step {
	if pipelines.Empty() {
		return nil
	}
	return r.note("pipelines")
}

func (r *recorder) ApplyWebhooks(_ context.Context, _ provider.Project, hooks []tmpl.Webhook) []provider.Step {
	if len(hooks) == 0 {
		return nil
	}
	return r.note("webhooks")
}

func (r *recorder) ApplyIntegrations(_ context.Context, _ provider.Project, list []tmpl.Integration) []provider.Step {
	if len(list) == 0 {
		return nil
	}
	return r.note("integrations")
}

func (r *recorder) ApplyVariables(_ context.Context, _ provider.Project, variables []tmpl.Variable) []provider.Step {
	if len(variables) == 0 {
		return nil
	}
	return r.note("variables")
}

func fullSpec() tmpl.Spec {
	return tmpl.Spec{
		Repo:          &tmpl.Repo{Features: tmpl.NewFeatures(map[string]bool{"issues": true})},
		BranchRules:   &tmpl.BranchRules{Rules: []tmpl.Rule{{Name: "main", Patterns: []string{"main"}}}},
		TagRules:      &tmpl.TagRules{Rules: []tmpl.TagRule{{Name: "rel", Patterns: []string{"v*"}}}},
		MergeRequests: &tmpl.MergeRequests{MergeMethod: provider.String("merge")},
		Actions:       &tmpl.Actions{OutsideAccess: provider.String("none")},
		Pipelines:     &tmpl.Pipelines{AutoDevOps: provider.Bool(false)},
		Webhooks:      []tmpl.Webhook{{URL: "https://example.test/hook"}},
		Integrations:  []tmpl.Integration{{Name: "slack"}},
		Variables:     []tmpl.Variable{{Key: "TOKEN", Value: "x"}},
	}
}

func TestEveryDeclaredBlockIsApplied(t *testing.T) {
	client := &recorder{}
	spec := fullSpec()

	plan := FromSpec(Plan{
		Provider: client,
		Project:  provider.Project{FullName: "group/demo"},
		Settings: Settings(spec, "demo", "demo"),
		RuleMode: provider.ModeAppend,
	}, spec)
	plan.Variables = spec.Variables

	Run(context.Background(), plan, nil)

	want := []string{
		"settings", "merge_requests", "branches", "tags",
		"actions", "pipelines", "webhooks", "integrations", "variables",
	}
	if strings.Join(client.calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v\nwant  = %v", client.calls, want)
	}
}

func TestMergeSettingsAreAppliedBeforeBranchRules(t *testing.T) {
	client := &recorder{}
	spec := fullSpec()

	plan := FromSpec(Plan{
		Provider: client,
		Project:  provider.Project{FullName: "group/demo"},
		RuleMode: provider.ModeAppend,
	}, spec)

	Run(context.Background(), plan, nil)

	merge, branches := -1, -1
	for i, call := range client.calls {
		switch call {
		case "merge_requests":
			merge = i
		case "branches":
			branches = i
		}
	}
	if merge < 0 || branches < 0 || merge > branches {
		t.Errorf("calls = %v, want merge_requests before branches", client.calls)
	}
}

func TestAnArchivedProjectIsLeftAlone(t *testing.T) {
	client := &recorder{}
	spec := fullSpec()

	plan := FromSpec(Plan{
		Provider: client,
		Project:  provider.Project{FullName: "group/demo", Archived: true},
		RuleMode: provider.ModeAppend,
	}, spec)

	result := Run(context.Background(), plan, nil)

	for _, call := range client.calls {
		if call != "settings" {
			t.Errorf("calls = %v, want nothing applied to an archived project", client.calls)
			break
		}
	}
	if len(result.Steps) == 0 || result.Steps[len(result.Steps)-1].Status != provider.StatusSkipped {
		t.Errorf("steps = %+v, want a skip explaining the project is archived", result.Steps)
	}
}

func TestUndeclaredBlocksAreNeverApplied(t *testing.T) {
	client := &recorder{}
	spec := tmpl.Spec{Repo: &tmpl.Repo{Visibility: provider.String("private")}}

	plan := FromSpec(Plan{
		Provider: client,
		Project:  provider.Project{FullName: "group/demo"},
		Settings: Settings(spec, "demo", "demo"),
		RuleMode: provider.ModeAppend,
	}, spec)

	Run(context.Background(), plan, nil)

	if strings.Join(client.calls, ",") != "settings" {
		t.Errorf("calls = %v, want only the settings call", client.calls)
	}
}
