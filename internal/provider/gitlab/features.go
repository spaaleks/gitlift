package gitlab

import "github.com/spaaleks/gitlift/internal/tmpl"

var featureFields = map[string]string{
	"issues":                  "issues_access_level",
	"merge_requests":          "merge_requests_access_level",
	"requirements":            "requirements_access_level",
	"wiki":                    "wiki_access_level",
	"repository":              "repository_access_level",
	"forking":                 "forking_access_level",
	"snippets":                "snippets_access_level",
	"builds":                  "builds_access_level",
	"security_and_compliance": "security_and_compliance_access_level",
	"releases":                "releases_access_level",
	"container_registry":      "container_registry_access_level",
	"feature_flags":           "feature_flags_access_level",
	"pages":                   "pages_access_level",
	"model_registry":          "model_registry_access_level",
	"model_experiments":       "model_experiments_access_level",
	"environments":            "environments_access_level",
	"infrastructure":          "infrastructure_access_level",
	"monitor":                 "monitor_access_level",
	"analytics":               "analytics_access_level",

	"packages":       "packages_enabled",
	"service_desk":   "service_desk_enabled",
	"lfs":            "lfs_enabled",
	"request_access": "request_access_enabled",
}

func fieldFor(key string) (string, bool, bool) {
	field, ok := featureFields[key]
	if !ok {
		return "", false, false
	}
	def, known := tmpl.FeatureDefFor(key)
	return field, known && def.Boolean, true
}

func accessValue(on bool) string {
	if on {
		return "enabled"
	}
	return "disabled"
}

func readFeatures(raw map[string]any) tmpl.Features {
	features := tmpl.Features{}

	for _, key := range tmpl.FeatureKeys("gitlab") {
		field, boolean, ok := fieldFor(key)
		if !ok {
			continue
		}
		value, present := raw[field]
		if !present {
			continue
		}

		if boolean {
			if on, ok := value.(bool); ok {
				features.Set(key, on)
			}
			continue
		}
		if level, ok := value.(string); ok {
			features.Set(key, level != "disabled")
		}
	}

	return features
}

func featurePayload(payload map[string]any, features tmpl.Features) (changed []string, graph map[string]bool, ignored []string) {
	supported, dropped := features.For("gitlab")
	graph = map[string]bool{}

	for _, key := range supported.Keys() {
		on, _ := supported.Get(key)

		if graphqlFeatures[key] {
			graph[key] = on
			continue
		}

		field, boolean, ok := fieldFor(key)
		if !ok {
			dropped = append(dropped, key)
			continue
		}

		if boolean {
			payload[field] = on
		} else {
			payload[field] = accessValue(on)
		}
		changed = append(changed, key+"="+boolText(on))
	}

	return changed, graph, dropped
}

func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
