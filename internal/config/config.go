package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Auth struct {
	Token    string `yaml:"token"`
	TokenEnv string `yaml:"token_env"`
	BaseURL  string `yaml:"base_url"`
}

type Provider struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	Auth Auth   `yaml:"auth"`

	Err error `yaml:"-"`
}

func (p Provider) Usable() bool { return p.Err == nil }

type Config struct {
	Path      string
	Providers []Provider
}

func (c Config) Usable() []Provider {
	var out []Provider
	for _, p := range c.Providers {
		if p.Usable() {
			out = append(out, p)
		}
	}
	return out
}

type file struct {
	Providers []Provider `yaml:"providers"`
}

var ErrNotFound = errors.New("no gitlift config found")

func SearchPaths() []string {
	var paths []string

	if p := os.Getenv("GITLIFT_CONFIG"); p != "" {
		paths = append(paths, p)
	}
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths,
			filepath.Join(cwd, ".gitlift.yaml"),
			filepath.Join(cwd, ".providers.yaml"),
		)
	}
	paths = append(paths, filepath.Join(configDir(), "providers.yaml"))

	return paths
}

func DefaultPath() string {
	if p := os.Getenv("GITLIFT_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(configDir(), "providers.yaml")
}

func configDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "gitlift")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "gitlift")
	}
	return ".gitlift"
}

func Load() (Config, error) {
	for _, path := range SearchPaths() {
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return Config{}, fmt.Errorf("reading %s: %w", path, err)
		}
		cfg, err := Parse(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parsing %s: %w", path, err)
		}
		cfg.Path = path
		return cfg, nil
	}
	return Config{}, ErrNotFound
}

func Parse(raw []byte) (Config, error) {
	var parsed file
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return Config{}, err
	}
	if len(parsed.Providers) == 0 {
		return Config{}, errors.New("no providers defined (expected a top-level `providers:` list)")
	}

	cfg := Config{Providers: make([]Provider, 0, len(parsed.Providers))}
	for i, p := range parsed.Providers {
		p.Type = strings.ToLower(strings.TrimSpace(p.Type))
		if p.Name == "" {
			p.Name = fmt.Sprintf("provider %d", i+1)
		}

		switch p.Type {
		case "github", "gitlab":
		case "":
			p.Err = errors.New("missing `type` (expected github or gitlab)")
		default:
			p.Err = fmt.Errorf("unsupported type %q (expected github or gitlab)", p.Type)
		}

		token, err := resolveToken(p.Auth)
		if err != nil && p.Err == nil {
			p.Err = err
		}
		p.Auth.Token = token
		p.Auth.BaseURL = NormalizeBaseURL(p.Type, p.Auth.BaseURL)

		cfg.Providers = append(cfg.Providers, p)
	}

	return cfg, nil
}

func resolveToken(auth Auth) (string, error) {
	if auth.TokenEnv != "" {
		token := strings.TrimSpace(os.Getenv(auth.TokenEnv))
		if token == "" {
			return "", fmt.Errorf("environment variable %s is empty", auth.TokenEnv)
		}
		return token, nil
	}

	token := strings.TrimSpace(os.Expand(auth.Token, os.Getenv))
	if token == "" {
		if strings.Contains(auth.Token, "$") {
			return "", fmt.Errorf("token %q expanded to nothing. Is the variable exported?", auth.Token)
		}
		return "", errors.New("missing `auth.token` (or `auth.token_env`)")
	}
	return token, nil
}

func NormalizeBaseURL(kind, raw string) string {
	url := strings.TrimRight(strings.TrimSpace(raw), "/")

	switch kind {
	case "github":
		if url == "" {
			return "https://api.github.com"
		}
		if !strings.Contains(url, "/api/") && !strings.HasPrefix(url, "https://api.github.com") {
			url += "/api/v3"
		}
		return url
	case "gitlab":
		if url == "" {
			return "https://gitlab.com/api/v4"
		}
		if !strings.Contains(url, "/api/") {
			url += "/api/v4"
		}
		return url
	}
	return url
}

const Example = `providers:
  - name: "GitHub"
    type: "github"
    auth:
      token_env: "GITHUB_TOKEN"

  - name: "GitLab"
    type: "gitlab"
    auth:
      token_env: "GITLAB_TOKEN"
      base_url: "https://gitlab.com"
`

func WriteExample(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(Example), 0o600)
}
