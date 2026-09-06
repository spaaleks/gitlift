package registry

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/spaaleks/gitlift/internal/config"
	"github.com/spaaleks/gitlift/internal/provider"
)

func TestLiveSmoke(t *testing.T) {
	if os.Getenv("GITLIFT_LIVE") == "" {
		t.Skip("set GITLIFT_LIVE=1 to run against the real providers")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("config: %s", cfg.Path)

	for _, entry := range cfg.Providers {
		if entry.Err != nil {
			t.Errorf("%-10s UNUSABLE  %v", entry.Name, entry.Err)
			continue
		}

		p, err := New(entry)
		if err != nil {
			t.Errorf("%-10s %v", entry.Name, err)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		projects, err := p.ListProjects(ctx)
		cancel()

		if err != nil {
			t.Errorf("%-10s %-22s FAILED  %s", entry.Name, p.Host(), provider.Explain(err))
			continue
		}
		t.Logf("%-10s %-22s ok      %d repositories", entry.Name, p.Host(), len(projects))
	}
}
