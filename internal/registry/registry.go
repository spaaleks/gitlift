package registry

import (
	"fmt"

	"github.com/spaaleks/gitlift/internal/config"
	"github.com/spaaleks/gitlift/internal/provider"
	"github.com/spaaleks/gitlift/internal/provider/github"
	"github.com/spaaleks/gitlift/internal/provider/gitlab"
)

func New(entry config.Provider) (provider.Provider, error) {
	if entry.Err != nil {
		return nil, entry.Err
	}

	switch entry.Type {
	case "github":
		return github.New(entry.Name, entry.Auth.Token, entry.Auth.BaseURL), nil
	case "gitlab":
		return gitlab.New(entry.Name, entry.Auth.Token, entry.Auth.BaseURL), nil
	default:
		return nil, fmt.Errorf("unsupported provider type %q", entry.Type)
	}
}

func All(cfg config.Config) ([]provider.Provider, []error) {
	var providers []provider.Provider
	var errs []error

	for _, entry := range cfg.Providers {
		p, err := New(entry)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", entry.Name, err))
			continue
		}
		providers = append(providers, p)
	}

	return providers, errs
}
