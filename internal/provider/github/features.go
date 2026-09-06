package github

import "github.com/spaaleks/gitlift/internal/tmpl"

var featureFields = map[string]string{
	"issues":             "has_issues",
	"wiki":               "has_wiki",
	"projects":           "has_projects",
	"discussions":        "has_discussions",
	"downloads":          "has_downloads",
	"forking":            "allow_forking",
	"template":           "is_template",
	"web_commit_signoff": "web_commit_signoff_required",
}

var securityFields = map[string]string{
	"advanced_security":               "advanced_security",
	"secret_scanning":                 "secret_scanning",
	"secret_scanning_push_protection": "secret_scanning_push_protection",
}

var toggleEndpoints = map[string]string{
	"dependabot_alerts":           "vulnerability-alerts",
	"dependabot_security_updates": "automated-security-fixes",
}

func readFeatures(raw map[string]any) tmpl.Features {
	features := tmpl.Features{}

	for key, field := range featureFields {
		if on, ok := raw[field].(bool); ok {
			features.Set(key, on)
		}
	}

	analysis, _ := raw["security_and_analysis"].(map[string]any)
	for key, field := range securityFields {
		member, _ := analysis[field].(map[string]any)
		if status, ok := member["status"].(string); ok {
			features.Set(key, status == "enabled")
		}
	}

	return features
}

func featurePayload(payload map[string]any, features tmpl.Features) (changed []string, endpoints map[string]bool, ignored []string) {
	supported, dropped := features.For("github")
	endpoints = map[string]bool{}
	analysis := map[string]any{}

	for _, key := range supported.Keys() {
		on, _ := supported.Get(key)

		switch {
		case featureFields[key] != "":
			payload[featureFields[key]] = on
		case securityFields[key] != "":
			analysis[securityFields[key]] = map[string]any{"status": statusValue(on)}
		case toggleEndpoints[key] != "":
			endpoints[key] = on
		default:
			dropped = append(dropped, key)
			continue
		}
		changed = append(changed, key+"="+boolText(on))
	}

	if len(analysis) > 0 {
		payload["security_and_analysis"] = analysis
	}

	return changed, endpoints, dropped
}

func statusValue(on bool) string {
	if on {
		return "enabled"
	}
	return "disabled"
}

func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
