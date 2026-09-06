package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/spaaleks/gitlift/internal/provider"
)

func (g *GitLab) Namespaces(ctx context.Context) ([]provider.Namespace, error) {
	var self struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	if err := g.client.Do(ctx, provider.Request{
		Method: http.MethodGet, Path: "/user", Out: &self,
	}); err != nil {
		return nil, err
	}

	spaces := []provider.Namespace{{Path: self.Username, Name: self.Name, Kind: "user"}}

	query := url.Values{}
	query.Set("min_access_level", "30")
	query.Set("order_by", "path")
	query.Set("sort", "asc")

	err := g.client.Paginate(ctx, "/groups", query, func(raw json.RawMessage) (int, error) {
		var batch []struct {
			FullPath string `json:"full_path"`
			Name     string `json:"name"`
		}
		if err := json.Unmarshal(raw, &batch); err != nil {
			return 0, err
		}
		for _, group := range batch {
			spaces = append(spaces, provider.Namespace{Path: group.FullPath, Name: group.Name, Kind: "group"})
		}
		return len(batch), nil
	})
	if err != nil {
		return spaces, nil
	}

	return spaces, nil
}
