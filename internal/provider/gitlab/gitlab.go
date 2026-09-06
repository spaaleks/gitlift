package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"github.com/spaaleks/gitlift/internal/provider"
	"github.com/spaaleks/gitlift/internal/tmpl"
)

type GitLab struct {
	name    string
	client  *provider.Client
	graphql *provider.Client
}

func New(name, token, baseURL string) *GitLab {
	header := http.Header{}
	header.Set("PRIVATE-TOKEN", token)

	return &GitLab{
		name:    name,
		client:  provider.NewClient("gitlab "+name, baseURL, header),
		graphql: provider.NewClient("gitlab "+name+" graphql", graphqlURL(baseURL), header),
	}
}

func (g *GitLab) Name() string { return g.name }

func (g *GitLab) Kind() string { return "gitlab" }

func (g *GitLab) Host() string { return g.client.Host() }

type project struct {
	ID                int64    `json:"id"`
	Name              string   `json:"name"`
	Path              string   `json:"path"`
	PathWithNamespace string   `json:"path_with_namespace"`
	Description       string   `json:"description"`
	Visibility        string   `json:"visibility"`
	DefaultBranch     string   `json:"default_branch"`
	WebURL            string   `json:"web_url"`
	Archived          bool     `json:"archived"`
	EmptyRepo         bool     `json:"empty_repo"`
	Topics            []string `json:"topics"`
	Namespace         struct {
		FullPath string `json:"full_path"`
	} `json:"namespace"`

	raw map[string]any
}

func (p *project) UnmarshalJSON(data []byte) error {
	type alias project
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*p = project(decoded)
	return json.Unmarshal(data, &p.raw)
}

func (g *GitLab) toProject(p project) provider.Project {
	return provider.Project{
		ProviderName:  g.name,
		Kind:          "gitlab",
		ID:            fmt.Sprint(p.ID),
		Owner:         p.Namespace.FullPath,
		Name:          p.Path,
		FullName:      p.PathWithNamespace,
		Description:   p.Description,
		Visibility:    p.Visibility,
		DefaultBranch: p.DefaultBranch,
		URL:           p.WebURL,
		Archived:      p.Archived,
		Empty:         p.EmptyRepo || p.DefaultBranch == "",
		Topics:        p.Topics,
		Features:      readFeatures(p.raw),
	}
}

func (g *GitLab) ListProjects(ctx context.Context) ([]provider.Project, error) {
	var projects []provider.Project

	query := url.Values{}
	query.Set("membership", "true")
	query.Set("min_access_level", "30")
	query.Set("order_by", "path")
	query.Set("sort", "asc")

	err := g.client.Paginate(ctx, "/projects", query, func(raw json.RawMessage) (int, error) {
		var batch []project
		if err := json.Unmarshal(raw, &batch); err != nil {
			return 0, err
		}
		for _, p := range batch {
			projects = append(projects, g.toProject(p))
		}
		return len(batch), nil
	})
	if err != nil {
		return nil, err
	}

	return projects, nil
}

func (g *GitLab) Lookup(ctx context.Context, target provider.Target, name string) (provider.Project, bool, error) {
	path := name
	if target.Owner != "" {
		path = target.Owner + "/" + name
	}

	var found project
	err := g.client.Do(ctx, provider.Request{
		Method: http.MethodGet,
		Path:   "/projects/" + url.PathEscape(path),
		Out:    &found,
	})
	if provider.IsStatus(err, http.StatusNotFound) {
		return provider.Project{}, false, nil
	}
	if err != nil {
		return provider.Project{}, false, err
	}
	return g.toProject(found), true, nil
}

func (g *GitLab) namespaceID(ctx context.Context, path string) (int64, error) {
	if path == "" {
		return 0, nil
	}

	var namespace struct {
		ID int64 `json:"id"`
	}
	err := g.client.Do(ctx, provider.Request{
		Method: http.MethodGet,
		Path:   "/namespaces/" + url.PathEscape(path),
		Out:    &namespace,
	})
	if err == nil {
		return namespace.ID, nil
	}
	if !provider.IsStatus(err, http.StatusNotFound) {
		return 0, err
	}

	var group struct {
		ID int64 `json:"id"`
	}
	if err := g.client.Do(ctx, provider.Request{
		Method: http.MethodGet,
		Path:   "/groups/" + url.PathEscape(path),
		Out:    &group,
	}); err != nil {
		return 0, fmt.Errorf("namespace %q not found", path)
	}
	return group.ID, nil
}

func (g *GitLab) Create(ctx context.Context, target provider.Target, settings provider.Settings) (provider.Project, error) {
	namespaceID, err := g.namespaceID(ctx, target.Owner)
	if err != nil {
		return provider.Project{}, err
	}

	name := settings.DisplayName
	if name == "" {
		name = settings.Name
	}

	payload := map[string]any{"name": name, "path": settings.Name}
	if namespaceID != 0 {
		payload["namespace_id"] = namespaceID
	}
	if settings.Description != nil {
		payload["description"] = *settings.Description
	}
	if settings.Visibility != nil {
		payload["visibility"] = *settings.Visibility
	}
	if settings.DefaultBranch != nil {
		payload["default_branch"] = *settings.DefaultBranch
	}
	if len(settings.Topics) > 0 {
		payload["topics"] = settings.Topics
	}
	featurePayload(payload, settings.Features)

	var created project
	if err := g.client.Do(ctx, provider.Request{
		Method: http.MethodPost, Path: "/projects", Body: payload, Out: &created,
	}); err != nil {
		return provider.Project{}, err
	}

	return g.toProject(created), nil
}

func (g *GitLab) UpdateSettings(ctx context.Context, target provider.Project, settings provider.Settings) []provider.Step {
	const scope = "settings"

	payload := map[string]any{}
	var changed []string

	if settings.Description != nil && *settings.Description != target.Description {
		payload["description"] = *settings.Description
		changed = append(changed, "description")
	}
	if settings.Visibility != nil && *settings.Visibility != target.Visibility {
		payload["visibility"] = *settings.Visibility
		changed = append(changed, "visibility="+*settings.Visibility)
	}
	if settings.DefaultBranch != nil && *settings.DefaultBranch != target.DefaultBranch {
		payload["default_branch"] = *settings.DefaultBranch
		changed = append(changed, "default_branch="+*settings.DefaultBranch)
	}
	if len(settings.Topics) > 0 && !slices.Equal(settings.Topics, target.Topics) {
		payload["topics"] = settings.Topics
		changed = append(changed, "topics")
	}

	featureChanges, graph, ignored := featurePayload(payload, settings.Features)
	changed = append(changed, featureChanges...)

	var steps []provider.Step
	for _, key := range ignored {
		steps = append(steps, provider.Info(scope, "feature "+key, "not a GitLab setting, ignored here"))
	}
	if on, declared := graph[duoKey]; declared {
		steps = append(steps, g.applyDuo(ctx, target, on))
	}

	if len(payload) == 0 {
		if len(graph) > 0 {
			return steps
		}
		return append(steps, provider.Skipped(scope, "project settings", "template declares nothing to change"))
	}

	const label = "project settings"
	err := g.client.Do(ctx, provider.Request{
		Method: http.MethodPut, Path: "/projects/" + target.ID, Body: payload,
	})

	switch {
	case err == nil:
		steps = append(steps, provider.OK(scope, label).With(changed...))
		return append(steps, g.verifyFeatures(ctx, target, settings.Features)...)
	case provider.IsStatus(err, http.StatusForbidden):
		return append(steps, provider.Skipped(scope, label,
			"the token needs at least Maintainer on this project").With(changed...))
	default:
		return append(steps, provider.Failed(scope, label, err).With(changed...))
	}
}

func (g *GitLab) verifyFeatures(ctx context.Context, target provider.Project, want tmpl.Features) []provider.Step {
	supported, _ := want.For("gitlab")
	if supported.Empty() {
		return nil
	}

	var after project
	if err := g.client.Do(ctx, provider.Request{
		Method: http.MethodGet, Path: "/projects/" + target.ID, Out: &after,
	}); err != nil {
		return nil
	}

	live := readFeatures(after.raw)

	var missing []string
	for _, key := range supported.Keys() {
		if graphqlFeatures[key] {
			continue
		}
		wanted, _ := supported.Get(key)
		got, present := live.Get(key)
		switch {
		case !present:
			missing = append(missing, key+" (not reported by this GitLab)")
		case got != wanted:
			missing = append(missing, fmt.Sprintf("%s (asked %t, still %t)", key, wanted, got))
		}
	}

	if len(missing) == 0 {
		return nil
	}

	return []provider.Step{provider.Skipped("settings", "toggles this GitLab did not apply",
		"the request was accepted but these came back unchanged. The version or tier may not support them").
		With(missing...)}
}

func (g *GitLab) ApplyActions(_ context.Context, _ provider.Project, actions *tmpl.Actions) []provider.Step {
	if actions.Empty() {
		return nil
	}
	return []provider.Step{provider.Info("actions", "actions block",
		"GitHub-only. Use the pipelines block for GitLab CI/CD settings")}
}
