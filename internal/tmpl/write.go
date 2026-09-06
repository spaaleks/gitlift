package tmpl

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func (r Repo) MarshalYAML() (any, error) {
	type plain struct {
		Visibility    *string   `yaml:"visibility,omitempty"`
		Description   *string   `yaml:"description,omitempty"`
		DefaultBranch *string   `yaml:"default_branch,omitempty"`
		Topics        []string  `yaml:"topics,omitempty"`
		Features      *Features `yaml:"features,omitempty"`
	}

	out := plain{
		Visibility:    r.Visibility,
		Description:   r.Description,
		DefaultBranch: r.DefaultBranch,
		Topics:        r.Topics,
	}
	if !r.Features.Empty() {
		features := r.Features
		out.Features = &features
	}
	return out, nil
}

type document struct {
	Name          string         `yaml:"name"`
	Description   string         `yaml:"description,omitempty"`
	Repo          *Repo          `yaml:"repo,omitempty"`
	BranchRules   *BranchRules   `yaml:"branch_rules,omitempty"`
	TagRules      *TagRules      `yaml:"tag_rules,omitempty"`
	MergeRequests *MergeRequests `yaml:"merge_requests,omitempty"`
	Actions       *Actions       `yaml:"actions,omitempty"`
	Pipelines     *Pipelines     `yaml:"pipelines,omitempty"`
	Webhooks      []Webhook      `yaml:"webhooks,omitempty"`
	Providers     map[string]any `yaml:"providers,omitempty"`
}

func Encode(spec Spec, kind string) ([]byte, error) {
	doc := document{
		Name:          spec.Name,
		Description:   spec.Description,
		Repo:          spec.Repo,
		BranchRules:   spec.BranchRules,
		TagRules:      spec.TagRules,
		MergeRequests: spec.MergeRequests,
		Actions:       spec.Actions,
		Pipelines:     spec.Pipelines,
		Webhooks:      spec.Webhooks,
	}

	if doc.Repo != nil && doc.Repo.Features.Empty() &&
		doc.Repo.Visibility == nil && doc.Repo.Description == nil &&
		doc.Repo.DefaultBranch == nil && len(doc.Repo.Topics) == 0 {
		doc.Repo = nil
	}

	context := map[string]any{}
	switch kind {
	case "github":
		context["owner"] = ""
		context["owner_type"] = "user"
	case "gitlab":
		context["namespace"] = ""
	}
	if kind != "" {
		doc.Providers = map[string]any{kind: map[string]any{"context": context}}
	}

	var buf strings.Builder
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}

	return []byte(buf.String()), nil
}

func Save(spec Spec, kind, dir string) (string, error) {
	body, err := Encode(spec, kind)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	path := filepath.Join(dir, FileName(spec.Name))
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("%s already exists, pick another name", path)
	}

	if err := os.WriteFile(path, body, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func FolderName(name string) string {
	slug := slugify(name)
	if slug == "" || slug == "." || slug == ".." {
		return ""
	}
	return slug
}

func Scopes(templates []Template) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range templates {
		if t.Builtin || t.Scope == "" || seen[t.Scope] {
			continue
		}
		seen[t.Scope] = true
		out = append(out, t.Scope)
	}
	sort.Strings(out)
	return out
}

func FileName(name string) string {
	slug := slugify(name)
	if slug == "" {
		slug = "template"
	}
	return slug + ".yaml"
}

func slugify(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}

	slug := strings.Trim(b.String(), "-")
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	return slug
}

func WriteDirs() []string {
	dirs := SearchDirs()
	pinned := os.Getenv("GITLIFT_TEMPLATES")
	if pinned == "" || len(dirs) == 0 || dirs[0] == pinned {
		return dirs
	}

	out := []string{pinned}
	for _, dir := range dirs {
		if dir != pinned {
			out = append(out, dir)
		}
	}
	return out
}

func Abbrev(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !strings.HasPrefix(path, home+string(filepath.Separator)) {
		return path
	}
	return "~" + strings.TrimPrefix(path, home)
}

func Expand(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/"))
}
