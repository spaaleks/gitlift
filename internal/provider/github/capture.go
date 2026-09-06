package github

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/spaaleks/gitlift/internal/provider"
	"github.com/spaaleks/gitlift/internal/tmpl"
)

const captureScope = "capture"

func (g *GitHub) Capture(ctx context.Context, project provider.Project) (tmpl.Spec, []provider.Step) {
	var steps []provider.Step

	var live repository
	if err := g.client.Do(ctx, provider.Request{
		Method: http.MethodGet, Path: "/repos/" + project.Owner + "/" + project.Name, Out: &live,
	}); err != nil {
		return tmpl.Spec{}, []provider.Step{provider.Failed(captureScope, "read repository", err)}
	}

	visibility := live.Visibility
	if visibility == "" {
		visibility = "public"
		if live.Private {
			visibility = "private"
		}
	}

	spec := tmpl.Spec{
		Repo: &tmpl.Repo{
			Visibility: provider.String(visibility),
			Topics:     live.Topics,
			Features:   readFeatures(live.raw),
		},
	}
	if live.DefaultBranch != "" {
		spec.Repo.DefaultBranch = provider.String(live.DefaultBranch)
	}
	steps = append(steps, provider.OK(captureScope, "repository settings").
		With(spec.Repo.Features.Keys()...))

	if mr := captureMergeSettings(live.raw); mr != nil {
		spec.MergeRequests = mr
		steps = append(steps, provider.OK(captureScope, "merge settings"))
	}

	rulesets, err := g.listRulesets(ctx, project)
	switch {
	case err != nil:
		steps = append(steps, provider.Skipped(captureScope, "branch rules", provider.Explain(err)))
	case len(rulesets) > 0:
		branches, tags := captureRulesets(ctx, g, project, rulesets)
		if len(branches) > 0 {
			spec.BranchRules = &tmpl.BranchRules{Rules: branches}
			steps = append(steps, provider.OK(captureScope, "branch rules"))
		}
		if len(tags) > 0 {
			spec.TagRules = &tmpl.TagRules{Rules: tags}
			steps = append(steps, provider.OK(captureScope, "tag rules"))
		}
	}

	steps = append(steps, provider.Info(captureScope, "not captured",
		"webhooks, Actions settings and secrets carry credentials and are left out"))

	return spec, steps
}

func captureRulesets(ctx context.Context, g *GitHub, project provider.Project, list []ruleset) ([]tmpl.Rule, []tmpl.TagRule) {
	var branches []tmpl.Rule
	var tags []tmpl.TagRule

	for _, entry := range list {
		var full ruleset
		if err := g.client.Do(ctx, provider.Request{
			Method: http.MethodGet,
			Path:   "/repos/" + project.Owner + "/" + project.Name + "/rulesets/" + itoa(entry.ID),
			Out:    &full,
		}); err != nil {
			full = entry
		}

		patterns := make([]string, 0, len(full.Conditions.RefName.Include))
		for _, include := range full.Conditions.RefName.Include {
			patterns = append(patterns, strings.TrimPrefix(
				strings.TrimPrefix(include, "refs/heads/"), "refs/tags/"))
		}
		if len(patterns) == 0 {
			continue
		}

		if full.Target == "tag" {
			tags = append(tags, tmpl.TagRule{Name: full.Name, Patterns: patterns})
			continue
		}

		rule := tmpl.Rule{
			Name:           full.Name,
			Patterns:       patterns,
			AllowForcePush: provider.Bool(true),
		}
		for _, item := range full.Rules {
			switch item["type"] {
			case "non_fast_forward":
				rule.AllowForcePush = provider.Bool(false)
			case "pull_request":
				parameters, _ := item["parameters"].(map[string]any)
				if methods, ok := parameters["allowed_merge_methods"].([]any); ok {
					for _, method := range methods {
						if text, ok := method.(string); ok {
							rule.MergeMethods = append(rule.MergeMethods, text)
						}
					}
				}
				if count, ok := parameters["required_approving_review_count"].(float64); ok && count > 0 {
					rule.RequiredApprovals = provider.Int(int(count))
				}
				if on, ok := parameters["require_code_owner_review"].(bool); ok && on {
					rule.CodeOwnerApproval = provider.Bool(true)
				}
			}
		}
		branches = append(branches, rule)
	}

	return branches, tags
}

func captureMergeSettings(raw map[string]any) *tmpl.MergeRequests {
	mr := &tmpl.MergeRequests{}

	merge, _ := raw["allow_merge_commit"].(bool)
	rebase, _ := raw["allow_rebase_merge"].(bool)
	squash, _ := raw["allow_squash_merge"].(bool)

	switch {
	case merge:
		mr.MergeMethod = provider.String("merge")
	case rebase:
		mr.MergeMethod = provider.String("rebase")
	}
	if _, present := raw["allow_squash_merge"]; present {
		option := "never"
		if squash {
			option = "default_off"
		}
		mr.Squash = provider.String(option)
	}
	if on, ok := raw["delete_branch_on_merge"].(bool); ok {
		mr.DeleteSourceBranch = provider.Bool(on)
	}
	if on, ok := raw["allow_auto_merge"].(bool); ok {
		mr.AllowAutoMerge = provider.Bool(on)
	}

	if mr.Empty() {
		return nil
	}
	return mr
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
