package matcher

import (
	"github.com/hoomoli/hookfly/internal/config"
	"github.com/hoomoli/hookfly/internal/domain"
)

// FirstCanonical returns the first canonical route, in validated priority order,
// whose predicate matches event.
func FirstCanonical(routes []config.Route, event domain.CanonicalEvent) (*config.Route, bool) {
	for index := range routes {
		if routeMatches(routes[index].Match, event) {
			return &routes[index], true
		}
	}
	return nil, false
}

// Overlaps reports whether two exact route predicates can match one event.
func Overlaps(left, right config.RouteMatch) bool {
	return left.Source == right.Source &&
		left.Repository == right.Repository &&
		left.Event == right.Event &&
		scalarDomainsOverlap(left.Ref, right.Ref) &&
		scalarDomainsOverlap(left.Status, right.Status) &&
		scalarDomainsOverlap(left.Revision, right.Revision) &&
		scalarDomainsOverlap(left.ExternalID, right.ExternalID) &&
		scalarDomainsOverlap(left.Trigger, right.Trigger)
}

func routeMatches(match config.RouteMatch, event domain.CanonicalEvent) bool {
	return match.Source == event.Source &&
		match.Repository == event.Repository &&
		match.Event == event.Event &&
		matchesScalar(match.Ref, event.Ref) &&
		matchesScalar(match.Status, event.Status) &&
		matchesScalar(match.Revision, event.Revision) &&
		matchesScalar(match.ExternalID, event.ExternalID) &&
		matchesScalar(match.Trigger, event.Trigger)
}

func matchesScalar(predicate *string, value string) bool {
	return predicate == nil || *predicate == value
}

func scalarDomainsOverlap(left, right *string) bool {
	return left == nil || right == nil || *left == *right
}
