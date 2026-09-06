package tmpl

func mergeMaps(base, override map[string]any) map[string]any {
	out := deepCopyMap(base)

	for key, value := range override {
		if nested, ok := value.(map[string]any); ok {
			if existing, ok := out[key].(map[string]any); ok {
				out[key] = mergeMaps(existing, nested)
				continue
			}
		}
		out[key] = deepCopy(value)
	}

	return out
}

func deepCopyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = deepCopy(value)
	}
	return out
}

func deepCopy(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return deepCopyMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = deepCopy(item)
		}
		return out
	default:
		return value
	}
}
