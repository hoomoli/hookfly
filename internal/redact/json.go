// Package redact produces bounded snapshots without common secret-bearing JSON fields.
package redact

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode"
)

var sensitiveKeyParts = []string{"token", "secret", "authorization", "apikey", "password", "privatekey"}

// JSON recursively masks sensitive JSON keys and always bounds its output to limit.
func JSON(input []byte, limit int) (output []byte, truncated bool) {
	return JSONWithSecret(input, "", limit)
}

// JSONWithSecret recursively masks sensitive JSON fields and one caller-provided secret.
func JSONWithSecret(input []byte, secret string, limit int) (output []byte, truncated bool) {
	if limit <= 0 {
		return nil, len(input) > 0
	}

	var value any
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err == nil {
		var extra any
		if err := decoder.Decode(&extra); err == io.EOF {
			masked, marshalErr := json.Marshal(maskSecret(mask(value, nil), secret))
			if marshalErr == nil {
				wasTruncated := len(input) > limit || len(masked) > limit
				if len(masked) <= limit {
					return masked, wasTruncated
				}
				return boundedJSONMarker(limit), true
			}
		}
	}

	text := append([]byte(nil), input...)
	if secret != "" {
		text = bytes.ReplaceAll(text, []byte(secret), []byte("[REDACTED]"))
	}
	if len(text) <= limit {
		return text, false
	}
	return boundedText(text, limit), true
}

func maskSecret(value any, secret string) any {
	if secret == "" {
		return value
	}
	switch typed := value.(type) {
	case string:
		return strings.ReplaceAll(typed, secret, "[REDACTED]")
	case []any:
		for index := range typed {
			typed[index] = maskSecret(typed[index], secret)
		}
	case map[string]any:
		cleaned := make(map[string]any, len(typed))
		for key, child := range typed {
			cleaned[strings.ReplaceAll(key, secret, "[REDACTED]")] = maskSecret(child, secret)
		}
		return cleaned
	}
	return value
}

func mask(value any, path []string) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := normalizedKey(key)
			if sensitiveKey(normalized) || (normalized == "value" && gitLabVariablePath(path)) {
				typed[key] = "[REDACTED]"
				continue
			}
			typed[key] = mask(child, append(path, normalized))
		}
	case []any:
		for index := range typed {
			typed[index] = mask(typed[index], path)
		}
	}
	return value
}

func normalizedKey(key string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			return unicode.ToLower(character)
		}
		return -1
	}, key)
}

func sensitiveKey(normalized string) bool {
	for _, part := range sensitiveKeyParts {
		if strings.Contains(normalized, part) {
			return true
		}
	}
	return false
}

func gitLabVariablePath(path []string) bool {
	return len(path) == 2 && path[0] == "objectattributes" && path[1] == "variables"
}

func boundedText(input []byte, limit int) []byte {
	marker := []byte("...[truncated]")
	if limit <= len(marker) {
		return append([]byte(nil), marker[:limit]...)
	}
	prefixLength := limit - len(marker)
	output := make([]byte, 0, limit)
	output = append(output, input[:prefixLength]...)
	return append(output, marker...)
}

func boundedJSONMarker(limit int) []byte {
	marker := []byte(`{"truncated":true}`)
	if len(marker) <= limit {
		return marker
	}
	if limit >= len("null") {
		return []byte("null")
	}
	if limit >= len("[]") {
		return []byte("[]")
	}
	return []byte("0")
}
