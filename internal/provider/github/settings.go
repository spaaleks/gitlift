package github

import (
	"context"
	"fmt"
	"net/http"

	"github.com/spaaleks/gitlift/internal/provider"
	"github.com/spaaleks/gitlift/internal/tmpl"
)

func (g *GitHub) ApplyTagRules(ctx context.Context, project provider.Project, rules []tmpl.TagRule, mode provider.RuleMode) []provider.Step {
	const scope = "tag rules"

	if len(rules) == 0 {
		return nil
	}

	existing, err := g.listRulesets(ctx, project)
	if err != nil {
		return []provider.Step{provider.Failed(scope, "read existing rulesets", err)}
	}

	byName := map[string]int64{}
	tagRulesets := map[string]int64{}
	for _, entry := range existing {
		byName[entry.Name] = entry.ID
		if entry.Target == "tag" {
			tagRulesets[entry.Name] = entry.ID
		}
	}

	var steps []provider.Step
	applied := map[string]bool{}

	for _, rule := range rules {
		built := buildTagRuleset(rule)
		if built == nil {
			steps = append(steps, provider.Skipped(scope, rule.Name, "no patterns"))
			continue
		}
		if rule.CreateAccess != "" {
			steps = append(steps, provider.Info(scope, rule.Name+" create_access",
				"GitHub has no per-tag create access level. The key is GitLab-only and was ignored"))
		}

		label := provider.DescribeTagRule(rule)
		id := byName[built.Name]

		err := g.saveRuleset(ctx, project, id, built)
		if err != nil {
			if provider.PlanLimited(err) {
				steps = append(steps, provider.Skipped(scope, label,
					"tag rulesets are not available on this plan for a private repository"))
				continue
			}
			steps = append(steps, provider.Failed(scope, label, err))
			continue
		}

		applied[built.Name] = true
		verb := "created"
		if id != 0 {
			verb = "updated"
		}
		steps = append(steps, provider.Step{Scope: scope, Label: label, Status: provider.StatusOK, Detail: verb})
	}

	if mode == provider.ModeReplace {
		for name, id := range tagRulesets {
			if applied[name] {
				continue
			}
			delErr := g.client.Do(ctx, provider.Request{
				Method: http.MethodDelete,
				Path:   fmt.Sprintf("/repos/%s/%s/rulesets/%d", project.Owner, project.Name, id),
			})
			if delErr != nil {
				steps = append(steps, provider.Failed(scope, "remove ruleset "+name, delErr))
				continue
			}
			steps = append(steps, provider.Step{
				Scope: scope, Label: name, Status: provider.StatusOK, Detail: "removed",
			})
		}
	}

	return steps
}

func (g *GitHub) ApplyMergeRequests(ctx context.Context, project provider.Project, settings *tmpl.MergeRequests) []provider.Step {
	const scope = "merge requests"

	if settings.Empty() {
		return nil
	}

	payload := map[string]any{}
	var changed []string
	var steps []provider.Step

	if settings.MergeMethod != nil {
		method := *settings.MergeMethod
		payload["allow_merge_commit"] = method == "merge"
		payload["allow_rebase_merge"] = method == "rebase" || method == "fast_forward"
		changed = append(changed, "merge_method="+method)

		if method == "fast_forward" {
			steps = append(steps, provider.Info(scope, "merge_method=fast_forward",
				"GitHub has no fast-forward merge. Rebase merging was enabled instead"))
		}
	}

	if settings.Squash != nil {
		allow := *settings.Squash != "never"
		payload["allow_squash_merge"] = allow
		changed = append(changed, fmt.Sprintf("squash=%t", allow))

		if *settings.Squash == "default_on" || *settings.Squash == "default_off" {
			steps = append(steps, provider.Info(scope, "squash="+*settings.Squash,
				"GitHub cannot preselect squashing. It was enabled as an option"))
		}
	}

	if settings.DeleteSourceBranch != nil {
		payload["delete_branch_on_merge"] = *settings.DeleteSourceBranch
		changed = append(changed, fmt.Sprintf("delete_source_branch=%t", *settings.DeleteSourceBranch))
	}
	if settings.AllowAutoMerge != nil {
		payload["allow_auto_merge"] = *settings.AllowAutoMerge
		changed = append(changed, fmt.Sprintf("allow_auto_merge=%t", *settings.AllowAutoMerge))
	}

	for label, declared := range map[string]bool{
		"pipeline_must_succeed":    settings.PipelineMustSucceed != nil,
		"all_threads_resolved":     settings.AllThreadsResolved != nil,
		"skipped_pipeline_allowed": settings.SkippedPipelineOK != nil,
		"approvals_required":       settings.ApprovalsRequired != nil,
		"reset_approvals_on_push":  settings.ResetApprovalsOnPush != nil,
		"author_may_approve":       settings.AuthorMayApprove != nil,
		"committer_may_approve":    settings.CommitterMayApprove != nil,
	} {
		if declared {
			steps = append(steps, provider.Info(scope, label,
				"GitHub keeps this in a ruleset, not repository settings. Set it under branch_rules"))
		}
	}

	if len(payload) == 0 {
		return steps
	}

	const label = "merge settings"
	err := g.client.Do(ctx, provider.Request{
		Method: http.MethodPatch,
		Path:   "/repos/" + project.Owner + "/" + project.Name,
		Body:   payload,
	})

	switch {
	case err == nil:
		return append(steps, provider.OK(scope, label).With(changed...))
	case provider.IsStatus(err, http.StatusForbidden):
		return append(steps, provider.Skipped(scope, label,
			"the token has no admin rights on this repository").With(changed...))
	default:
		return append(steps, provider.Failed(scope, label, err).With(changed...))
	}
}

func (g *GitHub) ApplyPipelines(_ context.Context, _ provider.Project, pipelines *tmpl.Pipelines) []provider.Step {
	if pipelines.Empty() {
		return nil
	}
	return []provider.Step{provider.Info("pipelines", "pipelines block",
		"GitLab-only. Use the actions block for GitHub Actions settings")}
}

func (g *GitHub) ApplyIntegrations(_ context.Context, _ provider.Project, integrations []tmpl.Integration) []provider.Step {
	if len(integrations) == 0 {
		return nil
	}
	return []provider.Step{provider.Info("integrations", "integrations block",
		"GitLab-only. GitHub integrations are Apps and are installed, not configured here")}
}
