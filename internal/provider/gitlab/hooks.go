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

const (
	hookScope        = "webhooks"
	integrationScope = "integrations"
)

var hookFields = map[string]string{
	"push":          "push_events",
	"tag_push":      "tag_push_events",
	"issues":        "issues_events",
	"merge_request": "merge_requests_events",
	"note":          "note_events",
	"pipeline":      "pipeline_events",
	"job":           "job_events",
	"wiki_page":     "wiki_page_events",
	"release":       "releases_events",
	"deployment":    "deployment_events",
}

type projectHook struct {
	ID  int64  `json:"id"`
	URL string `json:"url"`
}

func (g *GitLab) ApplyWebhooks(ctx context.Context, target provider.Project, hooks []tmpl.Webhook) []provider.Step {
	if len(hooks) == 0 {
		return nil
	}

	base := "/projects/" + target.ID + "/hooks"

	var existing []projectHook
	if err := g.client.Do(ctx, provider.Request{Method: http.MethodGet, Path: base, Out: &existing}); err != nil {
		if provider.IsStatus(err, http.StatusForbidden) {
			return []provider.Step{provider.Skipped(hookScope, "read webhooks",
				"the token needs Maintainer rights to manage webhooks")}
		}
		return []provider.Step{provider.Failed(hookScope, "read webhooks", err)}
	}

	byURL := map[string]int64{}
	for _, hook := range existing {
		byURL[hook.URL] = hook.ID
	}

	var steps []provider.Step
	for _, hook := range hooks {
		payload := map[string]any{"url": hook.URL}

		for _, event := range tmpl.HookEvents() {
			payload[hookFields[event]] = false
		}
		for _, event := range hook.Events {
			if field, ok := hookFields[event]; ok {
				payload[field] = true
			}
		}

		if hook.Secret != "" {
			payload["token"] = hook.Secret
		}
		if hook.SSLVerification != nil {
			payload["enable_ssl_verification"] = *hook.SSLVerification
		}
		if hook.BranchFilter != "" {
			payload["push_events_branch_filter"] = hook.BranchFilter
		}
		if hook.ContentType != "" {
			steps = append(steps, provider.Info(hookScope, hook.URL+" content_type",
				"GitHub-only. GitLab webhooks always post JSON"))
		}

		label := hook.URL + " (" + eventList(hook.Events) + ")"

		method, path := http.MethodPost, base
		verb := "created"
		if id, found := byURL[hook.URL]; found {
			method, path = http.MethodPut, fmt.Sprintf("%s/%d", base, id)
			verb = "updated"
		}

		err := g.client.Do(ctx, provider.Request{Method: method, Path: path, Body: payload})

		switch {
		case err == nil:
			steps = append(steps, provider.Step{
				Scope: hookScope, Label: label, Status: provider.StatusOK, Detail: verb,
			})
		case provider.IsStatus(err, http.StatusForbidden):
			steps = append(steps, provider.Skipped(hookScope, label,
				"the token needs Maintainer rights to manage webhooks"))
		case provider.IsStatus(err, http.StatusUnprocessableEntity):
			steps = append(steps, provider.Skipped(hookScope, label, provider.Message(err)))
		default:
			steps = append(steps, provider.Failed(hookScope, label, err))
		}
	}

	return steps
}

func eventList(events []string) string {
	if len(events) == 0 {
		return "no events"
	}
	return strings.Join(events, ", ")
}

func (g *GitLab) ApplyIntegrations(ctx context.Context, target provider.Project, integrations []tmpl.Integration) []provider.Step {
	if len(integrations) == 0 {
		return nil
	}

	var steps []provider.Step

	for _, integration := range integrations {
		slug := strings.ToLower(strings.TrimSpace(integration.Name))
		path := "/projects/" + target.ID + "/integrations/" + url.PathEscape(slug)

		if integration.Enabled != nil && !*integration.Enabled {
			err := g.client.Do(ctx, provider.Request{Method: http.MethodDelete, Path: path})
			steps = append(steps, integrationOutcome(slug+" disabled", err))
			continue
		}

		payload := map[string]any{}
		for key, value := range integration.Settings {
			payload[key] = value
		}
		for _, event := range integration.Events {
			if field, ok := hookFields[event]; ok {
				payload[field] = true
			}
		}

		label := slug
		if len(integration.Events) > 0 {
			label += " (" + eventList(integration.Events) + ")"
		}

		err := g.client.Do(ctx, provider.Request{Method: http.MethodPut, Path: path, Body: payload})
		steps = append(steps, integrationOutcome(label, err))
	}

	return steps
}

func integrationOutcome(label string, err error) provider.Step {
	switch {
	case err == nil:
		return provider.OK(integrationScope, label)
	case provider.IsStatus(err, http.StatusNotFound):
		return provider.Skipped(integrationScope, label,
			"this GitLab does not offer that integration under that name")
	case provider.IsStatus(err, http.StatusForbidden):
		return provider.Skipped(integrationScope, label,
			"the token needs Maintainer rights to manage integrations")
	case provider.IsStatus(err, http.StatusUnprocessableEntity, http.StatusBadRequest):
		return provider.Skipped(integrationScope, label, provider.Message(err))
	default:
		return provider.Failed(integrationScope, label, err)
	}
}
