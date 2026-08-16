package config

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ValidateAndCanonicalize validates cross-document bundle references and
// normalizes their representation for deterministic routing.
func ValidateAndCanonicalize(bundle *Bundle) error {
	if bundle == nil {
		return fmt.Errorf("configuration bundle is required")
	}

	repositories, err := validateSources(bundle)
	if err != nil {
		return err
	}
	connections, err := validateConnections(bundle.DokployConnections)
	if err != nil {
		return err
	}
	targets, err := validateTargets(bundle.Targets, connections)
	if err != nil {
		return err
	}
	if err := validateRoutes(bundle.Routes, repositories, targets); err != nil {
		return err
	}

	canonicalize(bundle)
	return nil
}

func validateSources(bundle *Bundle) (map[string]map[string]struct{}, error) {
	locations := make(map[string]SourceLocation, len(bundle.GitLabSources)+len(bundle.GitHubSources))
	repositories := make(map[string]map[string]struct{}, len(bundle.GitLabSources)+len(bundle.GitHubSources))
	gitLabTokens := make(map[[sha256.Size]byte]SourceLocation, len(bundle.GitLabSources))
	for index := range bundle.GitLabSources {
		source := &bundle.GitLabSources[index]
		if source.Token == "" {
			return nil, fmt.Errorf("GitLab source %q requires token", source.ID)
		}
		tokenHash := sha256.Sum256([]byte(source.Token))
		if previous, exists := gitLabTokens[tokenHash]; exists {
			return nil, fmt.Errorf("duplicate GitLab source token at %s and %s", previous.String(), source.Location.String())
		}
		gitLabTokens[tokenHash] = source.Location
		if err := validateSource(source.ID, source.Location, source.Repositories, locations, repositories); err != nil {
			return nil, err
		}
	}
	for index := range bundle.GitHubSources {
		source := &bundle.GitHubSources[index]
		if source.Secret == "" {
			return nil, fmt.Errorf("GitHub source %q requires secret", source.ID)
		}
		if err := validateSource(source.ID, source.Location, source.Repositories, locations, repositories); err != nil {
			return nil, err
		}
	}
	return repositories, nil
}

func validateSource(id string, location SourceLocation, configured []Repository, locations map[string]SourceLocation, repositories map[string]map[string]struct{}) error {
	if err := validateIdentifier("source", id); err != nil {
		return err
	}
	if err := addUniqueLocation(locations, "source", id, location); err != nil {
		return err
	}

	known := make(map[string]SourceLocation, len(configured))
	externalIDs := make(map[string]SourceLocation, len(configured))
	for index := range configured {
		repository := configured[index]
		if err := validateIdentifier("repository", repository.ID); err != nil {
			return err
		}
		if err := validatePositiveExternalID(repository.ExternalID); err != nil {
			return fmt.Errorf("repository %q %w", repository.ID, err)
		}
		if err := addUniqueLocation(externalIDs, "repository external", repository.ExternalID, repository.Location); err != nil {
			return fmt.Errorf("source %q: %w", id, err)
		}
		if err := addUniqueLocation(known, "repository", repository.ID, repository.Location); err != nil {
			return fmt.Errorf("source %q: %w", id, err)
		}
	}
	repositoryIDs := make(map[string]struct{}, len(known))
	for id := range known {
		repositoryIDs[id] = struct{}{}
	}
	repositories[id] = repositoryIDs
	return nil
}

func validateConnections(connections []DokployConnection) (map[string]struct{}, error) {
	locations := make(map[string]SourceLocation, len(connections))
	known := make(map[string]struct{}, len(connections))
	for index := range connections {
		connection := connections[index]
		if err := validateIdentifier("connection", connection.ID); err != nil {
			return nil, err
		}
		if err := addUniqueLocation(locations, "connection", connection.ID, connection.Location); err != nil {
			return nil, err
		}
		if connection.APIKey == "" {
			return nil, fmt.Errorf("Dokploy connection %q requires api_key", connection.ID)
		}
		if err := validateDokployURL(connection.BaseURL); err != nil {
			return nil, fmt.Errorf("Dokploy connection %q: %w", connection.ID, err)
		}
		known[connection.ID] = struct{}{}
	}
	return known, nil
}

func validateTargets(configured []Target, connections map[string]struct{}) (map[string]struct{}, error) {
	locations := make(map[string]SourceLocation, len(configured))
	known := make(map[string]struct{}, len(configured))
	for index := range configured {
		target := configured[index]
		if err := validateIdentifier("target", target.ID); err != nil {
			return nil, err
		}
		if err := addUniqueLocation(locations, "target", target.ID, target.Location); err != nil {
			return nil, err
		}
		if target.Type != "dokploy" {
			return nil, fmt.Errorf("unsupported target type %q", target.Type)
		}
		if _, exists := connections[target.Connection]; !exists {
			return nil, fmt.Errorf("target %q references unknown connection %q", target.ID, target.Connection)
		}
		if target.ResourceType != "compose" {
			return nil, fmt.Errorf("unsupported resource type %q", target.ResourceType)
		}
		if err := validateIdentifier("target resource", target.ResourceID); err != nil {
			return nil, err
		}
		known[target.ID] = struct{}{}
	}
	return known, nil
}

func validateRoutes(routes []Route, repositories map[string]map[string]struct{}, targets map[string]struct{}) error {
	locations := make(map[string]SourceLocation, len(routes))
	for index := range routes {
		route := routes[index]
		if err := validateIdentifier("route", route.ID); err != nil {
			return err
		}
		if err := addUniqueLocation(locations, "route", route.ID, route.Location); err != nil {
			return err
		}
		if route.Priority == nil {
			return fmt.Errorf("route %q priority is required", route.ID)
		}
		if err := validateRouteMatch(route.ID, route.Match, repositories); err != nil {
			return err
		}
		if err := validateAction(route, targets); err != nil {
			return err
		}
	}
	for left := range routes {
		for right := left + 1; right < len(routes); right++ {
			if *routes[left].Priority == *routes[right].Priority && routeMatchesOverlap(routes[left].Match, routes[right].Match) {
				return fmt.Errorf("equal-priority routes %q and %q overlap", routes[left].ID, routes[right].ID)
			}
		}
	}
	return nil
}

func validateRouteMatch(routeID string, match RouteMatch, repositories map[string]map[string]struct{}) error {
	if err := validateIdentifier("route source", match.Source); err != nil {
		return err
	}
	knownRepositories, exists := repositories[match.Source]
	if !exists {
		return fmt.Errorf("route %q references unknown source %q", routeID, match.Source)
	}
	if err := validateIdentifier("route repository", match.Repository); err != nil {
		return err
	}
	if _, exists := knownRepositories[match.Repository]; !exists {
		return fmt.Errorf("route %q references unknown repository %q", routeID, match.Repository)
	}
	if !canonicalEvents[match.Event] {
		return fmt.Errorf("route %q has unsupported event %q", routeID, match.Event)
	}
	return nil
}

func validateAction(route Route, targets map[string]struct{}) error {
	switch route.Action.Type {
	case "record_only":
		if len(route.Action.Targets) != 0 {
			return fmt.Errorf("record_only route %q cannot reference targets", route.ID)
		}
	case "deploy":
		if len(route.Action.Targets) == 0 {
			return fmt.Errorf("deploy route %q requires at least one target", route.ID)
		}
		seen := make(map[string]struct{}, len(route.Action.Targets))
		for _, targetID := range route.Action.Targets {
			if _, exists := seen[targetID]; exists {
				return fmt.Errorf("deploy route %q references duplicate target %q", route.ID, targetID)
			}
			seen[targetID] = struct{}{}
			if _, exists := targets[targetID]; !exists {
				return fmt.Errorf("deploy route %q references unknown target %q", route.ID, targetID)
			}
		}
	default:
		return fmt.Errorf("route %q has unsupported action %q", route.ID, route.Action.Type)
	}
	return nil
}

func validateIdentifier(kind, id string) error {
	if !identifierPattern.MatchString(id) {
		return fmt.Errorf("invalid %s ID %q", kind, id)
	}
	return nil
}

func validatePositiveExternalID(id string) error {
	value, err := strconv.ParseInt(id, 10, 64)
	if err != nil || value <= 0 {
		return fmt.Errorf("external ID must be positive")
	}
	return nil
}

func validateDokployURL(rawURL string) error {
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("invalid Dokploy URL")
	}
	return nil
}

func addUniqueLocation(locations map[string]SourceLocation, kind, id string, location SourceLocation) error {
	if previous, exists := locations[id]; exists {
		return fmt.Errorf("duplicate %s ID %q at %s and %s", kind, id, previous.String(), location.String())
	}
	locations[id] = location
	return nil
}

func routeMatchesOverlap(left, right RouteMatch) bool {
	return left.Source == right.Source &&
		left.Repository == right.Repository &&
		left.Event == right.Event &&
		scalarDomainsOverlap(left.Ref, right.Ref) &&
		scalarDomainsOverlap(left.Status, right.Status) &&
		scalarDomainsOverlap(left.Revision, right.Revision) &&
		scalarDomainsOverlap(left.ExternalID, right.ExternalID) &&
		scalarDomainsOverlap(left.Trigger, right.Trigger)
}

func scalarDomainsOverlap(left, right *string) bool {
	return left == nil || right == nil || *left == *right
}

func canonicalize(bundle *Bundle) {
	sort.Slice(bundle.GitLabSources, func(left, right int) bool { return bundle.GitLabSources[left].ID < bundle.GitLabSources[right].ID })
	sort.Slice(bundle.GitHubSources, func(left, right int) bool { return bundle.GitHubSources[left].ID < bundle.GitHubSources[right].ID })
	for index := range bundle.GitLabSources {
		sort.Slice(bundle.GitLabSources[index].Repositories, func(left, right int) bool {
			return bundle.GitLabSources[index].Repositories[left].ID < bundle.GitLabSources[index].Repositories[right].ID
		})
	}
	for index := range bundle.GitHubSources {
		sort.Slice(bundle.GitHubSources[index].Repositories, func(left, right int) bool {
			return bundle.GitHubSources[index].Repositories[left].ID < bundle.GitHubSources[index].Repositories[right].ID
		})
	}
	sort.Slice(bundle.DokployConnections, func(left, right int) bool {
		return bundle.DokployConnections[left].ID < bundle.DokployConnections[right].ID
	})
	sort.Slice(bundle.Targets, func(left, right int) bool { return bundle.Targets[left].ID < bundle.Targets[right].ID })
	sort.Slice(bundle.Routes, func(left, right int) bool {
		if *bundle.Routes[left].Priority != *bundle.Routes[right].Priority {
			return *bundle.Routes[left].Priority < *bundle.Routes[right].Priority
		}
		return bundle.Routes[left].ID < bundle.Routes[right].ID
	})
}

var canonicalEvents = map[string]bool{
	"pipeline":      true,
	"job":           true,
	"push":          true,
	"tag_push":      true,
	"merge_request": true,
}
