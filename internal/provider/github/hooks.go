package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/spaaleks/gitlift/internal/provider"
	"github.com/spaaleks/gitlift/internal/tmpl"
)

const hookScope = "webhooks"

var hookEvents = map[string]string{
	"push":          "push",
	"tag_push":      "create",
	"issues":        "issues",
	"merge_request": "pull_request",
	"note":          "issue_comment",
	"pipeline":      "workflow_run",
	"job":           "workflow_job",
	"wiki_page":     "gollum",
	"release":       "release",
	"deployment":    "deployment",
}

type repositoryHook struct {
	ID     int64 `json:"id"`
	Config struct {
		URL string `json:"url"`
	} `json:"config"`
}

func (g *GitHub) ApplyWebhooks(ctx context.Context, project provider.Project, hooks []tmpl.Webhook) []provider.Step {
	if len(hooks) == 0 {
		return nil
	}

	base := "/repos/" + project.Owner + "/" + project.Name + "/hooks"

	var existing []repositoryHook
	if err := g.client.Do(ctx, provider.Request{Method: http.MethodGet, Path: base, Out: &existing}); err != nil {
		if provider.IsStatus(err, http.StatusForbidden, http.StatusNotFound) {
			return []provider.Step{provider.Skipped(hookScope, "read webhooks",
				"the token needs admin rights on this repository to manage webhooks")}
		}
		return []provider.Step{provider.Failed(hookScope, "read webhooks", err)}
	}

	byURL := map[string]int64{}
	for _, hook := range existing {
		byURL[hook.Config.URL] = hook.ID
	}

	var steps []provider.Step
	for _, hook := range hooks {
		contentType := hook.ContentType
		if contentType == "" {
			contentType = "json"
		}

		config := map[string]any{"url": hook.URL, "content_type": contentType}
		if hook.Secret != "" {
			config["secret"] = hook.Secret
		}
		if hook.SSLVerification != nil {
			config["insecure_ssl"] = "1"
			if *hook.SSLVerification {
				config["insecure_ssl"] = "0"
			}
		}

		var events []string
		for _, event := range hook.Events {
			if mapped, ok := hookEvents[event]; ok {
				events = append(events, mapped)
			}
		}
		if len(events) == 0 {
			events = []string{"push"}
		}

		if hook.BranchFilter != "" {
			steps = append(steps, provider.Info(hookScope, hook.URL+" push_branch_filter",
				"GitLab-only. GitHub webhooks cannot filter by branch"))
		}

		payload := map[string]any{"config": config, "events": events, "active": true}

		label := hook.URL + " (" + strings.Join(hook.Events, ", ") + ")"
		if len(hook.Events) == 0 {
			label = hook.URL + " (push)"
		}

		method, path := http.MethodPost, base
		verb := "created"
		if id, found := byURL[hook.URL]; found {
			method, path = http.MethodPatch, fmt.Sprintf("%s/%d", base, id)
			verb = "updated"
		} else {
			payload["name"] = "web"
		}

		err := g.client.Do(ctx, provider.Request{Method: method, Path: path, Body: payload})

		switch {
		case err == nil:
			steps = append(steps, provider.Step{
				Scope: hookScope, Label: label, Status: provider.StatusOK, Detail: verb,
			})
		case provider.IsStatus(err, http.StatusForbidden, http.StatusNotFound):
			steps = append(steps, provider.Skipped(hookScope, label,
				"the token needs admin rights on this repository to manage webhooks"))
		case provider.IsStatus(err, http.StatusUnprocessableEntity):
			steps = append(steps, provider.Skipped(hookScope, label, provider.Message(err)))
		default:
			steps = append(steps, provider.Failed(hookScope, label, err))
		}
	}

	return steps
}
