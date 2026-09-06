package config

import "testing"

func TestParseResolvesTokensFromTheEnvironment(t *testing.T) {
	t.Setenv("GITLIFT_TEST_TOKEN", "from-env")

	cfg, err := Parse([]byte(`
providers:
  - name: via token_env
    type: github
    auth:
      token_env: GITLIFT_TEST_TOKEN
  - name: via expansion
    type: gitlab
    auth:
      token: "${GITLIFT_TEST_TOKEN}"
      base_url: "https://gitlab.example.com"
`))
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range cfg.Providers {
		if entry.Err != nil {
			t.Fatalf("%s: %v", entry.Name, entry.Err)
		}
		if entry.Auth.Token != "from-env" {
			t.Errorf("%s token = %q, want from-env", entry.Name, entry.Auth.Token)
		}
	}
}

func TestParseKeepsBadEntriesWithAReason(t *testing.T) {
	cfg, err := Parse([]byte(`
providers:
  - name: no token
    type: github
  - name: bad type
    type: bitbucket
    auth:
      token: x
  - name: good
    type: github
    auth:
      token: y
`))
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Providers) != 3 {
		t.Fatalf("got %d providers, want all 3 kept", len(cfg.Providers))
	}
	if cfg.Providers[0].Err == nil {
		t.Error("a provider with no token must carry an error")
	}
	if cfg.Providers[1].Err == nil {
		t.Error("a provider with an unknown type must carry an error")
	}
	if usable := cfg.Usable(); len(usable) != 1 || usable[0].Name != "good" {
		t.Errorf("usable = %+v, want only the good provider", usable)
	}
}

func TestParseRejectsAnEmptyFile(t *testing.T) {
	if _, err := Parse([]byte("providers: []\n")); err == nil {
		t.Fatal("expected an error for a file with no providers")
	}
	if _, err := Parse(nil); err == nil {
		t.Fatal("expected an error for an empty file")
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	cases := []struct {
		kind, in, want string
	}{
		{"github", "", "https://api.github.com"},
		{"github", "https://api.github.com/", "https://api.github.com"},
		{"github", "https://github.example.com", "https://github.example.com/api/v3"},
		{"gitlab", "", "https://gitlab.com/api/v4"},
		{"gitlab", "https://gitlab.example.com", "https://gitlab.example.com/api/v4"},
		{"gitlab", "https://gitlab.example.com/", "https://gitlab.example.com/api/v4"},
		{"gitlab", "https://gitlab.example.com/api/v4", "https://gitlab.example.com/api/v4"},
	}

	for _, tc := range cases {
		if got := NormalizeBaseURL(tc.kind, tc.in); got != tc.want {
			t.Errorf("NormalizeBaseURL(%q, %q) = %q, want %q", tc.kind, tc.in, got, tc.want)
		}
	}
}

func TestExampleConfigParses(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "a")
	t.Setenv("GITLAB_TOKEN", "b")

	cfg, err := Parse([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Usable()) != 2 {
		t.Errorf("usable = %d, want both example providers to work once the tokens are set", len(cfg.Usable()))
	}
}
