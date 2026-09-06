package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spaaleks/gitlift/internal/provider"
	"github.com/spaaleks/gitlift/internal/tmpl"
)

const branchScope = "branch rules"

type accessEntry struct {
	ID          int64  `json:"id"`
	AccessLevel int    `json:"access_level"`
	UserID      *int64 `json:"user_id"`
	GroupID     *int64 `json:"group_id"`
}

func (e accessEntry) named() bool { return e.UserID != nil || e.GroupID != nil }

type protectedBranch struct {
	ID                        int64         `json:"id"`
	Name                      string        `json:"name"`
	AllowForcePush            bool          `json:"allow_force_push"`
	CodeOwnerApprovalRequired bool          `json:"code_owner_approval_required"`
	PushAccessLevels          []accessEntry `json:"push_access_levels"`
	MergeAccessLevels         []accessEntry `json:"merge_access_levels"`
	UnprotectAccessLevels     []accessEntry `json:"unprotect_access_levels"`
}

func (b protectedBranch) hasNamedEntries() bool {
	for _, list := range [][]accessEntry{b.PushAccessLevels, b.MergeAccessLevels, b.UnprotectAccessLevels} {
		for _, entry := range list {
			if entry.named() {
				return true
			}
		}
	}
	return false
}

func (g *GitLab) listProtected(ctx context.Context, target provider.Project) ([]protectedBranch, error) {
	var branches []protectedBranch
	err := g.client.Do(ctx, provider.Request{
		Method: http.MethodGet,
		Path:   "/projects/" + target.ID + "/protected_branches",
		Out:    &branches,
	})
	if provider.IsStatus(err, http.StatusForbidden, http.StatusNotFound) {
		return nil, nil
	}
	return branches, err
}

func (g *GitLab) unprotect(ctx context.Context, target provider.Project, name string) error {
	err := g.client.Do(ctx, provider.Request{
		Method: http.MethodDelete,
		Path:   "/projects/" + target.ID + "/protected_branches/" + url.PathEscape(name),
	})
	if provider.IsStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}

func accessPatch(existing []accessEntry, want int) ([]map[string]any, bool) {
	var entries []map[string]any
	matched := false
	changed := false

	for _, entry := range existing {
		if entry.named() {
			continue
		}
		if entry.AccessLevel == want && !matched {
			matched = true
			continue
		}
		entries = append(entries, map[string]any{"id": entry.ID, "_destroy": true})
		changed = true
	}

	if !matched {
		entries = append(entries, map[string]any{"access_level": want})
		changed = true
	}

	return entries, changed
}

func roleLevel(entries []accessEntry) (int, bool) {
	for _, entry := range entries {
		if !entry.named() {
			return entry.AccessLevel, true
		}
	}
	return 0, false
}

type branchTarget struct {
	rule    tmpl.Rule
	pattern string
	label   string
}

func (g *GitLab) ApplyBranchRules(ctx context.Context, target provider.Project, rules []tmpl.Rule, mode provider.RuleMode) []provider.Step {
	if len(rules) == 0 {
		return nil
	}

	existing, err := g.listProtected(ctx, target)
	if err != nil {
		return []provider.Step{provider.Failed(branchScope, "read protected branches", err)}
	}

	current := map[string]protectedBranch{}
	for _, branch := range existing {
		current[branch.Name] = branch
	}

	var steps []provider.Step
	declared := map[string]bool{}
	protectedIDs := map[string][]int64{}

	for _, rule := range rules {
		if len(rule.Patterns) == 0 {
			steps = append(steps, provider.Skipped(branchScope, rule.Name, "no patterns"))
			continue
		}
		if len(rule.MergeMethods) > 0 {
			steps = append(steps, provider.Info(branchScope, rule.Name+" merge_methods",
				"GitLab's merge method is project-wide. Set merge_requests.merge_method instead"))
		}

		for _, pattern := range rule.Patterns {
			spot := branchTarget{rule: rule, pattern: pattern, label: rule.Name + " → " + pattern}

			branch, exists := current[pattern]
			var step provider.Step
			var id int64

			if exists {
				step, id = g.updateProtected(ctx, target, spot, branch)
			} else {
				step, id = g.createProtected(ctx, target, spot)
			}

			steps = append(steps, step)
			declared[pattern] = true
			if step.Status == provider.StatusOK && id != 0 {
				protectedIDs[rule.Name] = append(protectedIDs[rule.Name], id)
			}
		}

		if rule.RequiredApprovals != nil {
			steps = append(steps, g.applyApprovalRule(ctx, target, rule, protectedIDs[rule.Name]))
		}
	}

	if mode == provider.ModeReplace {
		for _, branch := range existing {
			if declared[branch.Name] {
				continue
			}
			if err := g.unprotect(ctx, target, branch.Name); err != nil {
				steps = append(steps, provider.Failed(branchScope, "unprotect "+branch.Name, err))
				continue
			}
			steps = append(steps, provider.Step{
				Scope: branchScope, Label: branch.Name, Status: provider.StatusOK, Detail: "unprotected",
			})
		}
	}

	return steps
}

func (g *GitLab) createProtected(ctx context.Context, target provider.Project, spot branchTarget) (provider.Step, int64) {
	payload := map[string]any{"name": spot.pattern}

	if spot.rule.AllowForcePush != nil {
		payload["allow_force_push"] = *spot.rule.AllowForcePush
	}
	if spot.rule.CodeOwnerApproval != nil {
		payload["code_owner_approval_required"] = *spot.rule.CodeOwnerApproval
	}
	if level, ok := provider.AccessLevel(spot.rule.PushAccess); ok {
		payload["push_access_level"] = level
	}
	if level, ok := provider.AccessLevel(spot.rule.MergeAccess); ok {
		payload["merge_access_level"] = level
	}
	if level, ok := provider.AccessLevel(spot.rule.UnprotectAccess); ok {
		payload["unprotect_access_level"] = level
	}

	var created protectedBranch
	err := g.client.Do(ctx, provider.Request{
		Method: http.MethodPost,
		Path:   "/projects/" + target.ID + "/protected_branches",
		Body:   payload,
		Out:    &created,
	})

	switch {
	case err == nil:
		return provider.Step{
			Scope: branchScope, Label: spot.label, Status: provider.StatusOK, Detail: "protected",
		}, created.ID
	case provider.IsStatus(err, http.StatusForbidden):
		return provider.Skipped(branchScope, spot.label,
			"the token needs at least Maintainer on this project"), 0
	default:
		return provider.Failed(branchScope, spot.label, err), 0
	}
}

func (g *GitLab) updateProtected(ctx context.Context, target provider.Project, spot branchTarget, branch protectedBranch) (provider.Step, int64) {
	payload := map[string]any{}
	var changed []string
	touchesAccess := false

	if spot.rule.AllowForcePush != nil && *spot.rule.AllowForcePush != branch.AllowForcePush {
		payload["allow_force_push"] = *spot.rule.AllowForcePush
		changed = append(changed, fmt.Sprintf("force-push=%t", *spot.rule.AllowForcePush))
	}
	if spot.rule.CodeOwnerApproval != nil && *spot.rule.CodeOwnerApproval != branch.CodeOwnerApprovalRequired {
		payload["code_owner_approval_required"] = *spot.rule.CodeOwnerApproval
		changed = append(changed, fmt.Sprintf("code-owner-approval=%t", *spot.rule.CodeOwnerApproval))
	}

	levels := []struct {
		declared string
		field    string
		label    string
		existing []accessEntry
	}{
		{spot.rule.PushAccess, "allowed_to_push", "push", branch.PushAccessLevels},
		{spot.rule.MergeAccess, "allowed_to_merge", "merge", branch.MergeAccessLevels},
		{spot.rule.UnprotectAccess, "allowed_to_unprotect", "unprotect", branch.UnprotectAccessLevels},
	}

	for _, level := range levels {
		want, ok := provider.AccessLevel(level.declared)
		if !ok {
			continue
		}
		if live, found := roleLevel(level.existing); found && live == want {
			continue
		}
		entries, needed := accessPatch(level.existing, want)
		if !needed {
			continue
		}
		payload[level.field] = entries
		changed = append(changed, level.label+"="+level.declared)
		touchesAccess = true
	}

	if len(payload) == 0 {
		return provider.Step{
			Scope: branchScope, Label: spot.label, Status: provider.StatusOK,
			Detail: "already matches the template",
		}, branch.ID
	}

	err := g.client.Do(ctx, provider.Request{
		Method: http.MethodPatch,
		Path:   "/projects/" + target.ID + "/protected_branches/" + url.PathEscape(spot.pattern),
		Body:   payload,
	})
	if err == nil {
		return provider.Step{
			Scope: branchScope, Label: spot.label, Status: provider.StatusOK,
			Detail: "updated (" + strings.Join(changed, ", ") + ")",
		}, branch.ID
	}

	if provider.IsStatus(err, http.StatusForbidden) && !touchesAccess {
		return provider.Skipped(branchScope, spot.label,
			"the token needs at least Maintainer on this project"), branch.ID
	}

	if !provider.IsStatus(err, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusForbidden) {
		return provider.Failed(branchScope, spot.label, err), branch.ID
	}

	return g.recreateProtected(ctx, target, spot, branch, err)
}

func (g *GitLab) restoreProtected(ctx context.Context, target provider.Project, branch protectedBranch) error {
	payload := map[string]any{
		"name":                         branch.Name,
		"allow_force_push":             branch.AllowForcePush,
		"code_owner_approval_required": branch.CodeOwnerApprovalRequired,
	}
	if level, ok := roleLevel(branch.PushAccessLevels); ok {
		payload["push_access_level"] = level
	}
	if level, ok := roleLevel(branch.MergeAccessLevels); ok {
		payload["merge_access_level"] = level
	}
	if level, ok := roleLevel(branch.UnprotectAccessLevels); ok {
		payload["unprotect_access_level"] = level
	}

	return g.client.Do(ctx, provider.Request{
		Method: http.MethodPost,
		Path:   "/projects/" + target.ID + "/protected_branches",
		Body:   payload,
	})
}

func (g *GitLab) recreateProtected(ctx context.Context, target provider.Project, spot branchTarget, branch protectedBranch, cause error) (provider.Step, int64) {
	if err := g.unprotect(ctx, target, spot.pattern); err != nil {
		if provider.IsStatus(err, http.StatusForbidden) {
			return provider.Skipped(branchScope, spot.label,
				"the token needs at least Maintainer on this project"), branch.ID
		}
		return provider.Failed(branchScope, spot.label, err), 0
	}

	step, id := g.createProtected(ctx, target, spot)
	if step.Status != provider.StatusOK {
		if restoreErr := g.restoreProtected(ctx, target, branch); restoreErr != nil {
			return provider.Step{
				Scope: branchScope, Label: spot.label, Status: provider.StatusFailed,
				Detail: "the rule could not be replaced (" + provider.Message(cause) +
					") and restoring the previous one also failed (" + provider.Message(restoreErr) +
					"). THE BRANCH IS NOW UNPROTECTED",
			}, 0
		}
		return provider.Step{
			Scope: branchScope, Label: spot.label, Status: provider.StatusFailed,
			Detail: "rejected (" + provider.Message(cause) + "). The previous rule was restored",
		}, branch.ID
	}

	detail := "reprotected (in-place update rejected: " + provider.Message(cause) + ")"
	status := provider.StatusOK
	if branch.hasNamedEntries() {
		detail = "reprotected, but per-user and per-group allowances were lost. " +
			"In-place update rejected: " + provider.Message(cause)
		status = provider.StatusSkipped
	}

	return provider.Step{Scope: branchScope, Label: spot.label, Status: status, Detail: detail}, id
}

func (g *GitLab) applyApprovalRule(ctx context.Context, target provider.Project, rule tmpl.Rule, branchIDs []int64) provider.Step {
	label := rule.Name + " required approvals"

	if len(branchIDs) == 0 {
		return provider.Skipped(branchScope, label, "no protected branch was created to attach it to")
	}

	name := rule.Name
	if name == "" {
		name = "gitlift"
	}

	body := map[string]any{
		"name":                              name,
		"approvals_required":                *rule.RequiredApprovals,
		"protected_branch_ids":              branchIDs,
		"applies_to_all_protected_branches": false,
	}

	base := "/projects/" + target.ID + "/approval_rules"

	var existing []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	listErr := g.client.Do(ctx, provider.Request{Method: http.MethodGet, Path: base, Out: &existing})
	if listErr != nil {
		return approvalStep(label, listErr)
	}

	method, path := http.MethodPost, base
	for _, entry := range existing {
		if entry.Name == name {
			method, path = http.MethodPut, fmt.Sprintf("%s/%d", base, entry.ID)
			break
		}
	}

	if err := g.client.Do(ctx, provider.Request{Method: method, Path: path, Body: body}); err != nil {
		return approvalStep(label, err)
	}
	return provider.OK(branchScope, label)
}

func approvalStep(label string, err error) provider.Step {
	if provider.IsStatus(err, http.StatusNotFound, http.StatusForbidden, http.StatusPaymentRequired) {
		return provider.Skipped(branchScope, label,
			"merge request approval rules need GitLab Premium, or the token lacks the rights")
	}
	return provider.Failed(branchScope, label, err)
}
