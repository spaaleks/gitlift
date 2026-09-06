package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/spaaleks/gitlift/internal/provider"
	"github.com/spaaleks/gitlift/internal/tmpl"
)

type Plan struct {
	Provider provider.Provider
	Template string

	Create  bool
	Target  provider.Target
	Project provider.Project

	Settings      provider.Settings
	Rules         []tmpl.Rule
	TagRules      []tmpl.TagRule
	RuleMode      provider.RuleMode
	MergeRequests *tmpl.MergeRequests
	Actions       *tmpl.Actions
	Pipelines     *tmpl.Pipelines
	Webhooks      []tmpl.Webhook
	Integrations  []tmpl.Integration
	Variables     []tmpl.Variable
}

type Result struct {
	Project provider.Project
	Steps   []provider.Step
}

func Run(ctx context.Context, plan Plan, emit func(provider.Step)) Result {
	send := func(steps ...provider.Step) []provider.Step {
		for _, step := range steps {
			if emit != nil {
				emit(step)
			}
		}
		return steps
	}

	result := Result{Project: plan.Project}

	if plan.Create {
		created, err := plan.Provider.Create(ctx, plan.Target, plan.Settings)
		if err != nil {
			result.Steps = send(provider.Failed("create", "create "+plan.Settings.Name, err))
			return result
		}
		result.Project = created
		result.Steps = send(provider.Step{
			Scope: "create", Label: "created " + created.FullName,
			Status: provider.StatusOK, Detail: created.URL,
		})
	} else {
		result.Steps = append(result.Steps, send(plan.Provider.UpdateSettings(ctx, result.Project, plan.Settings)...)...)
	}

	if result.Project.Archived {
		result.Steps = append(result.Steps, send(provider.Skipped("branch rules", "all rules",
			"the repository is archived and read-only"))...)
		return result
	}

	apply := []func() []provider.Step{
		func() []provider.Step {
			return plan.Provider.ApplyMergeRequests(ctx, result.Project, plan.MergeRequests)
		},
		func() []provider.Step {
			return plan.Provider.ApplyBranchRules(ctx, result.Project, plan.Rules, plan.RuleMode)
		},
		func() []provider.Step {
			return plan.Provider.ApplyTagRules(ctx, result.Project, plan.TagRules, plan.RuleMode)
		},
		func() []provider.Step { return plan.Provider.ApplyActions(ctx, result.Project, plan.Actions) },
		func() []provider.Step { return plan.Provider.ApplyPipelines(ctx, result.Project, plan.Pipelines) },
		func() []provider.Step { return plan.Provider.ApplyWebhooks(ctx, result.Project, plan.Webhooks) },
		func() []provider.Step {
			return plan.Provider.ApplyIntegrations(ctx, result.Project, plan.Integrations)
		},
		func() []provider.Step { return plan.Provider.ApplyVariables(ctx, result.Project, plan.Variables) },
	}

	for _, step := range apply {
		result.Steps = append(result.Steps, send(step()...)...)
	}

	return result
}

func Settings(spec tmpl.Spec, name, displayName string) provider.Settings {
	settings := provider.Settings{Name: name, DisplayName: displayName}

	if spec.Repo != nil {
		settings.Visibility = spec.Repo.Visibility
		settings.Description = spec.Repo.Description
		settings.DefaultBranch = spec.Repo.DefaultBranch
		settings.Topics = spec.Repo.Topics
		settings.Features = spec.Repo.Features.Clone()
	}

	return settings
}

func (p Plan) withSpec(spec tmpl.Spec) Plan {
	p.Rules = spec.Rules()
	p.TagRules = spec.Tags()
	p.MergeRequests = spec.MergeRequests
	p.Actions = spec.Actions
	p.Pipelines = spec.Pipelines
	p.Webhooks = spec.Webhooks
	p.Integrations = spec.Integrations
	return p
}

func FromSpec(base Plan, spec tmpl.Spec) Plan { return base.withSpec(spec) }

func (p Plan) Summary() string {
	var b strings.Builder

	line := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}

	line("Provider     %s (%s · %s)", p.Provider.Name(), p.Provider.Kind(), p.Provider.Host())
	line("Template     %s", p.Template)

	if p.Create {
		place := p.Target.Owner
		if place == "" {
			place = "(your personal namespace)"
		} else if p.Provider.Kind() == "github" {
			place += " (" + p.Target.OwnerType + ")"
		}
		line("Action       create a new repository")
		line("Location     %s", place)
		line("Name         %s", p.Settings.Name)
		if p.Settings.DisplayName != "" {
			line("Display name %s", p.Settings.DisplayName)
		}
	} else {
		line("Action       update an existing repository")
		line("Target       %s", p.Project.FullName)
		line("URL          %s", p.Project.URL)
	}

	kind := p.Provider.Kind()

	sections := []string{
		describeSettings(p.Settings, p.Create, p.Project, kind),
		describeRules(p.Rules, p.RuleMode),
		describeTagRules(p.TagRules, p.RuleMode),
		describeMergeRequests(p.MergeRequests),
		describeActions(p.Actions, kind),
		describePipelines(p.Pipelines, kind),
		describeWebhooks(p.Webhooks),
		describeIntegrations(p.Integrations, kind),
		describeVariables(p.Variables, kind),
	}

	for _, section := range sections {
		if section == "" {
			continue
		}
		b.WriteString("\n")
		b.WriteString(section)
	}

	return b.String()
}

func describeSettings(settings provider.Settings, create bool, current provider.Project, kind string) string {
	var b strings.Builder
	b.WriteString("Repository settings\n")

	wrote := false
	write := func(label, value, was string) {
		wrote = true
		if !create && was != "" && was != value {
			fmt.Fprintf(&b, "  %-22s %s  (was %s)\n", label, value, was)
			return
		}
		fmt.Fprintf(&b, "  %-22s %s\n", label, value)
	}

	if settings.Visibility != nil {
		write("visibility", *settings.Visibility, current.Visibility)
	}
	if settings.Description != nil {
		value := *settings.Description
		if value == "" {
			value = "(empty)"
		}
		write("description", value, current.Description)
	}
	if settings.DefaultBranch != nil {
		write("default branch", *settings.DefaultBranch, current.DefaultBranch)
	}
	if len(settings.Topics) > 0 {
		write("topics", strings.Join(settings.Topics, ", "), strings.Join(current.Topics, ", "))
	}

	rows, ignored := provider.FeatureRows(settings.Features, current.Features, kind)
	for _, row := range rows {
		was := ""
		if row.HasLive {
			was = boolText(row.Live)
		}
		write(row.Label, boolText(row.Want), was)
	}
	for _, row := range ignored {
		wrote = true
		reason := row.Note
		if reason == "" {
			reason = "not available on " + kind
		}
		fmt.Fprintf(&b, "  %-22s %s  (ignored, %s)\n", row.Label, boolText(row.Want), reason)
	}

	if !wrote {
		b.WriteString("  (the template declares none, nothing is touched)\n")
	}
	return b.String()
}

func describeRules(rules []tmpl.Rule, mode provider.RuleMode) string {
	var b strings.Builder

	if len(rules) == 0 {
		return "Branch rules\n  (the template declares none, nothing is touched)\n"
	}

	fmt.Fprintf(&b, "Branch rules (%s)\n", mode)
	for _, rule := range rules {
		fmt.Fprintf(&b, "  %s\n", provider.DescribeRule(rule))
	}
	if mode == provider.ModeReplace {
		b.WriteString("  rules not listed above will be REMOVED\n")
	}
	return b.String()
}

func describeTagRules(rules []tmpl.TagRule, mode provider.RuleMode) string {
	if len(rules) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Tag rules (%s)\n", mode)
	for _, rule := range rules {
		fmt.Fprintf(&b, "  %s\n", provider.DescribeTagRule(rule))
	}
	if mode == provider.ModeReplace {
		b.WriteString("  tag rules not listed above will be REMOVED\n")
	}
	return b.String()
}

func describeMergeRequests(settings *tmpl.MergeRequests) string {
	if settings.Empty() {
		return ""
	}

	var b strings.Builder
	b.WriteString("Merge requests\n")

	row := func(label string, value any) {
		fmt.Fprintf(&b, "  %-28s %v\n", label, value)
	}

	if settings.MergeMethod != nil {
		row("merge method", *settings.MergeMethod)
	}
	if settings.Squash != nil {
		row("squash", *settings.Squash)
	}
	if settings.DeleteSourceBranch != nil {
		row("delete source branch", boolText(*settings.DeleteSourceBranch))
	}
	if settings.PipelineMustSucceed != nil {
		row("pipeline must succeed", boolText(*settings.PipelineMustSucceed))
	}
	if settings.AllThreadsResolved != nil {
		row("all threads resolved", boolText(*settings.AllThreadsResolved))
	}
	if settings.SkippedPipelineOK != nil {
		row("skipped pipeline allowed", boolText(*settings.SkippedPipelineOK))
	}
	if settings.ApprovalsRequired != nil {
		row("approvals required", *settings.ApprovalsRequired)
	}
	if settings.ResetApprovalsOnPush != nil {
		row("reset approvals on push", boolText(*settings.ResetApprovalsOnPush))
	}
	if settings.AuthorMayApprove != nil {
		row("author may approve", boolText(*settings.AuthorMayApprove))
	}
	if settings.CommitterMayApprove != nil {
		row("committer may approve", boolText(*settings.CommitterMayApprove))
	}
	if settings.AllowAutoMerge != nil {
		row("allow auto merge", boolText(*settings.AllowAutoMerge))
	}

	return b.String()
}

func describeActions(actions *tmpl.Actions, kind string) string {
	if actions.Empty() {
		if kind == "github" {
			return "Actions\n  (the template declares none, nothing is touched)\n"
		}
		return ""
	}

	var b strings.Builder
	b.WriteString("Actions\n")

	if kind != "github" {
		b.WriteString("  (GitHub only, ignored here)\n")
		return b.String()
	}

	if actions.ForkPRApproval != nil {
		fmt.Fprintf(&b, "  %-28s %s\n", "fork PR approval", *actions.ForkPRApproval)
	}
	if actions.OutsideAccess != nil {
		fmt.Fprintf(&b, "  %-28s %s\n", "outside access", *actions.OutsideAccess)
	}
	if actions.DefaultWorkflowPermissions != nil {
		fmt.Fprintf(&b, "  %-28s %s\n", "default workflow permissions", *actions.DefaultWorkflowPermissions)
	}
	if actions.CanApprovePullRequests != nil {
		fmt.Fprintf(&b, "  %-28s %s\n", "can approve pull requests", boolText(*actions.CanApprovePullRequests))
	}
	return b.String()
}

func describePipelines(pipelines *tmpl.Pipelines, kind string) string {
	if pipelines.Empty() {
		return ""
	}

	var b strings.Builder
	b.WriteString("Pipelines\n")

	if kind != "gitlab" {
		b.WriteString("  (GitLab only, ignored here)\n")
		return b.String()
	}

	row := func(label string, value any) {
		fmt.Fprintf(&b, "  %-28s %v\n", label, value)
	}

	if pipelines.AutoDevOps != nil {
		row("auto devops", boolText(*pipelines.AutoDevOps))
	}
	if pipelines.AutoCancelPending != nil {
		row("auto-cancel pending", boolText(*pipelines.AutoCancelPending))
	}
	if pipelines.PublicPipelines != nil {
		row("public pipelines", boolText(*pipelines.PublicPipelines))
	}
	if pipelines.GitStrategy != nil {
		row("git strategy", *pipelines.GitStrategy)
	}
	if pipelines.GitDepth != nil {
		row("git depth", *pipelines.GitDepth)
	}
	if pipelines.Timeout != nil {
		row("timeout", *pipelines.Timeout)
	}
	if pipelines.ConfigPath != nil {
		row("config path", *pipelines.ConfigPath)
	}
	if pipelines.ForwardDeployment != nil {
		row("forward deployment", boolText(*pipelines.ForwardDeployment))
	}
	if pipelines.SeparatedCaches != nil {
		row("separated caches", boolText(*pipelines.SeparatedCaches))
	}
	if pipelines.ForkPipelines != nil {
		row("allow fork pipelines", boolText(*pipelines.ForkPipelines))
	}
	if pipelines.SharedRunners != nil {
		row("shared runners", boolText(*pipelines.SharedRunners))
	}
	if pipelines.GroupRunners != nil {
		row("group runners", boolText(*pipelines.GroupRunners))
	}
	if pipelines.JobTokenScope != nil {
		row("restrict job token scope", boolText(*pipelines.JobTokenScope))
	}

	return b.String()
}

func describeWebhooks(hooks []tmpl.Webhook) string {
	if len(hooks) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Webhooks (%d)\n", len(hooks))
	for _, hook := range hooks {
		events := strings.Join(hook.Events, ", ")
		if events == "" {
			events = "push"
		}
		secret := ""
		if hook.Secret != "" {
			secret = "  " + mask(hook.Secret)
		}
		fmt.Fprintf(&b, "  %-44s %s%s\n", hook.URL, events, secret)
	}
	return b.String()
}

func describeIntegrations(integrations []tmpl.Integration, kind string) string {
	if len(integrations) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Integrations (%d)\n", len(integrations))

	if kind != "gitlab" {
		b.WriteString("  (GitLab only, ignored here)\n")
		return b.String()
	}

	for _, integration := range integrations {
		state := "enabled"
		if integration.Enabled != nil && !*integration.Enabled {
			state = "disabled"
		}
		fmt.Fprintf(&b, "  %-24s %-10s %s\n", integration.Name, state, strings.Join(integration.Events, ", "))
	}
	return b.String()
}

func describeVariables(variables []tmpl.Variable, kind string) string {
	var b strings.Builder

	if len(variables) == 0 {
		b.WriteString("CI/CD variables\n  (none)\n")
		return b.String()
	}

	fmt.Fprintf(&b, "CI/CD variables (%d)\n", len(variables))
	for _, variable := range variables {
		var notes []string
		switch {
		case kind == "github" && variable.Secret():
			notes = append(notes, "encrypted secret")
		case kind == "github":
			notes = append(notes, "actions variable")
		case provider.DerefBool(variable.Masked, false):
			notes = append(notes, "masked")
		}
		if kind == "gitlab" {
			if variable.EnvironmentScope != "" {
				notes = append(notes, "scope "+variable.EnvironmentScope)
			}
			if variable.Type == "file" {
				notes = append(notes, "file")
			}
		}
		note := strings.Join(notes, ", ")
		value := variable.Value
		if kind == "github" && variable.Secret() || provider.DerefBool(variable.Masked, false) {
			value = mask(value)
		}
		if value == "" {
			value = "(empty)"
		}
		fmt.Fprintf(&b, "  %-24s %-28s %s\n", variable.Key, value, note)
	}
	return b.String()
}

func mask(value string) string {
	if value == "" {
		return ""
	}
	return strings.Repeat("•", min(len(value), 12))
}

func boolText(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
