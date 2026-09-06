package gitlab

import (
	"context"
	"net/http"
	"net/url"

	"github.com/spaaleks/gitlift/internal/provider"
	"github.com/spaaleks/gitlift/internal/tmpl"
)

const tagScope = "tag rules"

type protectedTag struct {
	Name              string        `json:"name"`
	CreateAccessLevel []accessEntry `json:"create_access_levels"`
}

func (g *GitLab) listProtectedTags(ctx context.Context, target provider.Project) ([]protectedTag, error) {
	var tags []protectedTag
	err := g.client.Do(ctx, provider.Request{
		Method: http.MethodGet,
		Path:   "/projects/" + target.ID + "/protected_tags",
		Out:    &tags,
	})
	if provider.IsStatus(err, http.StatusForbidden, http.StatusNotFound) {
		return nil, nil
	}
	return tags, err
}

func (g *GitLab) ApplyTagRules(ctx context.Context, target provider.Project, rules []tmpl.TagRule, mode provider.RuleMode) []provider.Step {
	if len(rules) == 0 {
		return nil
	}

	existing, err := g.listProtectedTags(ctx, target)
	if err != nil {
		return []provider.Step{provider.Failed(tagScope, "read protected tags", err)}
	}

	current := map[string]protectedTag{}
	for _, tag := range existing {
		current[tag.Name] = tag
	}

	var steps []provider.Step
	applied := map[string]bool{}

	for _, rule := range rules {
		if len(rule.Patterns) == 0 {
			steps = append(steps, provider.Skipped(tagScope, rule.Name, "no patterns"))
			continue
		}

		want, declared := provider.AccessLevel(rule.CreateAccess)

		for _, pattern := range rule.Patterns {
			label := rule.Name + " → " + pattern
			applied[pattern] = true

			if tag, exists := current[pattern]; exists {
				live, found := roleLevel(tag.CreateAccessLevel)
				if !declared || (found && live == want) {
					steps = append(steps, provider.Step{
						Scope: tagScope, Label: label, Status: provider.StatusOK,
						Detail: "already matches the template",
					})
					continue
				}
				if err := g.unprotectTag(ctx, target, pattern); err != nil {
					steps = append(steps, provider.Failed(tagScope, label, err))
					continue
				}
			}

			payload := map[string]any{"name": pattern}
			if declared {
				payload["create_access_level"] = want
			}

			err := g.client.Do(ctx, provider.Request{
				Method: http.MethodPost,
				Path:   "/projects/" + target.ID + "/protected_tags",
				Body:   payload,
			})

			switch {
			case err == nil:
				steps = append(steps, provider.Step{
					Scope: tagScope, Label: label, Status: provider.StatusOK, Detail: "protected",
				})
			case provider.IsStatus(err, http.StatusForbidden):
				steps = append(steps, provider.Skipped(tagScope, label,
					"the token needs at least Maintainer on this project"))
			default:
				steps = append(steps, provider.Failed(tagScope, label, err))
			}
		}
	}

	if mode == provider.ModeReplace {
		for _, tag := range existing {
			if applied[tag.Name] {
				continue
			}
			if err := g.unprotectTag(ctx, target, tag.Name); err != nil {
				steps = append(steps, provider.Failed(tagScope, "unprotect "+tag.Name, err))
				continue
			}
			steps = append(steps, provider.Step{
				Scope: tagScope, Label: tag.Name, Status: provider.StatusOK, Detail: "unprotected",
			})
		}
	}

	return steps
}

func (g *GitLab) unprotectTag(ctx context.Context, target provider.Project, name string) error {
	err := g.client.Do(ctx, provider.Request{
		Method: http.MethodDelete,
		Path:   "/projects/" + target.ID + "/protected_tags/" + url.PathEscape(name),
	})
	if provider.IsStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}
