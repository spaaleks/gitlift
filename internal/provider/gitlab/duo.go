package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/spaaleks/gitlift/internal/provider"
)

const duoKey = "duo"

var graphqlFeatures = map[string]bool{duoKey: true}

func graphqlURL(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	root = strings.TrimSuffix(root, "/api/v4")
	return root + "/api/graphql"
}

type graphqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

type duoResponse struct {
	Data struct {
		ProjectSettingsUpdate struct {
			Errors          []string `json:"errors"`
			ProjectSettings struct {
				DuoFeaturesEnabled *bool `json:"duoFeaturesEnabled"`
			} `json:"projectSettings"`
		} `json:"projectSettingsUpdate"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

const duoMutation = `mutation($fullPath: ID!, $enabled: Boolean!) {
  projectSettingsUpdate(input: {fullPath: $fullPath, duoFeaturesEnabled: $enabled}) {
    errors
    projectSettings { duoFeaturesEnabled }
  }
}`

func (g *GitLab) applyDuo(ctx context.Context, target provider.Project, on bool) provider.Step {
	const scope = "settings"
	label := fmt.Sprintf("GitLab Duo = %t", on)

	if target.FullName == "" {
		return provider.Skipped(scope, label, "the project path is not known, so the mutation cannot be addressed")
	}

	var out duoResponse
	err := g.graphql.Do(ctx, provider.Request{
		Method: http.MethodPost,
		Body: graphqlRequest{
			Query:     duoMutation,
			Variables: map[string]any{"fullPath": target.FullName, "enabled": on},
		},
		Out: &out,
	})

	switch {
	case provider.IsStatus(err, http.StatusNotFound):
		return provider.Skipped(scope, label, "this GitLab has no GraphQL endpoint at /api/graphql")
	case provider.IsStatus(err, http.StatusUnauthorized, http.StatusForbidden):
		return provider.Skipped(scope, label, "the token may not change project settings through GraphQL")
	case err != nil:
		return provider.Failed(scope, label, err)
	}

	if len(out.Errors) > 0 {
		var messages []string
		for _, entry := range out.Errors {
			messages = append(messages, entry.Message)
		}
		joined := strings.Join(messages, ", ")

		if strings.Contains(joined, "does not exist or you don't have permission") {
			reason, items := g.diagnoseDuo(ctx, target)
			return provider.Skipped(scope, label, reason).
				With(append(items, "GraphQL said: "+joined)...)
		}

		return provider.Skipped(scope, label, "GraphQL rejected the mutation: "+joined)
	}

	result := out.Data.ProjectSettingsUpdate
	if len(result.Errors) > 0 {
		return provider.Skipped(scope, label, strings.Join(result.Errors, ", "))
	}

	got := result.ProjectSettings.DuoFeaturesEnabled
	switch {
	case got == nil:
		return provider.Skipped(scope, label, "the mutation returned no value, so it cannot be confirmed")
	case *got != on:
		return provider.Skipped(scope, label,
			fmt.Sprintf("the mutation was accepted but the setting is still %t. A group or instance policy may lock it", *got))
	}

	return provider.OK(scope, label)
}

const duoDiagnosis = `query($fullPath: ID!) {
  currentUser { username bot }
  project(fullPath: $fullPath) { id userPermissions { adminProject } }
}`

type duoDiagnosisResponse struct {
	Data struct {
		CurrentUser *struct {
			Username string `json:"username"`
			Bot      bool   `json:"bot"`
		} `json:"currentUser"`
		Project *struct {
			ID              string `json:"id"`
			UserPermissions struct {
				AdminProject bool `json:"adminProject"`
			} `json:"userPermissions"`
		} `json:"project"`
	} `json:"data"`
}

func (g *GitLab) diagnoseDuo(ctx context.Context, target provider.Project) (string, []string) {
	var out duoDiagnosisResponse
	err := g.graphql.Do(ctx, provider.Request{
		Method: http.MethodPost,
		Body: graphqlRequest{
			Query:     duoDiagnosis,
			Variables: map[string]any{"fullPath": target.FullName},
		},
		Out: &out,
	})
	if err != nil {
		return "GraphQL refused the change and the follow-up check also failed (" +
			provider.Message(err) + ")", nil
	}

	var items []string

	who := "unknown"
	if user := out.Data.CurrentUser; user != nil {
		who = user.Username
		if user.Bot {
			who += " (a bot user, not a person)"
		}
	}
	items = append(items, "this token acts as: "+who)

	if out.Data.Project == nil {
		items = append(items, "the project does not resolve for this token")
		return "GraphQL cannot see " + target.FullName + " as this token. The path is wrong, " +
			"or the token has no access to it", items
	}

	items = append(items, "project resolves: "+out.Data.Project.ID)

	if !out.Data.Project.UserPermissions.AdminProject {
		items = append(items, "adminProject permission: false")
		return "this token cannot administer " + target.FullName + ", GitLab Duo needs Owner-level " +
			"rights, which is why the change works in the browser but not here", items
	}

	items = append(items, "adminProject permission: true")
	return "the token can administer the project, so GitLab Duo itself is unavailable here. " +
		"A group or instance policy is locking the setting", items
}
