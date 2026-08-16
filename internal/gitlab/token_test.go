package gitlab

import "testing"

func TestTokenMatches(t *testing.T) {
	tests := []struct {
		name     string
		provided string
		expected string
		want     bool
	}{
		{name: "equal nonempty", provided: "token", expected: "token", want: true},
		{name: "different", provided: "token", expected: "other", want: false},
		{name: "empty provided", provided: "", expected: "token", want: false},
		{name: "empty expected", provided: "token", expected: "", want: false},
		{name: "both empty", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TokenMatches(tt.provided, tt.expected); got != tt.want {
				t.Fatalf("TokenMatches(%q, %q) = %v, want %v", tt.provided, tt.expected, got, tt.want)
			}
		})
	}
}
