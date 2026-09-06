package tmpl

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed builtin/*.yaml
var builtin embed.FS

type Repo struct {
	Visibility    *string  `yaml:"visibility,omitempty"`
	Description   *string  `yaml:"description,omitempty"`
	DefaultBranch *string  `yaml:"default_branch,omitempty"`
	Topics        []string `yaml:"topics,omitempty"`
	Features      Features `yaml:"features,omitempty"`
}

type Rule struct {
	Name              string   `yaml:"name"`
	Patterns          []string `yaml:"patterns"`
	AllowForcePush    *bool    `yaml:"allow_force_push,omitempty"`
	PushAccess        string   `yaml:"push_access,omitempty"`
	MergeAccess       string   `yaml:"merge_access,omitempty"`
	UnprotectAccess   string   `yaml:"unprotect_access,omitempty"`
	CodeOwnerApproval *bool    `yaml:"code_owner_approval,omitempty"`
	RequiredApprovals *int     `yaml:"required_approvals,omitempty"`
	MergeMethods      []string `yaml:"merge_methods,omitempty"`
}

type BranchRules struct {
	Rules []Rule `yaml:"rules"`
}

type TagRule struct {
	Name         string   `yaml:"name"`
	Patterns     []string `yaml:"patterns"`
	CreateAccess string   `yaml:"create_access,omitempty"`
}

type TagRules struct {
	Rules []TagRule `yaml:"rules"`
}

type Actions struct {
	ForkPRApproval             *string `yaml:"fork_pr_approval,omitempty"`
	OutsideAccess              *string `yaml:"outside_access,omitempty"`
	DefaultWorkflowPermissions *string `yaml:"default_workflow_permissions,omitempty"`
	CanApprovePullRequests     *bool   `yaml:"can_approve_pull_requests,omitempty"`
}

func (a *Actions) Empty() bool {
	return a == nil ||
		(a.ForkPRApproval == nil && a.OutsideAccess == nil &&
			a.DefaultWorkflowPermissions == nil && a.CanApprovePullRequests == nil)
}

type Pipelines struct {
	AutoDevOps        *bool   `yaml:"auto_devops,omitempty"`
	AutoCancelPending *bool   `yaml:"auto_cancel_pending,omitempty"`
	PublicPipelines   *bool   `yaml:"public_pipelines,omitempty"`
	GitStrategy       *string `yaml:"git_strategy,omitempty"`
	GitDepth          *int    `yaml:"git_depth,omitempty"`
	Timeout           *string `yaml:"timeout,omitempty"`
	ConfigPath        *string `yaml:"config_path,omitempty"`
	ForwardDeployment *bool   `yaml:"forward_deployment,omitempty"`
	SeparatedCaches   *bool   `yaml:"separated_caches,omitempty"`
	ForkPipelines     *bool   `yaml:"allow_fork_pipelines,omitempty"`
	SharedRunners     *bool   `yaml:"shared_runners,omitempty"`
	GroupRunners      *bool   `yaml:"group_runners,omitempty"`
	JobTokenScope     *bool   `yaml:"restrict_job_token_scope,omitempty"`
}

func (p *Pipelines) Empty() bool {
	if p == nil {
		return true
	}
	return p.AutoDevOps == nil && p.AutoCancelPending == nil && p.PublicPipelines == nil &&
		p.GitStrategy == nil && p.GitDepth == nil && p.Timeout == nil && p.ConfigPath == nil &&
		p.ForwardDeployment == nil && p.SeparatedCaches == nil && p.ForkPipelines == nil &&
		p.SharedRunners == nil && p.GroupRunners == nil && p.JobTokenScope == nil
}

type MergeRequests struct {
	MergeMethod          *string `yaml:"merge_method,omitempty"`
	Squash               *string `yaml:"squash,omitempty"`
	DeleteSourceBranch   *bool   `yaml:"delete_source_branch,omitempty"`
	PipelineMustSucceed  *bool   `yaml:"pipeline_must_succeed,omitempty"`
	AllThreadsResolved   *bool   `yaml:"all_threads_resolved,omitempty"`
	SkippedPipelineOK    *bool   `yaml:"skipped_pipeline_allowed,omitempty"`
	ApprovalsRequired    *int    `yaml:"approvals_required,omitempty"`
	ResetApprovalsOnPush *bool   `yaml:"reset_approvals_on_push,omitempty"`
	AuthorMayApprove     *bool   `yaml:"author_may_approve,omitempty"`
	CommitterMayApprove  *bool   `yaml:"committer_may_approve,omitempty"`
	AllowAutoMerge       *bool   `yaml:"allow_auto_merge,omitempty"`
}

func (m *MergeRequests) Empty() bool {
	if m == nil {
		return true
	}
	return m.MergeMethod == nil && m.Squash == nil && m.DeleteSourceBranch == nil &&
		m.PipelineMustSucceed == nil && m.AllThreadsResolved == nil && m.SkippedPipelineOK == nil &&
		m.ApprovalsRequired == nil && m.ResetApprovalsOnPush == nil && m.AuthorMayApprove == nil &&
		m.CommitterMayApprove == nil && m.AllowAutoMerge == nil
}

func (m *MergeRequests) Approvals() bool {
	return m != nil && (m.ApprovalsRequired != nil || m.ResetApprovalsOnPush != nil ||
		m.AuthorMayApprove != nil || m.CommitterMayApprove != nil)
}

type Webhook struct {
	URL             string   `yaml:"url"`
	Secret          string   `yaml:"secret,omitempty"`
	Events          []string `yaml:"events,omitempty"`
	SSLVerification *bool    `yaml:"ssl_verification,omitempty"`
	BranchFilter    string   `yaml:"push_branch_filter,omitempty"`
	ContentType     string   `yaml:"content_type,omitempty"`
}

type Integration struct {
	Name     string         `yaml:"name"`
	Enabled  *bool          `yaml:"enabled,omitempty"`
	Events   []string       `yaml:"events,omitempty"`
	Settings map[string]any `yaml:"settings,omitempty"`
}

type Variable struct {
	Key              string `yaml:"key"`
	Value            string `yaml:"value"`
	Masked           *bool  `yaml:"masked,omitempty"`
	Protected        *bool  `yaml:"protected,omitempty"`
	Raw              *bool  `yaml:"raw,omitempty"`
	Type             string `yaml:"type,omitempty"`
	EnvironmentScope string `yaml:"environment_scope,omitempty"`
	Description      string `yaml:"description,omitempty"`
	Visibility       string `yaml:"visibility,omitempty"`
}

func (v Variable) Secret() bool { return v.Visibility == "private" }

func (v Variable) Scope() string {
	if v.EnvironmentScope == "" {
		return "*"
	}
	return v.EnvironmentScope
}

type Context struct {
	Owner     string `yaml:"owner,omitempty"`
	OwnerType string `yaml:"owner_type,omitempty"`
	Namespace string `yaml:"namespace,omitempty"`
}

type Spec struct {
	Name          string         `yaml:"name,omitempty"`
	Description   string         `yaml:"description,omitempty"`
	Repo          *Repo          `yaml:"repo,omitempty"`
	BranchRules   *BranchRules   `yaml:"branch_rules,omitempty"`
	TagRules      *TagRules      `yaml:"tag_rules,omitempty"`
	MergeRequests *MergeRequests `yaml:"merge_requests,omitempty"`
	Actions       *Actions       `yaml:"actions,omitempty"`
	Pipelines     *Pipelines     `yaml:"pipelines,omitempty"`
	Webhooks      []Webhook      `yaml:"webhooks,omitempty"`
	Integrations  []Integration  `yaml:"integrations,omitempty"`
	Variables     []Variable     `yaml:"ci_cd_variables,omitempty"`
	Context       Context        `yaml:"context,omitempty"`
}

func (s Spec) Rules() []Rule {
	if s.BranchRules == nil {
		return nil
	}
	return s.BranchRules.Rules
}

func (s Spec) Tags() []TagRule {
	if s.TagRules == nil {
		return nil
	}
	return s.TagRules.Rules
}

func (s Spec) Features() Features {
	if s.Repo == nil {
		return Features{}
	}
	return s.Repo.Features
}

type Template struct {
	Path        string
	Scope       string
	Builtin     bool
	Name        string
	Description string

	raw          map[string]any
	providerKeys []string
}

func (t Template) Supports(kind string) bool {
	if len(t.providerKeys) == 0 {
		return true
	}
	for _, key := range t.providerKeys {
		if key == kind {
			return true
		}
	}
	return false
}

func (t Template) ProviderLabel() string {
	if len(t.providerKeys) == 0 {
		return "all"
	}
	return strings.Join(t.providerKeys, ", ")
}

func (t Template) For(kind string) (Spec, error) {
	merged := deepCopyMap(t.raw)

	overrides, _ := merged["providers"].(map[string]any)
	delete(merged, "providers")

	if overrides != nil {
		if override, ok := overrides[kind].(map[string]any); ok {
			merged = mergeMaps(merged, override)
		}
	}

	encoded, err := yaml.Marshal(merged)
	if err != nil {
		return Spec{}, fmt.Errorf("template %s: %w", t.Name, err)
	}

	var spec Spec
	if err := yaml.Unmarshal(encoded, &spec); err != nil {
		return Spec{}, fmt.Errorf("template %s: %w", t.Name, err)
	}
	if spec.Name == "" {
		spec.Name = t.Name
	}

	if err := spec.Validate(kind); err != nil {
		return Spec{}, fmt.Errorf("template %s: %w", t.Name, err)
	}
	return spec, nil
}

var (
	visibilities   = []string{"private", "internal", "public"}
	mergeMethods   = []string{"merge", "squash", "rebase"}
	accessLevels   = []string{"no_one", "admins", "maintainers", "developers"}
	forkApprovals  = []string{"first_time_contributors_new_to_github", "first_time_contributors", "all_external_contributors"}
	outsideAccess  = []string{"none", "user", "organization"}
	workflowPerms  = []string{"read", "write"}
	visibilityKind = []string{"private", "all", "selected"}
	projectMerge   = []string{"merge", "rebase", "fast_forward"}
	squashOptions  = []string{"never", "default_off", "default_on", "always"}
	gitStrategies  = []string{"clone", "fetch"}
	variableTypes  = []string{"env_var", "file"}

	hookEvents = []string{
		"push", "tag_push", "issues", "merge_request", "note",
		"pipeline", "job", "wiki_page", "release", "deployment",
	}
)

func Visibilities() []string { return append([]string(nil), visibilities...) }

func AccessLevels() []string { return append([]string(nil), accessLevels...) }

func HookEvents() []string { return append([]string(nil), hookEvents...) }

func (s Spec) Validate(kind string) error {
	if s.Repo != nil {
		if s.Repo.Visibility != nil {
			if err := oneOf(*s.Repo.Visibility, visibilities, "repo.visibility"); err != nil {
				return err
			}
		}
		if err := s.Repo.Features.Validate(); err != nil {
			return err
		}
	}

	for _, rule := range s.Rules() {
		for field, value := range map[string]string{
			"push_access":      rule.PushAccess,
			"merge_access":     rule.MergeAccess,
			"unprotect_access": rule.UnprotectAccess,
		} {
			if value == "" {
				continue
			}
			if err := oneOf(value, accessLevels, "branch rule "+rule.Name+" "+field); err != nil {
				return err
			}
		}
		for _, method := range rule.MergeMethods {
			if err := oneOf(method, mergeMethods, "branch rule "+rule.Name+" merge_methods"); err != nil {
				return err
			}
		}
		if rule.RequiredApprovals != nil && *rule.RequiredApprovals < 0 {
			return fmt.Errorf("branch rule %s required_approvals cannot be negative", rule.Name)
		}
	}

	for _, rule := range s.Tags() {
		if rule.CreateAccess != "" {
			if err := oneOf(rule.CreateAccess, accessLevels, "tag rule "+rule.Name+" create_access"); err != nil {
				return err
			}
		}
	}

	if m := s.MergeRequests; m != nil {
		if m.MergeMethod != nil {
			if err := oneOf(*m.MergeMethod, projectMerge, "merge_requests.merge_method"); err != nil {
				return err
			}
		}
		if m.Squash != nil {
			if err := oneOf(*m.Squash, squashOptions, "merge_requests.squash"); err != nil {
				return err
			}
		}
		if m.ApprovalsRequired != nil && *m.ApprovalsRequired < 0 {
			return fmt.Errorf("merge_requests.approvals_required cannot be negative")
		}
	}

	if p := s.Pipelines; p != nil {
		if p.GitStrategy != nil {
			if err := oneOf(*p.GitStrategy, gitStrategies, "pipelines.git_strategy"); err != nil {
				return err
			}
		}
		if p.Timeout != nil {
			if _, err := ParseTimeout(*p.Timeout); err != nil {
				return fmt.Errorf("pipelines.timeout: %w", err)
			}
		}
		if p.GitDepth != nil && *p.GitDepth < 0 {
			return fmt.Errorf("pipelines.git_depth cannot be negative")
		}
	}

	if a := s.Actions; a != nil {
		if a.ForkPRApproval != nil {
			if err := oneOf(*a.ForkPRApproval, forkApprovals, "actions.fork_pr_approval"); err != nil {
				return err
			}
		}
		if a.OutsideAccess != nil {
			if err := oneOf(*a.OutsideAccess, outsideAccess, "actions.outside_access"); err != nil {
				return err
			}
		}
		if a.DefaultWorkflowPermissions != nil {
			if err := oneOf(*a.DefaultWorkflowPermissions, workflowPerms, "actions.default_workflow_permissions"); err != nil {
				return err
			}
		}
	}

	for index, hook := range s.Webhooks {
		if strings.TrimSpace(hook.URL) == "" {
			return fmt.Errorf("webhooks[%d] has no url", index)
		}
		for _, event := range hook.Events {
			if err := oneOf(event, hookEvents, "webhooks["+hook.URL+"].events"); err != nil {
				return err
			}
		}
	}

	for index, integration := range s.Integrations {
		if strings.TrimSpace(integration.Name) == "" {
			return fmt.Errorf("integrations[%d] has no name", index)
		}
		for _, event := range integration.Events {
			if err := oneOf(event, hookEvents, "integrations["+integration.Name+"].events"); err != nil {
				return err
			}
		}
	}

	for _, variable := range s.Variables {
		if variable.Key == "" {
			return fmt.Errorf("ci_cd_variables: an entry has no key")
		}
		if variable.Type != "" {
			if err := oneOf(variable.Type, variableTypes, "ci_cd_variables["+variable.Key+"].type"); err != nil {
				return err
			}
		}
		if kind == "github" && variable.Visibility != "" {
			if err := oneOf(variable.Visibility, visibilityKind, "ci_cd_variables["+variable.Key+"].visibility"); err != nil {
				return err
			}
		}
	}

	return nil
}

func ParseTimeout(value string) (int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, fmt.Errorf("empty")
	}
	if seconds, err := strconv.Atoi(trimmed); err == nil {
		if seconds <= 0 {
			return 0, fmt.Errorf("must be positive")
		}
		return seconds, nil
	}
	parsed, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, fmt.Errorf("%q is neither a number of seconds nor a duration like 1h30m", value)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("must be positive")
	}
	return int(parsed.Seconds()), nil
}

func oneOf(value string, allowed []string, field string) error {
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return fmt.Errorf("unknown %s %q (allowed: %s)", field, value, strings.Join(allowed, ", "))
}

func Load(dirs []string) ([]Template, error) {
	byName := map[string]int{}
	var templates []Template

	add := func(t Template) {
		if index, seen := byName[t.Name]; seen {
			templates[index] = t
			return
		}
		byName[t.Name] = len(templates)
		templates = append(templates, t)
	}

	entries, err := fs.ReadDir(builtin, "builtin")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		raw, err := builtin.ReadFile("builtin/" + entry.Name())
		if err != nil {
			return nil, err
		}
		t, err := parse(raw, "builtin/"+entry.Name(), "builtin")
		if err != nil {
			return nil, err
		}
		t.Builtin = true
		add(t)
	}

	var problems []string
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		found, errs := loadDir(dir)
		problems = append(problems, errs...)
		for _, t := range found {
			add(t)
		}
	}

	sort.SliceStable(templates, func(a, b int) bool {
		return naturalLess(templates[a].Name, templates[b].Name)
	})

	if len(problems) > 0 {
		return templates, fmt.Errorf("%s", strings.Join(problems, ", "))
	}
	return templates, nil
}

func loadDir(dir string) ([]Template, []string) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}

	var templates []Template
	var problems []string

	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
			return nil
		}

		scope := "custom"
		if rel, err := filepath.Rel(dir, path); err == nil {
			if head, _, found := strings.Cut(rel, string(filepath.Separator)); found {
				scope = head
			}
		}

		t, err := parse(raw, path, scope)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", filepath.Base(path), err))
			return nil
		}
		templates = append(templates, t)
		return nil
	})

	return templates, problems
}

func parse(raw []byte, path, scope string) (Template, error) {
	var document map[string]any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return Template{}, err
	}
	if document == nil {
		document = map[string]any{}
	}

	t := Template{Path: path, Scope: scope, raw: document}

	if name, ok := document["name"].(string); ok && name != "" {
		t.Name = name
	} else {
		t.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if description, ok := document["description"].(string); ok {
		t.Description = description
	}

	if providers, ok := document["providers"].(map[string]any); ok {
		keys := make([]string, 0, len(providers))
		for key := range providers {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		t.providerKeys = keys
	}

	return t, nil
}

func SearchDirs() []string {
	var dirs []string

	if home, err := os.UserHomeDir(); err == nil {
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		dirs = append(dirs, filepath.Join(base, "gitlift", "templates"))
	}
	if cwd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(cwd, "templates"))
	}
	if dir := os.Getenv("GITLIFT_TEMPLATES"); dir != "" {
		dirs = append(dirs, dir)
	}

	return dirs
}

func naturalLess(a, b string) bool {
	x, y := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	i, j := 0, 0

	for i < len(x) && j < len(y) {
		if isDigit(x[i]) && isDigit(y[j]) {
			ni, si := readNumber(x, i)
			nj, sj := readNumber(y, j)
			if ni != nj {
				return ni < nj
			}
			i, j = si, sj
			continue
		}
		if x[i] != y[j] {
			return x[i] < y[j]
		}
		i++
		j++
	}
	return len(x)-i < len(y)-j
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func readNumber(s []rune, from int) (int, int) {
	value := 0
	i := from
	for i < len(s) && isDigit(s[i]) {
		value = value*10 + int(s[i]-'0')
		i++
	}
	return value, i
}
