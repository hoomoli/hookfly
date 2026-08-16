package gitlab

import (
	"strings"
	"unicode"
)

func normalizedEventName(header string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(header))
	switch normalized {
	case "pipeline hook":
		return "pipeline", true
	case "job hook":
		return "job", true
	case "push hook":
		return "push", true
	case "tag push hook":
		return "tag_push", true
	case "merge request hook":
		return "merge_request", true
	}

	words := strings.FieldsFunc(normalized, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	return strings.Join(words, "_"), false
}

func stringAt(payload map[string]any, path ...string) string {
	value, ok := valueAt(payload, path...)
	if !ok {
		return ""
	}
	stringValue, _ := value.(string)
	return stringValue
}

func lastObjectStringAt(payload map[string]any, arrayKey, valueKey string) string {
	values, ok := payload[arrayKey].([]any)
	if !ok || len(values) == 0 {
		return ""
	}
	last, ok := values[len(values)-1].(map[string]any)
	if !ok {
		return ""
	}
	value, _ := last[valueKey].(string)
	return value
}

func valueAt(payload map[string]any, path ...string) (any, bool) {
	var current any = payload
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func lowercase(value string) string {
	return strings.ToLower(value)
}
