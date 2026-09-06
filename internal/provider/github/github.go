package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"

	"golang.org/x/crypto/nacl/box"

	"github.com/spaaleks/gitlift/internal/provider"
	"github.com/spaaleks/gitlift/internal/tmpl"
)

type GitHub struct {
	name   string
	client *provider.Client
}

func New(name, token, baseURL string) *GitHub {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	header.Set("Accept", "application/vnd.github+json")
	header.Set("X-GitHub-Api-Version", "2022-11-28")

	return &GitHub{name: name, client: provider.NewClient("github "+name, baseURL, header)}
}

func (g *GitHub) Name() string { return g.name }

func (g *GitHub) Kind() string { return "github" }

func (g *GitHub) Host() string { return g.client.Host() }

type repository struct {
	Name          string   `json:"name"`
	FullName      string   `json:"full_name"`
	Description   string   `json:"description"`
	Private       bool     `json:"private"`
	Visibility    string   `json:"visibility"`
	DefaultBranch string   `json:"default_branch"`
	HTMLURL       string   `json:"html_url"`
	Archived      bool     `json:"archived"`
	Size          int      `json:"size"`
	Topics        []string `json:"topics"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`

	raw map[string]any
}

func (r *repository) UnmarshalJSON(data []byte) error {
	type alias repository
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = repository(decoded)
	return json.Unmarshal(data, &r.raw)
}

func (g *GitHub) toProject(repo repository) provider.Project {
	visibility := repo.Visibility
	if visibility == "" {
		visibility = "public"
		if repo.Private {
			visibility = "private"
		}
	}

	return provider.Project{
		ProviderName:  g.name,
		Kind:          "github",
		ID:            repo.FullName,
		Owner:         repo.Owner.Login,
		Name:          repo.Name,
		FullName:      repo.FullName,
		Description:   repo.Description,
		Visibility:    visibility,
		DefaultBranch: repo.DefaultBranch,
		URL:           repo.HTMLURL,
		Archived:      repo.Archived,
		Empty:         repo.DefaultBranch == "",
		Topics:        repo.Topics,
		Features:      readFeatures(repo.raw),
	}
}

func (g *GitHub) ListProjects(ctx context.Context) ([]provider.Project, error) {
	var projects []provider.Project

	query := url.Values{}
	query.Set("affiliation", "owner,organization_member")
	query.Set("sort", "full_name")

	err := g.client.Paginate(ctx, "/user/repos", query, func(raw json.RawMessage) (int, error) {
		var batch []repository
		if err := json.Unmarshal(raw, &batch); err != nil {
			return 0, err
		}
		for _, repo := range batch {
			projects = append(projects, g.toProject(repo))
		}
		return len(batch), nil
	})
	if err != nil {
		return nil, err
	}

	return projects, nil
}

func (g *GitHub) Lookup(ctx context.Context, target provider.Target, name string) (provider.Project, bool, error) {
	var repo repository
	err := g.client.Do(ctx, provider.Request{
		Method: http.MethodGet,
		Path:   "/repos/" + target.Owner + "/" + name,
		Out:    &repo,
	})
	if provider.IsStatus(err, http.StatusNotFound) {
		return provider.Project{}, false, nil
	}
	if err != nil {
		return provider.Project{}, false, err
	}
	return g.toProject(repo), true, nil
}

func (g *GitHub) Create(ctx context.Context, target provider.Target, settings provider.Settings) (provider.Project, error) {
	path := "/user/repos"
	if target.OwnerType == "org" {
		path = "/orgs/" + target.Owner + "/repos"
	}

	payload := map[string]any{"name": settings.Name}
	if settings.Description != nil {
		payload["description"] = *settings.Description
	}
	featurePayload(payload, settings.Features)
	delete(payload, "security_and_analysis")
	if settings.Visibility != nil {
		if target.OwnerType == "org" {
			payload["visibility"] = *settings.Visibility
		} else {
			payload["private"] = *settings.Visibility != "public"
		}
	}

	var repo repository
	if err := g.client.Do(ctx, provider.Request{
		Method: http.MethodPost, Path: path, Body: payload, Out: &repo,
	}); err != nil {
		return provider.Project{}, err
	}

	return g.toProject(repo), nil
}

func (g *GitHub) UpdateSettings(ctx context.Context, project provider.Project, settings provider.Settings) []provider.Step {
	const scope = "settings"

	payload := map[string]any{}
	var changed []string

	if settings.Description != nil && *settings.Description != project.Description {
		payload["description"] = *settings.Description
		changed = append(changed, "description")
	}
	if settings.Visibility != nil && *settings.Visibility != project.Visibility {
		payload["visibility"] = *settings.Visibility
		changed = append(changed, "visibility="+*settings.Visibility)
	}
	if settings.DefaultBranch != nil && *settings.DefaultBranch != project.DefaultBranch {
		payload["default_branch"] = *settings.DefaultBranch
		changed = append(changed, "default_branch="+*settings.DefaultBranch)
	}

	featureChanges, endpoints, ignored := featurePayload(payload, settings.Features)
	changed = append(changed, featureChanges...)

	var steps []provider.Step
	for _, key := range ignored {
		steps = append(steps, provider.Info(scope, "feature "+key, "not a GitHub setting, ignored here"))
	}
	for key, on := range endpoints {
		steps = append(steps, g.toggleFeature(ctx, project, key, on))
	}

	if len(settings.Topics) > 0 && !slices.Equal(settings.Topics, project.Topics) {
		steps = append(steps, g.putTopics(ctx, project, settings.Topics))
	}

	if len(payload) == 0 {
		return append(steps, provider.Skipped(scope, "repository settings", "template declares nothing to change"))
	}

	const label = "repository settings"
	err := g.client.Do(ctx, provider.Request{
		Method: http.MethodPatch,
		Path:   "/repos/" + project.Owner + "/" + project.Name,
		Body:   payload,
	})
	if err != nil {
		return append(steps, provider.Failed(scope, label, err).With(changed...))
	}

	return append(steps, provider.OK(scope, label).With(changed...))
}

func (g *GitHub) toggleFeature(ctx context.Context, project provider.Project, key string, on bool) provider.Step {
	const scope = "settings"

	method := http.MethodDelete
	if on {
		method = http.MethodPut
	}

	label := key + "=" + boolText(on)
	err := g.client.Do(ctx, provider.Request{
		Method: method,
		Path:   "/repos/" + project.Owner + "/" + project.Name + "/" + toggleEndpoints[key],
	})

	switch {
	case err == nil:
		return provider.OK(scope, label)
	case provider.IsStatus(err, http.StatusForbidden, http.StatusNotFound):
		return provider.Skipped(scope, label,
			"not available here. The token needs repository administration, or the plan does not include it")
	default:
		return provider.Failed(scope, label, err)
	}
}

func (g *GitHub) putTopics(ctx context.Context, project provider.Project, topics []string) provider.Step {
	err := g.client.Do(ctx, provider.Request{
		Method: http.MethodPut,
		Path:   "/repos/" + project.Owner + "/" + project.Name + "/topics",
		Body:   map[string]any{"names": topics},
	})
	if err != nil {
		return provider.Failed("settings", "topics", err)
	}
	return provider.OK("settings", "topics").With(topics...)
}

type ruleset struct {
	ID          int64  `json:"id,omitempty"`
	Name        string `json:"name"`
	Target      string `json:"target"`
	Enforcement string `json:"enforcement"`
	Conditions  struct {
		RefName struct {
			Include []string `json:"include"`
			Exclude []string `json:"exclude"`
		} `json:"ref_name"`
	} `json:"conditions"`
	Rules []map[string]any `json:"rules"`
}

func buildRuleset(rule tmpl.Rule) *ruleset {
	if len(rule.Patterns) == 0 {
		return nil
	}

	name := rule.Name
	if name == "" {
		name = "branch-rules"
	}

	built := &ruleset{Name: name, Target: "branch", Enforcement: "active"}
	for _, pattern := range rule.Patterns {
		if !strings.HasPrefix(pattern, "refs/") && !strings.HasPrefix(pattern, "~") {
			pattern = "refs/heads/" + pattern
		}
		built.Conditions.RefName.Include = append(built.Conditions.RefName.Include, pattern)
	}
	built.Conditions.RefName.Exclude = []string{}

	built.Rules = []map[string]any{{"type": "deletion"}}
	if !provider.DerefBool(rule.AllowForcePush, false) {
		built.Rules = append(built.Rules, map[string]any{"type": "non_fast_forward"})
	}

	if len(rule.MergeMethods) > 0 || rule.CodeOwnerApproval != nil || rule.RequiredApprovals != nil {
		parameters := map[string]any{
			"required_approving_review_count":   provider.DerefInt(rule.RequiredApprovals, 0),
			"dismiss_stale_reviews_on_push":     false,
			"require_code_owner_review":         provider.DerefBool(rule.CodeOwnerApproval, false),
			"require_last_push_approval":        false,
			"required_review_thread_resolution": false,
		}
		if len(rule.MergeMethods) > 0 {
			parameters["allowed_merge_methods"] = rule.MergeMethods
		}
		built.Rules = append(built.Rules, map[string]any{"type": "pull_request", "parameters": parameters})
	}

	return built
}

func buildTagRuleset(rule tmpl.TagRule) *ruleset {
	if len(rule.Patterns) == 0 {
		return nil
	}

	name := rule.Name
	if name == "" {
		name = "tag-rules"
	}

	built := &ruleset{Name: name, Target: "tag", Enforcement: "active"}
	for _, pattern := range rule.Patterns {
		if !strings.HasPrefix(pattern, "refs/") && !strings.HasPrefix(pattern, "~") {
			pattern = "refs/tags/" + pattern
		}
		built.Conditions.RefName.Include = append(built.Conditions.RefName.Include, pattern)
	}
	built.Conditions.RefName.Exclude = []string{}
	built.Rules = []map[string]any{{"type": "deletion"}, {"type": "non_fast_forward"}}

	return built
}

func (r *ruleset) withoutPullRequest() *ruleset {
	stripped := *r
	stripped.Rules = nil
	for _, rule := range r.Rules {
		if rule["type"] != "pull_request" {
			stripped.Rules = append(stripped.Rules, rule)
		}
	}
	return &stripped
}

func (g *GitHub) listRulesets(ctx context.Context, project provider.Project) ([]ruleset, error) {
	var existing []ruleset
	err := g.client.Do(ctx, provider.Request{
		Method: http.MethodGet,
		Path:   "/repos/" + project.Owner + "/" + project.Name + "/rulesets",
		Out:    &existing,
	})
	if provider.IsStatus(err, http.StatusForbidden, http.StatusNotFound) {
		return nil, nil
	}
	return existing, err
}

func (g *GitHub) saveRuleset(ctx context.Context, project provider.Project, id int64, body *ruleset) error {
	path := "/repos/" + project.Owner + "/" + project.Name + "/rulesets"
	method := http.MethodPost
	if id != 0 {
		method = http.MethodPut
		path += "/" + fmt.Sprint(id)
	}
	return g.client.Do(ctx, provider.Request{Method: method, Path: path, Body: body})
}

func (g *GitHub) ApplyBranchRules(ctx context.Context, project provider.Project, rules []tmpl.Rule, mode provider.RuleMode) []provider.Step {
	const scope = "branch rules"

	if len(rules) == 0 {
		return nil
	}

	existing, err := g.listRulesets(ctx, project)
	if err != nil {
		return []provider.Step{provider.Failed(scope, "read existing rulesets", err)}
	}

	byName := map[string]int64{}
	for _, entry := range existing {
		byName[entry.Name] = entry.ID
	}

	var steps []provider.Step
	applied := map[string]bool{}

	for _, rule := range rules {
		built := buildRuleset(rule)
		if built == nil {
			steps = append(steps, provider.Skipped(scope, rule.Name, "no patterns"))
			continue
		}

		label := provider.DescribeRule(rule)
		id := byName[built.Name]

		err := g.saveRuleset(ctx, project, id, built)

		if err != nil && provider.IsStatus(err, http.StatusUnprocessableEntity) && len(rule.MergeMethods) > 0 {
			if retryErr := g.saveRuleset(ctx, project, id, built.withoutPullRequest()); retryErr == nil {
				applied[built.Name] = true
				steps = append(steps, provider.Step{
					Scope: scope, Label: label, Status: provider.StatusSkipped,
					Detail: "merge-method restriction rejected (" + provider.Message(err) + "). The rest of the rule was applied",
				})
				continue
			}
		}

		if err == nil {
			applied[built.Name] = true
			verb := "created"
			if id != 0 {
				verb = "updated"
			}
			steps = append(steps, provider.Step{Scope: scope, Label: label, Status: provider.StatusOK, Detail: verb})
			continue
		}

		steps = append(steps, g.fallbackProtection(ctx, project, rule, label, err)...)
	}

	if mode == provider.ModeReplace {
		for _, entry := range existing {
			if applied[entry.Name] {
				continue
			}
			delErr := g.client.Do(ctx, provider.Request{
				Method: http.MethodDelete,
				Path:   fmt.Sprintf("/repos/%s/%s/rulesets/%d", project.Owner, project.Name, entry.ID),
			})
			if delErr != nil {
				steps = append(steps, provider.Failed(scope, "remove ruleset "+entry.Name, delErr))
				continue
			}
			steps = append(steps, provider.Step{
				Scope: scope, Label: entry.Name, Status: provider.StatusOK, Detail: "removed",
			})
		}
	}

	for _, key := range gitlabOnlyAccessKeys(rules) {
		steps = append(steps, provider.Info(scope, key,
			"GitHub has no per-branch access level. The key is GitLab-only and was ignored"))
	}

	return steps
}

func gitlabOnlyAccessKeys(rules []tmpl.Rule) []string {
	var keys []string
	seen := map[string]bool{}

	for _, rule := range rules {
		for key, value := range map[string]string{
			"push_access":      rule.PushAccess,
			"merge_access":     rule.MergeAccess,
			"unprotect_access": rule.UnprotectAccess,
		} {
			if value != "" && !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}

	sort.Strings(keys)
	return keys
}

func (g *GitHub) fallbackProtection(ctx context.Context, project provider.Project, rule tmpl.Rule, label string, cause error) []provider.Step {
	const scope = "branch rules"

	if !provider.PlanLimited(cause) {
		return []provider.Step{provider.Failed(scope, label, cause)}
	}

	var steps []provider.Step
	for _, pattern := range rule.Patterns {
		branch := strings.TrimPrefix(pattern, "refs/heads/")

		if provider.IsGlob(branch) {
			steps = append(steps, provider.Skipped(scope, label+" → "+branch,
				"glob patterns need rulesets, which this plan does not include for private repos"))
			continue
		}

		payload := map[string]any{
			"required_status_checks":        nil,
			"enforce_admins":                false,
			"required_pull_request_reviews": nil,
			"restrictions":                  nil,
			"allow_force_pushes":            provider.DerefBool(rule.AllowForcePush, false),
			"allow_deletions":               false,
		}
		if len(rule.MergeMethods) > 0 {
			payload["required_pull_request_reviews"] = map[string]any{
				"dismiss_stale_reviews":           false,
				"require_code_owner_reviews":      false,
				"required_approving_review_count": 0,
			}
		}

		err := g.client.Do(ctx, provider.Request{
			Method: http.MethodPut,
			Path:   "/repos/" + project.Owner + "/" + project.Name + "/branches/" + url.PathEscape(branch) + "/protection",
			Body:   payload,
		})

		switch {
		case err == nil:
			steps = append(steps, provider.Step{
				Scope: scope, Label: label + " → " + branch, Status: provider.StatusOK,
				Detail: "rulesets unavailable on this plan, used classic branch protection",
			})
		case provider.IsStatus(err, http.StatusNotFound):
			steps = append(steps, provider.Skipped(scope, label+" → "+branch,
				"branch does not exist yet. Push it and re-run"))
		default:
			steps = append(steps, provider.Skipped(scope, label+" → "+branch, provider.Message(cause)))
		}
	}

	return steps
}

func (g *GitHub) ApplyActions(ctx context.Context, project provider.Project, actions *tmpl.Actions) []provider.Step {
	const scope = "actions"

	if actions.Empty() {
		return nil
	}

	base := "/repos/" + project.Owner + "/" + project.Name + "/actions/permissions"
	var steps []provider.Step

	if actions.ForkPRApproval != nil {
		steps = append(steps, g.putActions(ctx, base+"/fork-pr-contributor-approval",
			map[string]any{"approval_policy": *actions.ForkPRApproval},
			"fork PR approval = "+*actions.ForkPRApproval))
	}

	if actions.OutsideAccess != nil {
		if project.Visibility == "public" {
			steps = append(steps, provider.Skipped(scope, "outside access = "+*actions.OutsideAccess,
				"only configurable on private repositories"))
		} else {
			steps = append(steps, g.putActions(ctx, base+"/access",
				map[string]any{"access_level": *actions.OutsideAccess},
				"outside access = "+*actions.OutsideAccess))
		}
	}

	if actions.DefaultWorkflowPermissions != nil || actions.CanApprovePullRequests != nil {
		payload := map[string]any{}
		var parts []string
		if actions.DefaultWorkflowPermissions != nil {
			payload["default_workflow_permissions"] = *actions.DefaultWorkflowPermissions
			parts = append(parts, "token="+*actions.DefaultWorkflowPermissions)
		}
		if actions.CanApprovePullRequests != nil {
			payload["can_approve_pull_request_reviews"] = *actions.CanApprovePullRequests
			parts = append(parts, fmt.Sprintf("can approve PRs=%t", *actions.CanApprovePullRequests))
		}
		steps = append(steps, g.putActions(ctx, base+"/workflow", payload,
			"workflow permissions ("+strings.Join(parts, ", ")+")"))
	}

	return steps
}

func (g *GitHub) putActions(ctx context.Context, path string, body map[string]any, label string) provider.Step {
	const scope = "actions"

	err := g.client.Do(ctx, provider.Request{Method: http.MethodPut, Path: path, Body: body})
	if err == nil {
		return provider.OK(scope, label)
	}

	switch {
	case provider.IsStatus(err, http.StatusForbidden):
		return provider.Skipped(scope, label, "the token has no admin rights on this repository")
	case provider.IsStatus(err, http.StatusNotFound):
		return provider.Skipped(scope, label, "not available here (Actions disabled, or the setting does not apply)")
	case provider.IsStatus(err, http.StatusUnprocessableEntity):
		return provider.Skipped(scope, label, provider.Message(err))
	default:
		return provider.Failed(scope, label, err)
	}
}

type publicKey struct {
	KeyID string `json:"key_id"`
	Key   string `json:"key"`
}

func (g *GitHub) ApplyVariables(ctx context.Context, project provider.Project, variables []tmpl.Variable) []provider.Step {
	const scope = "variables"

	if len(variables) == 0 {
		return nil
	}

	var steps []provider.Step
	var key *publicKey
	var keyErr error
	keyLoaded := false

	for _, variable := range variables {
		if variable.Value == "" {
			steps = append(steps, provider.Skipped(scope, describeVariable(variable)+" "+variable.Key,
				"no value given, the repository keeps whatever it has now"))
			continue
		}

		if !variable.Secret() {
			steps = append(steps, g.upsertVariable(ctx, project, variable))
			continue
		}

		if !keyLoaded {
			key, keyErr = g.publicKey(ctx, project)
			keyLoaded = true
		}
		if keyErr != nil {
			reason := "cannot read the Actions public key. The token needs the secrets scope"
			if !provider.IsStatus(keyErr, http.StatusForbidden, http.StatusNotFound) {
				reason = provider.Explain(keyErr)
			}
			steps = append(steps, provider.Skipped(scope, "secret "+variable.Key, reason))
			continue
		}

		steps = append(steps, g.upsertSecret(ctx, project, variable, key))
	}

	return steps
}

func describeVariable(variable tmpl.Variable) string {
	if variable.Secret() {
		return "secret"
	}
	return "variable"
}

func (g *GitHub) publicKey(ctx context.Context, project provider.Project) (*publicKey, error) {
	var key publicKey
	err := g.client.Do(ctx, provider.Request{
		Method: http.MethodGet,
		Path:   "/repos/" + project.Owner + "/" + project.Name + "/actions/secrets/public-key",
		Out:    &key,
	})
	if err != nil {
		return nil, err
	}
	return &key, nil
}

func (g *GitHub) upsertVariable(ctx context.Context, project provider.Project, variable tmpl.Variable) provider.Step {
	const scope = "variables"

	base := "/repos/" + project.Owner + "/" + project.Name + "/actions/variables"
	err := g.client.Do(ctx, provider.Request{
		Method: http.MethodPost, Path: base,
		Body: map[string]any{"name": variable.Key, "value": variable.Value},
	})

	if provider.IsStatus(err, http.StatusConflict) || provider.NameTaken(err) {
		err = g.client.Do(ctx, provider.Request{
			Method: http.MethodPatch, Path: base + "/" + variable.Key,
			Body: map[string]any{"name": variable.Key, "value": variable.Value},
		})
	}

	switch {
	case err == nil:
		return provider.OK(scope, "variable "+variable.Key)
	case provider.IsStatus(err, http.StatusForbidden):
		return provider.Skipped(scope, "variable "+variable.Key, "the token has no write access to Actions variables")
	default:
		return provider.Failed(scope, "variable "+variable.Key, err)
	}
}

func (g *GitHub) upsertSecret(ctx context.Context, project provider.Project, variable tmpl.Variable, key *publicKey) provider.Step {
	const scope = "variables"

	sealed, err := seal(variable.Value, key.Key)
	if err != nil {
		return provider.Failed(scope, "secret "+variable.Key, err)
	}

	err = g.client.Do(ctx, provider.Request{
		Method: http.MethodPut,
		Path:   "/repos/" + project.Owner + "/" + project.Name + "/actions/secrets/" + variable.Key,
		Body:   map[string]any{"encrypted_value": sealed, "key_id": key.KeyID},
	})

	switch {
	case err == nil:
		return provider.OK(scope, "secret "+variable.Key)
	case provider.IsStatus(err, http.StatusForbidden):
		return provider.Skipped(scope, "secret "+variable.Key, "the token has no write access to Actions secrets")
	default:
		return provider.Failed(scope, "secret "+variable.Key, err)
	}
}

func seal(value, encodedKey string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		return "", fmt.Errorf("decoding repository public key: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("repository public key has %d bytes, expected 32", len(raw))
	}

	var peer [32]byte
	copy(peer[:], raw)

	sealed, err := box.SealAnonymous(nil, []byte(value), &peer, nil)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}
