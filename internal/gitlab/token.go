package gitlab

import "crypto/subtle"

// TokenMatches compares two nonempty GitLab webhook tokens in constant time.
func TokenMatches(provided, expected string) bool {
	if provided == "" || expected == "" || len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}
