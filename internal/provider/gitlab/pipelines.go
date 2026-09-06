package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/spaaleks/gitlift/internal/provider"
	"github.com/spaaleks/gitlift/internal/tmpl"
)

const pipelineScope = "pipelines"

func (g *GitLab) ApplyPipelines(ctx context.Context, target provider.Project, pipelines *tmpl.Pipelines) []provider.Step {
	if pipelines.Empty() {
		return nil
	}

	var steps []provider.Step

	payload := map[string]any{}
	var changed []string

	add := func(field, label string, value any) {
		payload[field] = value
		changed = append(changed, fmt.Sprintf("%s=%v", label, value))
	}

	if pipelines.AutoDevOps != nil {
		add("auto_devops_enabled", "auto_devops", *pipelines.AutoDevOps)
	}
	if pipelines.AutoCancelPending != nil {
		state := "disabled"
		if *pipelines.AutoCancelPending {
			state = "enabled"
		}
		add("auto_cancel_pending_pipelines", "auto_cancel_pending", state)
	}
	if pipelines.PublicPipelines != nil {
		add("public_builds", "public_pipelines", *pipelines.PublicPipelines)
	}
	if pipelines.GitStrategy != nil {
		add("build_git_strategy", "git_strategy", *pipelines.GitStrategy)
	}
	if pipelines.GitDepth != nil {
		add("ci_default_git_depth", "git_depth", *pipelines.GitDepth)
	}
	if pipelines.Timeout != nil {
		seconds, err := tmpl.ParseTimeout(*pipelines.Timeout)
		if err != nil {
			steps = append(steps, provider.Skipped(pipelineScope, "timeout", err.Error()))
		} else {
			add("build_timeout", "timeout", seconds)
		}
	}
	if pipelines.ConfigPath != nil {
		add("ci_config_path", "config_path", *pipelines.ConfigPath)
	}
	if pipelines.ForwardDeployment != nil {
		add("ci_forward_deployment_enabled", "forward_deployment", *pipelines.ForwardDeployment)
	}
	if pipelines.SeparatedCaches != nil {
		add("ci_separated_caches", "separated_caches", *pipelines.SeparatedCaches)
	}
	if pipelines.ForkPipelines != nil {
		add("ci_allow_fork_pipelines_to_run_in_parent_project", "allow_fork_pipelines", *pipelines.ForkPipelines)
	}
	if pipelines.SharedRunners != nil {
		add("shared_runners_enabled", "shared_runners", *pipelines.SharedRunners)
	}
	if pipelines.GroupRunners != nil {
		add("group_runners_enabled", "group_runners", *pipelines.GroupRunners)
	}

	if len(payload) > 0 {
		const label = "pipeline settings"
		err := g.client.Do(ctx, provider.Request{
			Method: http.MethodPut, Path: "/projects/" + target.ID, Body: payload,
		})

		switch {
		case err == nil:
			steps = append(steps, provider.OK(pipelineScope, label).With(changed...))
		case provider.IsStatus(err, http.StatusForbidden):
			steps = append(steps, provider.Skipped(pipelineScope, label,
				"the token needs at least Maintainer on this project").With(changed...))
		default:
			steps = append(steps, provider.Failed(pipelineScope, label, err).With(changed...))
		}
	}

	if pipelines.JobTokenScope != nil {
		steps = append(steps, g.applyJobTokenScope(ctx, target, *pipelines.JobTokenScope))
	}

	return steps
}

func (g *GitLab) applyJobTokenScope(ctx context.Context, target provider.Project, restricted bool) provider.Step {
	label := fmt.Sprintf("restrict job token scope = %t", restricted)

	err := g.client.Do(ctx, provider.Request{
		Method: http.MethodPatch,
		Path:   "/projects/" + target.ID + "/job_token_scope",
		Body:   map[string]any{"enabled": restricted},
	})

	switch {
	case err == nil:
		return provider.OK(pipelineScope, label)
	case provider.IsStatus(err, http.StatusForbidden):
		return provider.Skipped(pipelineScope, label, "the token needs Maintainer rights on this project")
	case provider.IsStatus(err, http.StatusNotFound):
		return provider.Skipped(pipelineScope, label, "this GitLab version has no job token scope setting")
	default:
		return provider.Failed(pipelineScope, label, err)
	}
}

const variableScope = "variables"

type gitlabVariable struct {
	Key              string `json:"key"`
	EnvironmentScope string `json:"environment_scope"`
}

func (g *GitLab) ApplyVariables(ctx context.Context, target provider.Project, variables []tmpl.Variable) []provider.Step {
	if len(variables) == 0 {
		return nil
	}

	existing, listErr := g.listVariables(ctx, target)

	var steps []provider.Step
	for _, variable := range variables {
		label := "variable " + variable.Key
		if variable.EnvironmentScope != "" {
			label += " [" + variable.EnvironmentScope + "]"
		}

		if variable.Value == "" {
			steps = append(steps, provider.Skipped(variableScope, label,
				"no value given, the project keeps whatever it has now"))
			continue
		}

		steps = append(steps, g.upsertVariable(ctx, target, variable, label, existing, listErr))
	}

	return steps
}

func (g *GitLab) listVariables(ctx context.Context, target provider.Project) (map[string]bool, error) {
	known := map[string]bool{}

	var page []gitlabVariable
	err := g.client.Do(ctx, provider.Request{
		Method: http.MethodGet,
		Path:   "/projects/" + target.ID + "/variables",
		Query:  url.Values{"per_page": []string{"100"}},
		Out:    &page,
	})
	if err != nil {
		return nil, err
	}

	for _, entry := range page {
		scope := entry.EnvironmentScope
		if scope == "" {
			scope = "*"
		}
		known[entry.Key+"\x00"+scope] = true
	}
	return known, nil
}

func (g *GitLab) upsertVariable(ctx context.Context, target provider.Project, variable tmpl.Variable, label string, existing map[string]bool, listErr error) provider.Step {
	base := "/projects/" + target.ID + "/variables"

	body := map[string]any{
		"key":               variable.Key,
		"value":             variable.Value,
		"masked":            provider.DerefBool(variable.Masked, false),
		"protected":         provider.DerefBool(variable.Protected, false),
		"environment_scope": variable.Scope(),
	}
	if variable.Raw != nil {
		body["raw"] = *variable.Raw
	}
	if variable.Type != "" {
		body["variable_type"] = variable.Type
	}
	if variable.Description != "" {
		body["description"] = variable.Description
	}

	filter := url.Values{"filter[environment_scope]": []string{variable.Scope()}}

	update := func() error {
		return g.client.Do(ctx, provider.Request{
			Method: http.MethodPut,
			Path:   base + "/" + url.PathEscape(variable.Key),
			Query:  filter,
			Body:   body,
		})
	}

	var err error
	if listErr == nil && existing[variable.Key+"\x00"+variable.Scope()] {
		err = update()
	} else {
		err = g.client.Do(ctx, provider.Request{Method: http.MethodPost, Path: base, Body: body})
		if provider.NameTaken(err) || provider.IsStatus(err, http.StatusBadRequest) {
			if retryErr := update(); retryErr == nil {
				err = nil
			}
		}
	}

	switch {
	case err == nil:
		return provider.OK(variableScope, label)
	case provider.IsStatus(err, http.StatusForbidden):
		return provider.Skipped(variableScope, label,
			"the token needs at least Maintainer to manage CI/CD variables")
	default:
		return provider.Failed(variableScope, label, err)
	}
}
