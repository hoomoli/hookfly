package runtimecfg

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/config"
	"github.com/hoomoli/hookfly/internal/domain"
	"github.com/hoomoli/hookfly/internal/store"
)

func TestCompileCreatesIsolatedCanonicalSourcesAndProjections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Compile() made a network call")
	}))
	defer server.Close()

	bundle := generationBundle(server.URL)
	generation, err := Compile(bundle, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{generation.routes[0].ID, generation.routes[1].ID}; !reflect.DeepEqual(got, []string{"record-github", "deploy-gitlab"}) {
		t.Fatalf("route order = %#v", got)
	}
	githubSource, found := generation.Source("github", "github-primary")
	if !found {
		t.Fatal("GitHub source is missing")
	}
	if _, found := generation.Source("gitlab", "github-primary"); found {
		t.Fatal("source lookup crossed provider boundary")
	}
	payload := []byte(`{"repository":{"id":101},"ref":"refs/heads/main","after":"abc123"}`)
	headers := githubHeaders("github-secret", payload)
	if !githubSource.Authenticate(headers, payload) {
		t.Fatal("Authenticate() = false")
	}
	event, incoming, err := githubSource.Normalize(headers, payload)
	if err != nil || event.Provider != "github" || event.Source != "github-primary" || event.Repository != "application" || !incoming.Supported {
		t.Fatalf("Normalize() = %#v/%#v/%v", event, incoming, err)
	}
	if _, found := generation.Target("production"); !found {
		t.Fatal("target is missing")
	}
	if got, want := generation.Repositories(), []store.ConfiguredRepository{
		{Provider: "github", SourceID: "github-primary", ID: "application", Name: "Application"},
		{Provider: "gitlab", SourceID: "gitlab-primary", ID: "application", Name: "Application"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Repositories() = %#v", got)
	}
	limits, defaultLimit, globalLimit := generation.RetentionLimits()
	if got := limits[store.RepositoryKey{SourceID: "github-primary", RepositoryID: "application"}]; got != 8 || defaultLimit != 10 || globalLimit != 100 {
		t.Fatalf("RetentionLimits() = %#v, %d, %d", limits, defaultLimit, globalLimit)
	}
}

func TestConnectionSummariesAreStableSafeAndDefensive(t *testing.T) {
	// Break caught: exposing configured origins/credentials, returning map order, or letting a caller mutate generation capabilities.
	bundle := generationBundle("https://zulu.example.invalid")
	bundle.DokployConnections = append(bundle.DokployConnections, config.DokployConnection{
		ID: "alpha", BaseURL: "https://alpha.example.invalid", APIKey: "alpha-secret-key",
	})
	generation, err := Compile(bundle, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	want := []ConnectionSummary{
		{ID: "alpha", Type: "dokploy", Status: "configured", Capabilities: []string{"resource_discovery"}},
		{ID: "primary", Type: "dokploy", Status: "configured", Capabilities: []string{"resource_discovery"}},
	}
	first := generation.Connections()
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("Connections() = %#v, want %#v", first, want)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"alpha.example.invalid", "zulu.example.invalid", "alpha-secret-key", "dokploy-key", "base_url", "api_key"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("Connections() exposed %q: %s", forbidden, encoded)
		}
	}
	first[0].Capabilities[0] = "modified"
	if got := generation.Connections(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Connections() exposed generation state: %#v", got)
	}
}

func TestGenerationSelectsGitLabSourceByToken(t *testing.T) {
	bundle := generationBundle("https://dokploy.example.invalid")
	bundle.GitLabSources = append(bundle.GitLabSources, config.GitLabSource{
		ID: "gitlab-secondary", Token: "secondary-token",
		Repositories: []config.Repository{{ID: "operations", Name: "Operations", ExternalID: "7"}},
	})
	generation, err := Compile(bundle, discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name, token, wantSource string
		wantFound               bool
	}{
		{name: "primary", token: "gitlab-token", wantSource: "gitlab-primary", wantFound: true},
		{name: "secondary with same project ID", token: "secondary-token", wantSource: "gitlab-secondary", wantFound: true},
		{name: "missing"},
		{name: "unknown", token: "unknown-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := make(http.Header)
			if test.token != "" {
				headers.Set("X-Gitlab-Token", test.token)
			}
			source, found := generation.GitLabSource(headers)
			if found != test.wantFound {
				t.Fatalf("GitLabSource() found = %v, want %v", found, test.wantFound)
			}
			if found && source.id != test.wantSource {
				t.Fatalf("GitLabSource() source = %q, want %q", source.id, test.wantSource)
			}
		})
	}
}

func TestSourceNormalizeLeavesUnconfiguredRepositoryEmpty(t *testing.T) {
	generation, err := Compile(generationBundle("https://dokploy.example.invalid"), discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	source, found := generation.Source("github", "github-primary")
	if !found {
		t.Fatal("GitHub source is missing")
	}
	payload := []byte(`{"repository":{"id":999},"ref":"refs/heads/main","after":"abc123"}`)
	headers := githubHeaders("github-secret", payload)
	if !source.Authenticate(headers, payload) {
		t.Fatal("Authenticate() = false")
	}
	event, incoming, err := source.Normalize(headers, payload)
	if err != nil || !incoming.Supported || event.Repository != "" {
		t.Fatalf("Normalize() = %#v/%#v/%v", event, incoming, err)
	}
}

func TestCompileDigestExcludesSecretsAndUsesCanonicalBundle(t *testing.T) {
	first := generationBundle("https://DOKPLOY.EXAMPLE.INVALID:443/")
	second := generationBundle("https://dokploy.example.invalid")
	addDigestPermutationEntries(first)
	addDigestPermutationEntries(second)
	second.GitLabSources[0].Token = "other-gitlab-token"
	second.GitHubSources[0].Secret = "other-github-secret"
	second.DokployConnections[0].APIKey = "other-api-key"
	second.GitLabSources[0], second.GitLabSources[1] = second.GitLabSources[1], second.GitLabSources[0]
	second.GitHubSources[0], second.GitHubSources[1] = second.GitHubSources[1], second.GitHubSources[0]
	second.GitLabSources[1].Repositories[0], second.GitLabSources[1].Repositories[1] = second.GitLabSources[1].Repositories[1], second.GitLabSources[1].Repositories[0]
	second.GitHubSources[1].Repositories[0], second.GitHubSources[1].Repositories[1] = second.GitHubSources[1].Repositories[1], second.GitHubSources[1].Repositories[0]
	second.DokployConnections[0], second.DokployConnections[1] = second.DokployConnections[1], second.DokployConnections[0]
	second.Targets[0], second.Targets[1] = second.Targets[1], second.Targets[0]
	second.Routes[0], second.Routes[1] = second.Routes[1], second.Routes[0]
	second.Routes[1], second.Routes[2] = second.Routes[2], second.Routes[1]

	firstGeneration, err := Compile(first, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	secondGeneration, err := Compile(second, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if firstGeneration.Digest() != secondGeneration.Digest() {
		t.Fatalf("digest differs for reordered, secret-only change: %s != %s", firstGeneration.Digest(), secondGeneration.Digest())
	}
	second.Targets[0].ResourceID = "compose-changed"
	changed, err := Compile(second, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if changed.Digest() == firstGeneration.Digest() {
		t.Fatal("nonsecret target change did not alter digest")
	}
}

func addDigestPermutationEntries(bundle *config.Bundle) {
	secondaryPriority := 3
	bundle.GitLabSources[0].Repositories = append(bundle.GitLabSources[0].Repositories,
		config.Repository{ID: "library", Name: "Library", ExternalID: "8"})
	bundle.GitLabSources = append(bundle.GitLabSources, config.GitLabSource{
		ID: "gitlab-secondary", Token: "secondary-gitlab-secret",
		Repositories: []config.Repository{{ID: "service", Name: "Service", ExternalID: "9"}},
	})
	bundle.GitHubSources[0].Repositories = append(bundle.GitHubSources[0].Repositories,
		config.Repository{ID: "library", Name: "Library", ExternalID: "102"})
	bundle.GitHubSources = append(bundle.GitHubSources, config.GitHubSource{
		ID: "github-secondary", Secret: "secondary-github-secret",
		Repositories: []config.Repository{{ID: "service", Name: "Service", ExternalID: "103"}},
	})
	bundle.DokployConnections = append(bundle.DokployConnections, config.DokployConnection{
		ID: "secondary", BaseURL: "https://secondary.example.invalid", APIKey: "secondary-api-key",
	})
	bundle.Targets = append(bundle.Targets, config.Target{
		ID: "staging", Type: "dokploy", Connection: "secondary", ResourceType: "compose", ResourceID: "compose-staging",
	})
	bundle.Routes = append(bundle.Routes, config.Route{
		ID: "secondary-route", Priority: &secondaryPriority,
		Match:  config.RouteMatch{Source: "gitlab-secondary", Repository: "service", Event: "pipeline"},
		Action: config.Action{Type: "deploy", Targets: []string{"staging"}},
	})
}

func TestCompileOwnsDeepCopyOfBundleAndTargetViews(t *testing.T) {
	bundle := generationBundle("https://dokploy.example.invalid")
	ref := "main"
	pollTimeout := config.Duration{Duration: 5 * time.Second}
	bundle.Routes[0].Match.Ref = &ref
	bundle.Targets[0].PollTimeout = &pollTimeout
	generation, err := Compile(bundle, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	digest := generation.Digest()
	*bundle.Routes[0].Priority = 99
	*bundle.Routes[0].Match.Ref = "other"
	*bundle.GitHubSources[0].Repositories[0].HistoryLimit = 99
	bundle.Targets[0].PollTimeout.Duration = time.Hour

	if generation.Digest() != digest || *generation.routes[1].Priority != 2 || *generation.routes[1].Match.Ref != "main" {
		t.Fatalf("generation changed after input mutation: %#v", generation.routes[1])
	}
	limits, _, _ := generation.RetentionLimits()
	if got := limits[store.RepositoryKey{SourceID: "github-primary", RepositoryID: "application"}]; got != 8 {
		t.Fatalf("history limit = %d", got)
	}
	if got := generation.bundle.Targets[0].PollTimeout.Duration; got != 5*time.Second {
		t.Fatalf("poll timeout = %v", got)
	}

	first, found := generation.Target("production")
	if !found {
		t.Fatal("Target() did not find production")
	}
	originalSnapshot := append([]byte(nil), first.Snapshot...)
	first.ComposeID = "modified"
	first.Snapshot[0] ^= 1
	second, found := generation.Target("production")
	if !found || second.ComposeID != "compose-production" || !reflect.DeepEqual(second.Snapshot, originalSnapshot) {
		t.Fatalf("Target() exposed generation state: %#v", second)
	}

	resolved, status := generation.ResolveTarget("production", originalSnapshot)
	if status != BindingCompatible || resolved == nil {
		t.Fatalf("ResolveTarget() = %#v, %q", resolved, status)
	}
	resolved.ComposeID = "modified"
	resolved.Snapshot[0] ^= 1
	resolvedAgain, status := generation.ResolveTarget("production", originalSnapshot)
	if status != BindingCompatible || resolvedAgain.ComposeID != "compose-production" || !reflect.DeepEqual(resolvedAgain.Snapshot, originalSnapshot) {
		t.Fatalf("ResolveTarget() exposed generation state: %#v, %q", resolvedAgain, status)
	}

	targets := generation.Targets()
	if len(targets) != 1 || targets[0].ID != "production" {
		t.Fatalf("Targets() = %#v", targets)
	}
	targets[0].ComposeID = "modified"
	targets[0].Snapshot[0] ^= 1
	targetsAgain := generation.Targets()
	if targetsAgain[0].ComposeID != "compose-production" || !reflect.DeepEqual(targetsAgain[0].Snapshot, originalSnapshot) {
		t.Fatalf("Targets() exposed generation state: %#v", targetsAgain)
	}
}

func TestRouteMatchesCanonicalFieldsAndReturnsDefensiveSecretFreeSnapshots(t *testing.T) {
	bundle := generationBundle("https://dokploy.example.invalid")
	ref, status, revision, externalID, trigger := "main", "success", "abc123", "91", "push"
	bundle.Routes[1].Match.Ref = &ref
	bundle.Routes[1].Match.Status = &status
	bundle.Routes[1].Match.Revision = &revision
	bundle.Routes[1].Match.ExternalID = &externalID
	bundle.Routes[1].Match.Trigger = &trigger
	bundle.Routes[1].Action = config.Action{Type: "deploy", Targets: []string{"production"}}
	generation, err := Compile(bundle, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	event := domain.CanonicalEvent{
		Provider: "github", Source: "github-primary", Repository: "application", Event: "push",
		Ref: ref, Status: status, Revision: revision, ExternalID: externalID, Trigger: trigger,
	}
	decision, err := generation.Route(event)
	if err != nil || decision.RoutingResult != domain.RoutingDeploy || decision.RuleID != "record-github" || len(decision.Deliveries) != 1 {
		t.Fatalf("Route() = %#v, %v", decision, err)
	}
	if decision.Deliveries[0].DispatchKey == "" || decision.Deliveries[0].DispatchKey != generation.targets["production"].Fingerprint {
		t.Fatalf("delivery dispatch key = %q", decision.Deliveries[0].DispatchKey)
	}
	for _, secret := range []string{"github-secret", "dokploy-key"} {
		if strings.Contains(string(decision.RuleSnapshot), secret) || strings.Contains(string(decision.Deliveries[0].TargetSnapshot), secret) {
			t.Fatalf("route snapshots contain secret %q", secret)
		}
	}
	decision.RuleSnapshot[0] ^= 1
	decision.Deliveries[0].TargetSnapshot[0] ^= 1
	again, err := generation.Route(event)
	if err != nil || !json.Valid(again.RuleSnapshot) || !json.Valid(again.Deliveries[0].TargetSnapshot) {
		t.Fatalf("Route() exposed generation snapshots: %#v, %v", again, err)
	}
	event.Status = "failed"
	unmatched, err := generation.Route(event)
	if err != nil || unmatched.RoutingResult != domain.RoutingUnmatched {
		t.Fatalf("exact Route() = %#v, %v", unmatched, err)
	}
}

func generationBundle(baseURL string) *config.Bundle {
	priorityOne, priorityTwo := 1, 2
	githubLimit := 8
	return &config.Bundle{
		Global:             config.Global{Kind: "Hookfly", History: config.GlobalHistory{GlobalLimit: 100, DefaultRepositoryLimit: 10}},
		GitLabSources:      []config.GitLabSource{{ID: "gitlab-primary", Token: "gitlab-token", Repositories: []config.Repository{{ID: "application", Name: "Application", ExternalID: "7"}}}},
		GitHubSources:      []config.GitHubSource{{ID: "github-primary", Secret: "github-secret", Repositories: []config.Repository{{ID: "application", Name: "Application", ExternalID: "101", HistoryLimit: &githubLimit}}}},
		DokployConnections: []config.DokployConnection{{ID: "primary", BaseURL: baseURL, APIKey: "dokploy-key"}},
		Targets:            []config.Target{{ID: "production", Type: "dokploy", Connection: "primary", ResourceType: "compose", ResourceID: "compose-production"}},
		Routes: []config.Route{
			{ID: "deploy-gitlab", Priority: &priorityTwo, Match: config.RouteMatch{Source: "gitlab-primary", Repository: "application", Event: "push"}, Action: config.Action{Type: "deploy", Targets: []string{"production"}}},
			{ID: "record-github", Priority: &priorityOne, Match: config.RouteMatch{Source: "github-primary", Repository: "application", Event: "push"}, Action: config.Action{Type: "record_only"}},
		},
	}
}

func githubSignature(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func githubHeaders(secret string, payload []byte) http.Header {
	headers := make(http.Header)
	headers.Set("X-GitHub-Event", "push")
	headers.Set("X-Hub-Signature-256", githubSignature(secret, payload))
	return headers
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
