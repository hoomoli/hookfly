// Package runtimecfg compiles and publishes immutable runtime configuration generations.
package runtimecfg

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/hoomoli/hookfly/internal/config"
	"github.com/hoomoli/hookfly/internal/dokploy"
	"github.com/hoomoli/hookfly/internal/domain"
	"github.com/hoomoli/hookfly/internal/github"
	"github.com/hoomoli/hookfly/internal/gitlab"
	"github.com/hoomoli/hookfly/internal/provider"
	"github.com/hoomoli/hookfly/internal/store"
)

const bindingVersion = 2

// Target is the immutable executable projection of one configured deployment target.
type Target struct {
	ID             string
	ConnectionID   string
	ResourceType   string
	ComposeID      string
	Fingerprint    string
	Client         *dokploy.Client
	PollInterval   time.Duration
	PollTimeout    time.Duration
	Snapshot       []byte
	BindingVersion int
}

// Generation contains all projections that must change atomically after a reload.
type Generation struct {
	bundle           *config.Bundle
	digest           string
	connections      map[string]*connection
	repositories     []store.ConfiguredRepository
	repositoryLimits map[store.RepositoryKey]int
	targets          map[string]*Target
	sources          map[sourceKey]*Source
	gitLabSources    map[[sha256.Size]byte]*Source
	routes           []config.Route
}

type sourceKey struct {
	provider string
	id       string
}

// Source is the immutable provider-specific webhook boundary for one source.
type Source struct {
	provider     string
	id           string
	adapter      provider.Adapter
	repositories map[string]string
}

// Authenticate verifies the request using only this source's immutable adapter.
func (s *Source) Authenticate(headers http.Header, body []byte) bool {
	return s != nil && s.adapter != nil && s.adapter.Authenticate(headers, body)
}

// Normalize produces a source-stamped canonical event and its provider input.
func (s *Source) Normalize(headers http.Header, body []byte) (domain.CanonicalEvent, provider.IncomingEvent, error) {
	if s == nil || s.adapter == nil {
		return domain.CanonicalEvent{}, provider.IncomingEvent{}, fmt.Errorf("webhook source is unavailable")
	}
	incoming, err := s.adapter.Normalize(headers, body)
	if err != nil {
		return domain.CanonicalEvent{}, provider.IncomingEvent{}, err
	}
	return domain.CanonicalEvent{
		Provider: s.provider, Source: s.id, Repository: s.repositories[incoming.RepositoryExternalID],
		Event: incoming.Event, Ref: incoming.Ref, Status: incoming.Status, Revision: incoming.Revision, CommitMessage: incoming.CommitMessage,
		ExternalID: incoming.ExternalID, Trigger: incoming.Trigger,
	}, incoming, nil
}

// Load discovers, decodes, validates, and compiles one atomic configuration candidate.
func Load(path string, lookup config.EnvLookup, logger *slog.Logger) (*Generation, error) {
	candidate, err := config.Discover(path)
	if err != nil {
		return nil, err
	}
	bundle, err := config.Decode(candidate, lookup)
	if err != nil {
		return nil, err
	}
	if err := config.ValidateAndCanonicalize(bundle); err != nil {
		return nil, err
	}
	return compileBundle(bundle, logger)
}

// Compile creates one immutable generation from a decoded bundle.
func Compile(bundle *config.Bundle, logger *slog.Logger) (*Generation, error) {
	return compileBundle(bundle, logger)
}

func compileBundle(bundle *config.Bundle, logger *slog.Logger) (*Generation, error) {
	if bundle == nil {
		return nil, fmt.Errorf("configuration bundle is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	candidate := cloneBundle(bundle)
	if err := config.ValidateAndCanonicalize(candidate); err != nil {
		return nil, err
	}

	interval, timeout := pollingSettings(candidate.Global.Polling)
	configuredConnections := make(map[string]config.DokployConnection, len(candidate.DokployConnections))
	generation := &Generation{
		bundle: candidate, connections: make(map[string]*connection, len(candidate.DokployConnections)),
		targets:          make(map[string]*Target, len(candidate.Targets)),
		sources:          make(map[sourceKey]*Source, len(candidate.GitLabSources)+len(candidate.GitHubSources)),
		gitLabSources:    make(map[[sha256.Size]byte]*Source, len(candidate.GitLabSources)),
		repositoryLimits: make(map[store.RepositoryKey]int), routes: append([]config.Route(nil), candidate.Routes...),
	}
	for index := range candidate.DokployConnections {
		canonical, err := dokploy.CanonicalBaseURL(candidate.DokployConnections[index].BaseURL)
		if err != nil {
			return nil, fmt.Errorf("compile Dokploy connection: %w", err)
		}
		candidate.DokployConnections[index].BaseURL = canonical
		configuredConnections[candidate.DokployConnections[index].ID] = candidate.DokployConnections[index]
		client, err := dokploy.NewClient(canonical, candidate.DokployConnections[index].APIKey, http.DefaultClient, logger)
		if err != nil {
			return nil, fmt.Errorf("compile Dokploy connection: %w", err)
		}
		generation.connections[candidate.DokployConnections[index].ID] = &connection{
			id: candidate.DokployConnections[index].ID, kind: ConnectionTypeDokploy,
			client: client,
		}
	}
	for _, configured := range candidate.GitLabSources {
		source := generation.addSource("gitlab", configured.ID, gitlab.New(configured.Token), configured.Repositories)
		generation.gitLabSources[sha256.Sum256([]byte(configured.Token))] = source
	}
	for _, configured := range candidate.GitHubSources {
		generation.addSource("github", configured.ID, github.New(configured.Secret), configured.Repositories)
	}
	for _, configured := range candidate.Targets {
		connection := configuredConnections[configured.Connection]
		client, err := dokploy.NewClient(connection.BaseURL, connection.APIKey, http.DefaultClient, logger)
		if err != nil {
			return nil, fmt.Errorf("compile Dokploy target: %w", err)
		}
		targetTimeout := timeout
		if configured.PollTimeout != nil && configured.PollTimeout.Duration > 0 {
			targetTimeout = configured.PollTimeout.Duration
		}
		snapshot, err := json.Marshal(targetSnapshot{
			BindingVersion: bindingVersion, ID: configured.ID, Type: configured.Type,
			ResourceType: configured.ResourceType, ResourceID: configured.ResourceID,
			Connection: connectionSnapshot{ID: connection.ID, BaseURL: connection.BaseURL},
		})
		if err != nil {
			return nil, fmt.Errorf("encode target binding: %w", err)
		}
		generation.targets[configured.ID] = &Target{
			ID: configured.ID, ConnectionID: configured.Connection, ResourceType: configured.ResourceType, ComposeID: configured.ResourceID, Client: client,
			PollInterval: interval, PollTimeout: targetTimeout, Snapshot: snapshot,
			BindingVersion: bindingVersion,
			Fingerprint:    bindingFingerprint(bindingVersion, configured.Type, configured.ResourceType, connection.BaseURL, configured.ResourceID),
		}
	}
	generation.digest = bundleDigest(candidate)
	return generation, nil
}

func (g *Generation) addSource(providerName, sourceID string, adapter provider.Adapter, configured []config.Repository) *Source {
	source := &Source{provider: providerName, id: sourceID, adapter: adapter, repositories: make(map[string]string, len(configured))}
	for _, repository := range configured {
		source.repositories[repository.ExternalID] = repository.ID
		key := store.RepositoryKey{SourceID: sourceID, RepositoryID: repository.ID}
		generationRepository := store.ConfiguredRepository{Provider: providerName, SourceID: sourceID, ID: repository.ID, Name: repository.Name}
		g.repositories = append(g.repositories, generationRepository)
		if repository.HistoryLimit != nil {
			g.repositoryLimits[key] = *repository.HistoryLimit
		}
	}
	g.sources[sourceKey{provider: providerName, id: sourceID}] = source
	sort.Slice(g.repositories, func(left, right int) bool {
		if g.repositories[left].Provider != g.repositories[right].Provider {
			return g.repositories[left].Provider < g.repositories[right].Provider
		}
		if g.repositories[left].SourceID != g.repositories[right].SourceID {
			return g.repositories[left].SourceID < g.repositories[right].SourceID
		}
		return g.repositories[left].ID < g.repositories[right].ID
	})
	return source
}

func pollingSettings(polling config.Polling) (time.Duration, time.Duration) {
	interval := polling.Interval.Duration
	if interval <= 0 {
		interval = time.Second
	} else if interval < time.Millisecond {
		interval = time.Millisecond
	}
	timeout := polling.Timeout.Duration
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return interval, timeout
}

func cloneBundle(bundle *config.Bundle) *config.Bundle {
	copyBundle := *bundle
	copyBundle.GitLabSources = append([]config.GitLabSource(nil), bundle.GitLabSources...)
	copyBundle.GitHubSources = append([]config.GitHubSource(nil), bundle.GitHubSources...)
	copyBundle.DokployConnections = append([]config.DokployConnection(nil), bundle.DokployConnections...)
	copyBundle.Targets = append([]config.Target(nil), bundle.Targets...)
	copyBundle.Routes = append([]config.Route(nil), bundle.Routes...)
	for index := range copyBundle.GitLabSources {
		copyBundle.GitLabSources[index].Repositories = append([]config.Repository(nil), bundle.GitLabSources[index].Repositories...)
		cloneRepositoryLimits(copyBundle.GitLabSources[index].Repositories)
	}
	for index := range copyBundle.GitHubSources {
		copyBundle.GitHubSources[index].Repositories = append([]config.Repository(nil), bundle.GitHubSources[index].Repositories...)
		cloneRepositoryLimits(copyBundle.GitHubSources[index].Repositories)
	}
	for index := range copyBundle.Targets {
		copyBundle.Targets[index].PollTimeout = cloneDuration(bundle.Targets[index].PollTimeout)
	}
	for index := range copyBundle.Routes {
		copyBundle.Routes[index].Action.Targets = append([]string(nil), bundle.Routes[index].Action.Targets...)
		copyBundle.Routes[index].Priority = cloneInt(bundle.Routes[index].Priority)
		copyBundle.Routes[index].Match = cloneRouteMatch(bundle.Routes[index].Match)
	}
	return &copyBundle
}

func cloneRepositoryLimits(repositories []config.Repository) {
	for index := range repositories {
		repositories[index].HistoryLimit = cloneInt(repositories[index].HistoryLimit)
	}
}

func cloneDuration(value *config.Duration) *config.Duration {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneRouteMatch(match config.RouteMatch) config.RouteMatch {
	match.Ref = cloneString(match.Ref)
	match.Status = cloneString(match.Status)
	match.Revision = cloneString(match.Revision)
	match.ExternalID = cloneString(match.ExternalID)
	match.Trigger = cloneString(match.Trigger)
	return match
}

// Digest returns the secret-free identity of the compiled generation.
func (g *Generation) Digest() string { return g.digest }

// Source resolves one provider source without allowing provider-name collisions.
func (g *Generation) Source(providerName, sourceID string) (*Source, bool) {
	source, found := g.sources[sourceKey{provider: providerName, id: sourceID}]
	return source, found
}

// GitLabSource resolves and authenticates one GitLab source by its webhook token.
func (g *Generation) GitLabSource(headers http.Header) (*Source, bool) {
	if g == nil || headers.Get("X-Gitlab-Token") == "" {
		return nil, false
	}
	token := headers.Get("X-Gitlab-Token")
	source, found := g.gitLabSources[sha256.Sum256([]byte(token))]
	if !found || !source.Authenticate(headers, nil) {
		return nil, false
	}
	return source, true
}

// Repositories returns the canonical configured repository projection.
func (g *Generation) Repositories() []store.ConfiguredRepository {
	return append([]store.ConfiguredRepository(nil), g.repositories...)
}

// Target resolves one immutable executable target.
func (g *Generation) Target(id string) (*Target, bool) {
	target, found := g.targets[id]
	return cloneTarget(target), found
}

// Targets returns defensive copies of every executable target in stable ID order.
func (g *Generation) Targets() []*Target {
	targets := make([]*Target, 0, len(g.targets))
	for _, target := range g.targets {
		targets = append(targets, cloneTarget(target))
	}
	sort.Slice(targets, func(left, right int) bool { return targets[left].ID < targets[right].ID })
	return targets
}

func cloneTarget(target *Target) *Target {
	if target == nil {
		return nil
	}
	copyTarget := *target
	copyTarget.Snapshot = append([]byte(nil), target.Snapshot...)
	return &copyTarget
}

// PollInterval returns the generation-wide bounded polling interval.
func (g *Generation) PollInterval() time.Duration {
	interval, _ := pollingSettings(g.bundle.Global.Polling)
	return interval
}

// PollTimeout returns the generation-wide default monitoring timeout.
func (g *Generation) PollTimeout() time.Duration {
	_, timeout := pollingSettings(g.bundle.Global.Polling)
	return timeout
}

// RetentionLimits returns copies of the current history limit projections.
func (g *Generation) RetentionLimits() (map[store.RepositoryKey]int, int, int) {
	limits := make(map[store.RepositoryKey]int, len(g.repositoryLimits))
	for key, limit := range g.repositoryLimits {
		limits[key] = limit
	}
	return limits, g.bundle.Global.History.DefaultRepositoryLimit, g.bundle.Global.History.GlobalLimit
}

// Route applies the generation's ordered canonical rules and creates secret-free snapshots.
func (g *Generation) Route(event domain.CanonicalEvent) (domain.RouteDecision, error) {
	var rule *config.Route
	for index := range g.routes {
		if routeMatches(g.routes[index].Match, event) {
			rule = &g.routes[index]
			break
		}
	}
	if rule == nil {
		return domain.RouteDecision{RoutingResult: domain.RoutingUnmatched}, nil
	}
	ruleSnapshot, err := json.Marshal(newCanonicalRuleSnapshot(*rule))
	if err != nil {
		return domain.RouteDecision{}, fmt.Errorf("encode rule snapshot: %w", err)
	}
	switch rule.Action.Type {
	case "record_only":
		if len(rule.Action.Targets) != 0 {
			return domain.RouteDecision{}, fmt.Errorf("record-only route contains targets")
		}
		return domain.RouteDecision{RoutingResult: domain.RoutingRecordOnly, RuleID: rule.ID, RuleSnapshot: ruleSnapshot}, nil
	case "deploy":
		if len(rule.Action.Targets) == 0 {
			return domain.RouteDecision{}, fmt.Errorf("deploy route has no targets")
		}
		deliveries := make([]domain.NewDelivery, 0, len(rule.Action.Targets))
		for _, targetID := range rule.Action.Targets {
			target, found := g.targets[targetID]
			if !found {
				return domain.RouteDecision{}, fmt.Errorf("route target is unavailable")
			}
			deliveries = append(deliveries, domain.NewDelivery{
				TargetID: target.ID, TargetSnapshot: append([]byte(nil), target.Snapshot...), DispatchKey: target.Fingerprint,
			})
		}
		return domain.RouteDecision{
			RoutingResult: domain.RoutingDeploy, RuleID: rule.ID,
			RuleSnapshot: ruleSnapshot, Deliveries: deliveries,
		}, nil
	default:
		return domain.RouteDecision{}, fmt.Errorf("route action is unavailable")
	}
}

func routeMatches(match config.RouteMatch, event domain.CanonicalEvent) bool {
	return match.Source == event.Source &&
		match.Repository == event.Repository &&
		match.Event == event.Event &&
		matchesOptional(match.Ref, event.Ref) &&
		matchesOptional(match.Status, event.Status) &&
		matchesOptional(match.Revision, event.Revision) &&
		matchesOptional(match.ExternalID, event.ExternalID) &&
		matchesOptional(match.Trigger, event.Trigger)
}

func matchesOptional(expected *string, actual string) bool {
	return expected == nil || *expected == actual
}

type targetSnapshot struct {
	BindingVersion int                `json:"binding_version"`
	ID             string             `json:"id"`
	Type           string             `json:"type"`
	ResourceType   string             `json:"resource_type"`
	ResourceID     string             `json:"resource_id"`
	Connection     connectionSnapshot `json:"connection"`
}

type connectionSnapshot struct {
	ID      string `json:"id"`
	BaseURL string `json:"base_url"`
}

type ruleActionSnapshot struct {
	Type    string   `json:"type"`
	Targets []string `json:"targets,omitempty"`
}

type canonicalRuleSnapshot struct {
	ID       string                     `json:"id"`
	Priority int                        `json:"priority"`
	Match    canonicalRuleMatchSnapshot `json:"match"`
	Action   ruleActionSnapshot         `json:"action"`
}

type canonicalRuleMatchSnapshot struct {
	Source     string  `json:"source"`
	Repository string  `json:"repository"`
	Event      string  `json:"event"`
	Ref        *string `json:"ref,omitempty"`
	Status     *string `json:"status,omitempty"`
	Revision   *string `json:"revision,omitempty"`
	ExternalID *string `json:"external_id,omitempty"`
	Trigger    *string `json:"trigger,omitempty"`
}

func newCanonicalRuleSnapshot(route config.Route) canonicalRuleSnapshot {
	return canonicalRuleSnapshot{
		ID: route.ID, Priority: *route.Priority,
		Match: canonicalRuleMatchSnapshot{
			Source: route.Match.Source, Repository: route.Match.Repository, Event: route.Match.Event,
			Ref: cloneString(route.Match.Ref), Status: cloneString(route.Match.Status),
			Revision: cloneString(route.Match.Revision), ExternalID: cloneString(route.Match.ExternalID),
			Trigger: cloneString(route.Match.Trigger),
		},
		Action: ruleActionSnapshot{Type: route.Action.Type, Targets: append([]string(nil), route.Action.Targets...)},
	}
}

func bindingFingerprint(version int, targetType, resourceType, baseURL, resourceID string) string {
	hash := sha256.New()
	for _, field := range []string{fmt.Sprintf("%d", version), targetType, resourceType, baseURL, resourceID} {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(field))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func bundleDigest(bundle *config.Bundle) string {
	type digestRepository struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		ExternalID   string `json:"external_id"`
		HistoryLimit *int   `json:"history_limit,omitempty"`
	}
	type digestSource struct {
		Provider     string             `json:"provider"`
		ID           string             `json:"id"`
		Repositories []digestRepository `json:"repositories"`
	}
	type digestConnection struct {
		ID      string `json:"id"`
		BaseURL string `json:"base_url"`
	}
	type digestTarget struct {
		ID           string `json:"id"`
		Type         string `json:"type"`
		Connection   string `json:"connection"`
		ResourceType string `json:"resource_type"`
		ResourceID   string `json:"resource_id"`
		PollTimeout  string `json:"poll_timeout,omitempty"`
	}
	type digestRoute struct {
		ID       string            `json:"id"`
		Priority int               `json:"priority"`
		Match    config.RouteMatch `json:"match"`
		Action   config.Action     `json:"action"`
	}
	safe := struct {
		Kind                   string             `json:"kind"`
		ConfigDir              string             `json:"config_dir"`
		GlobalLimit            int                `json:"global_limit"`
		DefaultRepositoryLimit int                `json:"default_repository_limit"`
		PollInterval           string             `json:"poll_interval"`
		PollTimeout            string             `json:"poll_timeout"`
		Sources                []digestSource     `json:"sources"`
		Connections            []digestConnection `json:"connections"`
		Targets                []digestTarget     `json:"targets"`
		Routes                 []digestRoute      `json:"routes"`
	}{
		Kind: bundle.Global.Kind, ConfigDir: bundle.Global.ConfigDir,
		GlobalLimit: bundle.Global.History.GlobalLimit, DefaultRepositoryLimit: bundle.Global.History.DefaultRepositoryLimit,
		PollInterval: bundle.Global.Polling.Interval.Duration.String(), PollTimeout: bundle.Global.Polling.Timeout.Duration.String(),
	}
	appendSource := func(providerName, sourceID string, repositories []config.Repository) {
		source := digestSource{Provider: providerName, ID: sourceID, Repositories: make([]digestRepository, 0, len(repositories))}
		for _, repository := range repositories {
			source.Repositories = append(source.Repositories, digestRepository{ID: repository.ID, Name: repository.Name, ExternalID: repository.ExternalID, HistoryLimit: repository.HistoryLimit})
		}
		safe.Sources = append(safe.Sources, source)
	}
	for _, source := range bundle.GitLabSources {
		appendSource("gitlab", source.ID, source.Repositories)
	}
	for _, source := range bundle.GitHubSources {
		appendSource("github", source.ID, source.Repositories)
	}
	sort.Slice(safe.Sources, func(left, right int) bool {
		if safe.Sources[left].Provider != safe.Sources[right].Provider {
			return safe.Sources[left].Provider < safe.Sources[right].Provider
		}
		return safe.Sources[left].ID < safe.Sources[right].ID
	})
	for _, connection := range bundle.DokployConnections {
		safe.Connections = append(safe.Connections, digestConnection{ID: connection.ID, BaseURL: connection.BaseURL})
	}
	for _, target := range bundle.Targets {
		pollTimeout := ""
		if target.PollTimeout != nil {
			pollTimeout = target.PollTimeout.Duration.String()
		}
		safe.Targets = append(safe.Targets, digestTarget{ID: target.ID, Type: target.Type, Connection: target.Connection, ResourceType: target.ResourceType, ResourceID: target.ResourceID, PollTimeout: pollTimeout})
	}
	for _, route := range bundle.Routes {
		safe.Routes = append(safe.Routes, digestRoute{ID: route.ID, Priority: *route.Priority, Match: route.Match, Action: route.Action})
	}
	data, _ := json.Marshal(safe)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
