package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/spaaleks/gitlift/internal/provider"
)

func (g *GitHub) Namespaces(ctx context.Context) ([]provider.Namespace, error) {
	var self struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := g.client.Do(ctx, provider.Request{
		Method: http.MethodGet, Path: "/user", Out: &self,
	}); err != nil {
		return nil, err
	}

	spaces := []provider.Namespace{{Path: self.Login, Name: self.Name, Kind: "user"}}

	err := g.client.Paginate(ctx, "/user/orgs", url.Values{}, func(raw json.RawMessage) (int, error) {
		var batch []struct {
			Login string `json:"login"`
		}
		if err := json.Unmarshal(raw, &batch); err != nil {
			return 0, err
		}
		for _, org := range batch {
			spaces = append(spaces, provider.Namespace{Path: org.Login, Kind: "org"})
		}
		return len(batch), nil
	})
	if err != nil {
		return spaces, nil
	}

	return spaces, nil
}
