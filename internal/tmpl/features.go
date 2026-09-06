package tmpl

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type FeatureDef struct {
	Key     string
	Label   string
	Kinds   []string
	Note    string
	Boolean bool
}

func (d FeatureDef) Supports(kind string) bool {
	for _, candidate := range d.Kinds {
		if candidate == kind {
			return true
		}
	}
	return false
}

const (
	both   = "github,gitlab"
	ghOnly = "github"
	glOnly = "gitlab"
)

var featureCatalog = buildCatalog([]FeatureDef{
	{Key: "issues", Label: "issues", Kinds: kinds(both)},
	{Key: "merge_requests", Label: "merge requests", Kinds: kinds(glOnly),
		Note: "GitHub cannot disable pull requests"},
	{Key: "requirements", Label: "requirements", Kinds: kinds(glOnly)},
	{Key: "wiki", Label: "wiki", Kinds: kinds(both)},
	{Key: "projects", Label: "projects", Kinds: kinds(ghOnly),
		Note: "GitLab has no per-project boards toggle"},
	{Key: "discussions", Label: "discussions", Kinds: kinds(ghOnly)},
	{Key: "downloads", Label: "downloads", Kinds: kinds(ghOnly)},
	{Key: "template", Label: "template repository", Kinds: kinds(ghOnly)},
	{Key: "web_commit_signoff", Label: "web commit signoff", Kinds: kinds(ghOnly)},

	{Key: "repository", Label: "repository", Kinds: kinds(glOnly)},
	{Key: "forking", Label: "forking", Kinds: kinds(both)},
	{Key: "snippets", Label: "snippets", Kinds: kinds(glOnly)},

	{Key: "builds", Label: "CI/CD", Kinds: kinds(glOnly)},

	{Key: "security_and_compliance", Label: "security & compliance", Kinds: kinds(glOnly)},
	{Key: "advanced_security", Label: "advanced security", Kinds: kinds(ghOnly)},
	{Key: "secret_scanning", Label: "secret scanning", Kinds: kinds(ghOnly)},
	{Key: "secret_scanning_push_protection", Label: "secret push protection", Kinds: kinds(ghOnly)},
	{Key: "dependabot_alerts", Label: "dependabot alerts", Kinds: kinds(ghOnly)},
	{Key: "dependabot_security_updates", Label: "dependabot updates", Kinds: kinds(ghOnly)},

	{Key: "releases", Label: "releases", Kinds: kinds(glOnly)},
	{Key: "packages", Label: "packages", Kinds: kinds(glOnly), Boolean: true},
	{Key: "container_registry", Label: "container registry", Kinds: kinds(glOnly)},
	{Key: "feature_flags", Label: "feature flags", Kinds: kinds(glOnly)},
	{Key: "pages", Label: "pages", Kinds: kinds(glOnly)},
	{Key: "model_registry", Label: "model registry", Kinds: kinds(glOnly)},
	{Key: "model_experiments", Label: "model experiments", Kinds: kinds(glOnly)},

	{Key: "environments", Label: "environments", Kinds: kinds(glOnly)},
	{Key: "infrastructure", Label: "infrastructure", Kinds: kinds(glOnly)},

	{Key: "monitor", Label: "monitor", Kinds: kinds(glOnly)},

	{Key: "analytics", Label: "analytics", Kinds: kinds(glOnly)},

	{Key: "duo", Label: "GitLab Duo", Kinds: kinds(glOnly)},

	{Key: "service_desk", Label: "service desk", Kinds: kinds(glOnly), Boolean: true},
	{Key: "lfs", Label: "git LFS", Kinds: kinds(glOnly), Boolean: true},
	{Key: "request_access", Label: "request access", Kinds: kinds(glOnly), Boolean: true},
})

func kinds(list string) []string { return strings.Split(list, ",") }

var featureIndex = map[string]FeatureDef{}

func buildCatalog(defs []FeatureDef) []FeatureDef {
	for _, def := range defs {
		featureIndex[def.Key] = def
	}
	return defs
}

func FeatureDefFor(key string) (FeatureDef, bool) {
	def, ok := featureIndex[key]
	return def, ok
}

func FeatureKeys(kind string) []string {
	var keys []string
	for _, def := range featureCatalog {
		if def.Supports(kind) {
			keys = append(keys, def.Key)
		}
	}
	return keys
}

type Features struct {
	set map[string]bool
}

func NewFeatures(pairs map[string]bool) Features {
	f := Features{}
	for key, on := range pairs {
		f.Set(key, on)
	}
	return f
}

func (f Features) Clone() Features {
	if f.set == nil {
		return Features{}
	}
	out := Features{set: make(map[string]bool, len(f.set))}
	for key, value := range f.set {
		out.set[key] = value
	}
	return out
}

func (f Features) Empty() bool { return len(f.set) == 0 }

func (f Features) Len() int { return len(f.set) }

func (f Features) Get(key string) (bool, bool) {
	value, ok := f.set[key]
	return value, ok
}

func (f Features) Has(key string) bool {
	_, ok := f.set[key]
	return ok
}

func (f *Features) Set(key string, on bool) {
	if f.set == nil {
		f.set = map[string]bool{}
	}
	f.set[key] = on
}

func (f Features) Keys() []string {
	if len(f.set) == 0 {
		return nil
	}

	keys := make([]string, 0, len(f.set))
	seen := map[string]bool{}
	for _, def := range featureCatalog {
		if f.Has(def.Key) {
			keys = append(keys, def.Key)
			seen[def.Key] = true
		}
	}

	var extra []string
	for key := range f.set {
		if !seen[key] {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)

	return append(keys, extra...)
}

func (f Features) For(kind string) (Features, []string) {
	kept := Features{}
	var dropped []string

	for _, key := range f.Keys() {
		def, known := FeatureDefFor(key)
		if !known || !def.Supports(kind) {
			dropped = append(dropped, key)
			continue
		}
		value, _ := f.Get(key)
		kept.Set(key, value)
	}

	return kept, dropped
}

func (f Features) Validate() error {
	for _, key := range f.Keys() {
		if _, ok := FeatureDefFor(key); !ok {
			return fmt.Errorf("unknown repo feature %q (allowed: %s)", key,
				strings.Join(FeatureKeys("gitlab"), ", ")+", "+strings.Join(FeatureKeys("github"), ", "))
		}
	}
	return nil
}

func (f Features) MarshalYAML() (any, error) {
	if len(f.set) == 0 {
		return nil, nil
	}
	node := &yaml.Node{Kind: yaml.MappingNode}
	for _, key := range f.Keys() {
		value, _ := f.Get(key)
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: key},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(value)})
	}
	return node, nil
}

func (f *Features) UnmarshalYAML(node *yaml.Node) error {
	var raw map[string]bool
	if err := node.Decode(&raw); err != nil {
		return fmt.Errorf("repo.features must be a map of name: true|false (%w)", err)
	}
	for key, value := range raw {
		f.Set(key, value)
	}
	return nil
}
