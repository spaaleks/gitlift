package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/spaaleks/gitlift/internal/tmpl"
)

type Status int

const (
	StatusOK Status = iota
	StatusSkipped
	StatusFailed
	StatusInfo
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusSkipped:
		return "skipped"
	case StatusFailed:
		return "failed"
	default:
		return "info"
	}
}

type Step struct {
	Scope  string
	Label  string
	Status Status
	Detail string
	Items  []string
}

func (s Step) With(items ...string) Step {
	s.Items = items
	return s
}

func OK(scope, label string) Step {
	return Step{Scope: scope, Label: label, Status: StatusOK}
}

func Skipped(scope, label, reason string) Step {
	return Step{Scope: scope, Label: label, Status: StatusSkipped, Detail: reason}
}

func Failed(scope, label string, err error) Step {
	return Step{Scope: scope, Label: label, Status: StatusFailed, Detail: Explain(err)}
}

func Info(scope, label, detail string) Step {
	return Step{Scope: scope, Label: label, Status: StatusInfo, Detail: detail}
}

type Features = tmpl.Features

type Project struct {
	ProviderName  string
	Kind          string
	ID            string
	Owner         string
	Name          string
	FullName      string
	Description   string
	Visibility    string
	DefaultBranch string
	URL           string
	Topics        []string
	Features      Features
	Archived      bool
	Empty         bool
}

type Settings struct {
	Name          string
	DisplayName   string
	Description   *string
	Visibility    *string
	DefaultBranch *string
	Topics        []string
	Features      Features
}

type Target struct {
	Owner     string
	OwnerType string
}

type Namespace struct {
	Path string
	Name string
	Kind string
}

func (n Namespace) Label() string {
	if n.Name == "" || n.Name == n.Path {
		return n.Path
	}
	return n.Path + "  (" + n.Name + ")"
}

type RuleMode string

const (
	ModeAppend  RuleMode = "append"
	ModeReplace RuleMode = "replace"
)

type Provider interface {
	Name() string
	Kind() string
	Host() string

	ListProjects(ctx context.Context) ([]Project, error)
	Namespaces(ctx context.Context) ([]Namespace, error)
	Lookup(ctx context.Context, target Target, name string) (Project, bool, error)
	Create(ctx context.Context, target Target, settings Settings) (Project, error)

	Capture(ctx context.Context, project Project) (tmpl.Spec, []Step)
	UpdateSettings(ctx context.Context, project Project, settings Settings) []Step
	ApplyBranchRules(ctx context.Context, project Project, rules []tmpl.Rule, mode RuleMode) []Step
	ApplyTagRules(ctx context.Context, project Project, rules []tmpl.TagRule, mode RuleMode) []Step
	ApplyMergeRequests(ctx context.Context, project Project, settings *tmpl.MergeRequests) []Step
	ApplyActions(ctx context.Context, project Project, actions *tmpl.Actions) []Step
	ApplyPipelines(ctx context.Context, project Project, pipelines *tmpl.Pipelines) []Step
	ApplyWebhooks(ctx context.Context, project Project, hooks []tmpl.Webhook) []Step
	ApplyIntegrations(ctx context.Context, project Project, integrations []tmpl.Integration) []Step
	ApplyVariables(ctx context.Context, project Project, variables []tmpl.Variable) []Step
}

func Bool(v bool) *bool { return &v }

func String(v string) *string { return &v }

func Int(v int) *int { return &v }

func DerefBool(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

func DerefInt(v *int, fallback int) int {
	if v == nil {
		return fallback
	}
	return *v
}

func DerefString(v *string, fallback string) string {
	if v == nil {
		return fallback
	}
	return *v
}

func AccessLevel(access string) (int, bool) {
	switch access {
	case "no_one":
		return 0, true
	case "developers":
		return 30, true
	case "maintainers":
		return 40, true
	case "admins":
		return 60, true
	default:
		return 0, false
	}
}

func AccessName(level int) string {
	switch level {
	case 0:
		return "no one"
	case 30:
		return "developers"
	case 40:
		return "maintainers"
	case 60:
		return "admins"
	default:
		return fmt.Sprintf("level %d", level)
	}
}

func IsGlob(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[]")
}

func DescribeRule(rule tmpl.Rule) string {
	name := rule.Name
	if name == "" {
		name = "branch-rules"
	}
	parts := []string{name + ": " + strings.Join(rule.Patterns, ", ")}
	if len(rule.MergeMethods) > 0 {
		parts = append(parts, "merge via "+strings.Join(rule.MergeMethods, "/")+" only")
	}
	if DerefBool(rule.AllowForcePush, false) {
		parts = append(parts, "force-push allowed")
	}
	if rule.PushAccess != "" {
		parts = append(parts, "push: "+rule.PushAccess)
	}
	if rule.MergeAccess != "" {
		parts = append(parts, "merge: "+rule.MergeAccess)
	}
	if rule.UnprotectAccess != "" {
		parts = append(parts, "unprotect: "+rule.UnprotectAccess)
	}
	if DerefBool(rule.CodeOwnerApproval, false) {
		parts = append(parts, "code-owner approval")
	}
	if rule.RequiredApprovals != nil {
		parts = append(parts, fmt.Sprintf("%d approval(s)", *rule.RequiredApprovals))
	}
	return strings.Join(parts, " · ")
}

func DescribeTagRule(rule tmpl.TagRule) string {
	name := rule.Name
	if name == "" {
		name = "tag-rules"
	}
	parts := []string{name + ": " + strings.Join(rule.Patterns, ", ")}
	if rule.CreateAccess != "" {
		parts = append(parts, "create: "+rule.CreateAccess)
	}
	return strings.Join(parts, " · ")
}

type FeatureRow struct {
	Key     string
	Label   string
	Want    bool
	Live    bool
	HasLive bool
	Note    string
}

func FeatureRows(want Features, live Features, kind string) (rows []FeatureRow, ignored []FeatureRow) {
	for _, key := range want.Keys() {
		value, _ := want.Get(key)

		def, known := tmpl.FeatureDefFor(key)
		label := key
		note := ""
		if known {
			label = def.Label
			note = def.Note
		}

		row := FeatureRow{Key: key, Label: label, Want: value, Note: note}
		if current, ok := live.Get(key); ok {
			row.Live, row.HasLive = current, true
		}

		if !known || !def.Supports(kind) {
			ignored = append(ignored, row)
			continue
		}
		rows = append(rows, row)
	}
	return rows, ignored
}
