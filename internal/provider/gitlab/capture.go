package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/spaaleks/gitlift/internal/provider"
	"github.com/spaaleks/gitlift/internal/tmpl"
)

const captureScope = "capture"

func (g *GitLab) Capture(ctx context.Context, target provider.Project) (tmpl.Spec, []provider.Step) {
	var steps []provider.Step

	var live project
	if err := g.client.Do(ctx, provider.Request{
		Method: http.MethodGet, Path: "/projects/" + target.ID, Out: &live,
	}); err != nil {
		return tmpl.Spec{}, []provider.Step{provider.Failed(captureScope, "read project", err)}
	}

	spec := tmpl.Spec{
		Repo: &tmpl.Repo{
			Visibility: provider.String(live.Visibility),
			Features:   readFeatures(live.raw),
		},
	}
	if live.DefaultBranch != "" {
		spec.Repo.DefaultBranch = provider.String(live.DefaultBranch)
	}
	spec.Repo.Topics = live.Topics
	steps = append(steps, provider.OK(captureScope, "project settings").
		With(spec.Repo.Features.Keys()...))

	if mr := captureMergeRequests(live.raw); mr != nil {
		spec.MergeRequests = mr
		steps = append(steps, provider.OK(captureScope, "merge request settings"))
	}
	if pipelines := capturePipelines(live.raw); pipelines != nil {
		spec.Pipelines = pipelines
		steps = append(steps, provider.OK(captureScope, "pipeline settings"))
	}

	branches, err := g.listProtected(ctx, target)
	switch {
	case err != nil:
		steps = append(steps, provider.Skipped(captureScope, "branch rules", provider.Explain(err)))
	case len(branches) > 0:
		spec.BranchRules = &tmpl.BranchRules{Rules: captureBranchRules(branches)}
		steps = append(steps, provider.OK(captureScope, "branch rules").With(ruleNames(branches)...))
	}

	tags, err := g.listProtectedTags(ctx, target)
	switch {
	case err != nil:
		steps = append(steps, provider.Skipped(captureScope, "tag rules", provider.Explain(err)))
	case len(tags) > 0:
		spec.TagRules = &tmpl.TagRules{Rules: captureTagRules(tags)}
		steps = append(steps, provider.OK(captureScope, "tag rules"))
	}

	steps = append(steps, provider.Info(captureScope, "not captured",
		"webhooks, integrations and CI/CD variables carry secrets and are left out"))

	return spec, steps
}

func ruleNames(branches []protectedBranch) []string {
	names := make([]string, 0, len(branches))
	for _, branch := range branches {
		names = append(names, branch.Name)
	}
	return names
}

func captureBranchRules(branches []protectedBranch) []tmpl.Rule {
	rules := make([]tmpl.Rule, 0, len(branches))

	for _, branch := range branches {
		rule := tmpl.Rule{
			Name:           branch.Name,
			Patterns:       []string{branch.Name},
			AllowForcePush: provider.Bool(branch.AllowForcePush),
		}
		if level, ok := roleLevel(branch.PushAccessLevels); ok {
			rule.PushAccess = provider.AccessName(level)
		}
		if level, ok := roleLevel(branch.MergeAccessLevels); ok {
			rule.MergeAccess = provider.AccessName(level)
		}
		if level, ok := roleLevel(branch.UnprotectAccessLevels); ok {
			rule.UnprotectAccess = provider.AccessName(level)
		}
		if branch.CodeOwnerApprovalRequired {
			rule.CodeOwnerApproval = provider.Bool(true)
		}
		rules = append(rules, rule)
	}

	return rules
}

func captureTagRules(tags []protectedTag) []tmpl.TagRule {
	rules := make([]tmpl.TagRule, 0, len(tags))

	for _, tag := range tags {
		rule := tmpl.TagRule{Name: tag.Name, Patterns: []string{tag.Name}}
		if level, ok := roleLevel(tag.CreateAccessLevel); ok {
			rule.CreateAccess = provider.AccessName(level)
		}
		rules = append(rules, rule)
	}

	return rules
}

func captureMergeRequests(raw map[string]any) *tmpl.MergeRequests {
	mr := &tmpl.MergeRequests{}

	for name, method := range map[string]string{"merge": "merge", "rebase_merge": "rebase", "ff": "fast_forward"} {
		if raw["merge_method"] == name {
			mr.MergeMethod = provider.String(method)
		}
	}
	if option, ok := raw["squash_option"].(string); ok {
		mr.Squash = provider.String(option)
	}
	if on, ok := raw["remove_source_branch_after_merge"].(bool); ok {
		mr.DeleteSourceBranch = provider.Bool(on)
	}
	if on, ok := raw["only_allow_merge_if_pipeline_succeeds"].(bool); ok {
		mr.PipelineMustSucceed = provider.Bool(on)
	}
	if on, ok := raw["only_allow_merge_if_all_discussions_are_resolved"].(bool); ok {
		mr.AllThreadsResolved = provider.Bool(on)
	}
	if on, ok := raw["allow_merge_on_skipped_pipeline"].(bool); ok {
		mr.SkippedPipelineOK = provider.Bool(on)
	}

	if mr.Empty() {
		return nil
	}
	return mr
}

func capturePipelines(raw map[string]any) *tmpl.Pipelines {
	pipelines := &tmpl.Pipelines{}

	if on, ok := raw["auto_devops_enabled"].(bool); ok {
		pipelines.AutoDevOps = provider.Bool(on)
	}
	if state, ok := raw["auto_cancel_pending_pipelines"].(string); ok {
		pipelines.AutoCancelPending = provider.Bool(state == "enabled")
	}
	if on, ok := raw["public_builds"].(bool); ok {
		pipelines.PublicPipelines = provider.Bool(on)
	}
	if strategy, ok := raw["build_git_strategy"].(string); ok && strategy != "" {
		pipelines.GitStrategy = provider.String(strategy)
	}
	if depth, ok := raw["ci_default_git_depth"].(float64); ok {
		pipelines.GitDepth = provider.Int(int(depth))
	}
	if seconds, ok := raw["build_timeout"].(float64); ok && seconds > 0 {
		pipelines.Timeout = provider.String(durationText(int(seconds)))
	}
	if path, ok := raw["ci_config_path"].(string); ok && path != "" {
		pipelines.ConfigPath = provider.String(path)
	}
	if on, ok := raw["shared_runners_enabled"].(bool); ok {
		pipelines.SharedRunners = provider.Bool(on)
	}
	if on, ok := raw["group_runners_enabled"].(bool); ok {
		pipelines.GroupRunners = provider.Bool(on)
	}

	if pipelines.Empty() {
		return nil
	}
	return pipelines
}

func durationText(seconds int) string {
	d := time.Duration(seconds) * time.Second
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprint(seconds)
}
