package gitlab

import (
	"context"
	"fmt"
	"net/http"

	"github.com/spaaleks/gitlift/internal/provider"
	"github.com/spaaleks/gitlift/internal/tmpl"
)

const mrScope = "merge requests"

var mergeMethods = map[string]string{
	"merge":        "merge",
	"rebase":       "rebase_merge",
	"fast_forward": "ff",
}

func (g *GitLab) ApplyMergeRequests(ctx context.Context, target provider.Project, settings *tmpl.MergeRequests) []provider.Step {
	if settings.Empty() {
		return nil
	}

	var steps []provider.Step

	if step, ok := g.mergeProjectSettings(ctx, target, settings); ok {
		steps = append(steps, step)
	}
	if settings.Approvals() {
		steps = append(steps, g.mergeApprovalSettings(ctx, target, settings)...)
	}
	if settings.AllowAutoMerge != nil {
		steps = append(steps, provider.Info(mrScope, "allow_auto_merge",
			"GitHub-only. GitLab's equivalent is set per merge request"))
	}

	return steps
}

func (g *GitLab) mergeProjectSettings(ctx context.Context, target provider.Project, settings *tmpl.MergeRequests) (provider.Step, bool) {
	payload := map[string]any{}
	var changed []string

	if settings.MergeMethod != nil {
		method := mergeMethods[*settings.MergeMethod]
		payload["merge_method"] = method
		changed = append(changed, "merge_method="+*settings.MergeMethod)
	}
	if settings.Squash != nil {
		payload["squash_option"] = *settings.Squash
		changed = append(changed, "squash="+*settings.Squash)
	}
	if settings.DeleteSourceBranch != nil {
		payload["remove_source_branch_after_merge"] = *settings.DeleteSourceBranch
		changed = append(changed, fmt.Sprintf("delete_source_branch=%t", *settings.DeleteSourceBranch))
	}
	if settings.PipelineMustSucceed != nil {
		payload["only_allow_merge_if_pipeline_succeeds"] = *settings.PipelineMustSucceed
		changed = append(changed, fmt.Sprintf("pipeline_must_succeed=%t", *settings.PipelineMustSucceed))
	}
	if settings.AllThreadsResolved != nil {
		payload["only_allow_merge_if_all_discussions_are_resolved"] = *settings.AllThreadsResolved
		changed = append(changed, fmt.Sprintf("all_threads_resolved=%t", *settings.AllThreadsResolved))
	}
	if settings.SkippedPipelineOK != nil {
		payload["allow_merge_on_skipped_pipeline"] = *settings.SkippedPipelineOK
		changed = append(changed, fmt.Sprintf("skipped_pipeline_allowed=%t", *settings.SkippedPipelineOK))
	}

	if len(payload) == 0 {
		return provider.Step{}, false
	}

	const label = "merge request settings"
	err := g.client.Do(ctx, provider.Request{
		Method: http.MethodPut, Path: "/projects/" + target.ID, Body: payload,
	})

	switch {
	case err == nil:
		return provider.OK(mrScope, label).With(changed...), true
	case provider.IsStatus(err, http.StatusForbidden):
		return provider.Skipped(mrScope, label,
			"the token needs at least Maintainer on this project").With(changed...), true
	default:
		return provider.Failed(mrScope, label, err).With(changed...), true
	}
}

func (g *GitLab) mergeApprovalSettings(ctx context.Context, target provider.Project, settings *tmpl.MergeRequests) []provider.Step {
	var steps []provider.Step

	payload := map[string]any{}
	var changed []string

	if settings.ResetApprovalsOnPush != nil {
		payload["reset_approvals_on_push"] = *settings.ResetApprovalsOnPush
		changed = append(changed, fmt.Sprintf("reset_on_push=%t", *settings.ResetApprovalsOnPush))
	}
	if settings.AuthorMayApprove != nil {
		payload["merge_requests_author_approval"] = *settings.AuthorMayApprove
		changed = append(changed, fmt.Sprintf("author_may_approve=%t", *settings.AuthorMayApprove))
	}
	if settings.CommitterMayApprove != nil {
		payload["merge_requests_disable_committers_approval"] = !*settings.CommitterMayApprove
		changed = append(changed, fmt.Sprintf("committer_may_approve=%t", *settings.CommitterMayApprove))
	}

	if len(payload) > 0 {
		err := g.client.Do(ctx, provider.Request{
			Method: http.MethodPost, Path: "/projects/" + target.ID + "/approvals", Body: payload,
		})
		steps = append(steps, approvalOutcome("approval settings", err).With(changed...))
	}

	if settings.ApprovalsRequired != nil {
		label := fmt.Sprintf("approvals required = %d", *settings.ApprovalsRequired)
		err := g.upsertProjectApprovalRule(ctx, target, *settings.ApprovalsRequired)
		steps = append(steps, approvalOutcome(label, err))
	}

	return steps
}

func (g *GitLab) upsertProjectApprovalRule(ctx context.Context, target provider.Project, count int) error {
	const name = "All eligible users"

	base := "/projects/" + target.ID + "/approval_rules"

	var existing []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := g.client.Do(ctx, provider.Request{Method: http.MethodGet, Path: base, Out: &existing}); err != nil {
		return err
	}

	body := map[string]any{
		"name":                              name,
		"approvals_required":                count,
		"rule_type":                         "any_approver",
		"applies_to_all_protected_branches": true,
	}

	for _, entry := range existing {
		if entry.Name == name {
			return g.client.Do(ctx, provider.Request{
				Method: http.MethodPut, Path: fmt.Sprintf("%s/%d", base, entry.ID), Body: body,
			})
		}
	}

	return g.client.Do(ctx, provider.Request{Method: http.MethodPost, Path: base, Body: body})
}

func approvalOutcome(label string, err error) provider.Step {
	switch {
	case err == nil:
		return provider.OK(mrScope, label)
	case provider.IsStatus(err, http.StatusNotFound, http.StatusForbidden, http.StatusPaymentRequired):
		return provider.Skipped(mrScope, label,
			"merge request approvals need GitLab Premium, or the token lacks the rights")
	default:
		return provider.Failed(mrScope, label, err)
	}
}
