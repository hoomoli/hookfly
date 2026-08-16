package matcher

import (
	"testing"

	"github.com/hoomoli/hookfly/internal/config"
	"github.com/hoomoli/hookfly/internal/domain"
)

func TestFirstCanonicalMatchesRequiredAndOptionalRoutePredicates(t *testing.T) {
	ref, status, revision, externalID, trigger := "main", "success", "deadbeef", "42", "push"
	routes := []config.Route{
		{
			ID: "record", Priority: canonicalIntPointer(10),
			Match:  config.RouteMatch{Source: "gitlab", Repository: "app", Event: "pipeline"},
			Action: config.Action{Type: "record_only"},
		},
		{
			ID: "deploy", Priority: canonicalIntPointer(20),
			Match: config.RouteMatch{
				Source: "gitlab", Repository: "app", Event: "pipeline",
				Ref: &ref, Status: &status, Revision: &revision, ExternalID: &externalID, Trigger: &trigger,
			},
			Action: config.Action{Type: "deploy", Targets: []string{"production"}},
		},
	}
	event := domain.CanonicalEvent{
		Provider: "gitlab", Source: "gitlab", Repository: "app", Event: "pipeline",
		Ref: "main", Status: "success", Revision: "deadbeef", ExternalID: "42", Trigger: "push",
	}

	got, ok := FirstCanonical(routes, event)
	if !ok || got.ID != "record" {
		t.Fatalf("FirstCanonical() = %#v, matched = %v, want record", got, ok)
	}
	if got, ok := FirstCanonical(routes[1:], event); !ok || got.ID != "deploy" {
		t.Fatalf("FirstCanonical() optional match = %#v, matched = %v, want deploy", got, ok)
	}
	event.Status = "failed"
	if got, ok := FirstCanonical(routes[1:], event); ok || got != nil {
		t.Fatalf("FirstCanonical() = %#v, matched = %v, want no match", got, ok)
	}
}

func TestOverlapsProvesExactPredicateDisjointness(t *testing.T) {
	base := config.RouteMatch{Source: "gitlab-a", Repository: "app", Event: "pipeline"}
	withStatus := base
	withStatus.Status = stringPointer("success")
	if !Overlaps(base, withStatus) {
		t.Fatal("Overlaps() = false, want wildcard optional predicate to overlap")
	}

	tests := []struct {
		name  string
		left  config.RouteMatch
		right config.RouteMatch
	}{
		{"source", base, config.RouteMatch{Source: "github-a", Repository: "app", Event: "pipeline"}},
		{"repository", base, config.RouteMatch{Source: "gitlab-a", Repository: "other", Event: "pipeline"}},
		{"event", base, config.RouteMatch{Source: "gitlab-a", Repository: "app", Event: "job"}},
		{"ref", withOptional(base, "ref", "main"), withOptional(base, "ref", "release")},
		{"status", withOptional(base, "status", "success"), withOptional(base, "status", "failed")},
		{"revision", withOptional(base, "revision", "a"), withOptional(base, "revision", "b")},
		{"external ID", withOptional(base, "external_id", "1"), withOptional(base, "external_id", "2")},
		{"trigger", withOptional(base, "trigger", "push"), withOptional(base, "trigger", "web")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if Overlaps(tt.left, tt.right) {
				t.Fatalf("Overlaps(%#v, %#v) = true, want false", tt.left, tt.right)
			}
		})
	}
}

func withOptional(match config.RouteMatch, field, value string) config.RouteMatch {
	switch field {
	case "ref":
		match.Ref = stringPointer(value)
	case "status":
		match.Status = stringPointer(value)
	case "revision":
		match.Revision = stringPointer(value)
	case "external_id":
		match.ExternalID = stringPointer(value)
	case "trigger":
		match.Trigger = stringPointer(value)
	}
	return match
}

func canonicalIntPointer(value int) *int { return &value }

func stringPointer(value string) *string {
	return &value
}
