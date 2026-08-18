package runtimecfg

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/hoomoli/hookfly/internal/config"
	"github.com/hoomoli/hookfly/internal/domain"
)

func TestPublicExamplesCompile(t *testing.T) {
	configRoot := filepath.Join("..", "..", "configs")
	lookup := func(name string) (string, bool) {
		value, ok := map[string]string{
			"GITLAB_TOKEN":            "test-gitlab-token",
			"GITHUB_WEBHOOK_SECRET":   "test-github-secret",
			"HARBOR_AUTHORIZATION":    "Bearer test-harbor-secret",
			"DOKPLOY_API_KEY":         "test-dokploy-key",
			"APPLICATION_ADMIN_TOKEN": "test-application-admin-token",
		}[name]
		return value, ok
	}

	for _, test := range []struct {
		name                                 string
		path                                 string
		wantSources, wantTargets, wantRoutes int
	}{
		{
			name: "starter compiles without runtime bindings",
			path: filepath.Join(configRoot, "hookfly.yaml"),
		},
		{
			name:        "routing example compiles all providers and deployment target types",
			path:        filepath.Join(configRoot, "routing.example", "hookfly.yaml"),
			wantSources: 3,
			wantTargets: 2,
			wantRoutes:  3,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate, err := config.Discover(test.path)
			if err != nil {
				t.Fatalf("Discover() error = %v", err)
			}
			bundle, err := config.Decode(candidate, lookup)
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if err := config.ValidateAndCanonicalize(bundle); err != nil {
				t.Fatalf("ValidateAndCanonicalize() error = %v", err)
			}
			generation, err := Compile(bundle, discardLogger())
			if err != nil {
				t.Fatalf("Compile() error = %v", err)
			}
			if got := len(generation.sources); got != test.wantSources {
				t.Fatalf("source count = %d, want %d", got, test.wantSources)
			}
			if got := len(generation.targets); got != test.wantTargets {
				t.Fatalf("target count = %d, want %d", got, test.wantTargets)
			}
			if got := len(generation.routes); got != test.wantRoutes {
				t.Fatalf("route count = %d, want %d", got, test.wantRoutes)
			}
		})
	}
}

func TestPublicExampleRoutesDeployCanonicalProviderEvents(t *testing.T) {
	bundle, err := publicExampleBundle()
	if err != nil {
		t.Fatal(err)
	}
	generation, err := Compile(bundle, discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	wantActions := map[string]config.Action{
		"deploy-gitlab-main":   {Type: "deploy", Targets: []string{"production"}},
		"deploy-github-main":   {Type: "deploy", Targets: []string{"production"}},
		"deploy-harbor-latest": {Type: "deploy", Targets: []string{"production"}},
	}
	if got := len(generation.routes); got != len(wantActions) {
		t.Fatalf("route count = %d, want %d", got, len(wantActions))
	}
	for _, route := range generation.routes {
		want, found := wantActions[route.ID]
		if !found {
			t.Fatalf("unexpected example route %q", route.ID)
		}
		if route.Action.Type != want.Type || len(route.Action.Targets) != 1 || route.Action.Targets[0] != want.Targets[0] {
			t.Fatalf("route %q action = %#v, want deploy to production", route.ID, route.Action)
		}
	}

	for _, test := range []struct {
		name, provider, sourceID, event string
		headers                         http.Header
		payload                         []byte
		wantRuleID                      string
		wantRepository                  string
	}{
		{
			name: "GitLab pipeline", provider: "gitlab", sourceID: "gitlab-example", event: "pipeline",
			headers:        publicExampleHeader("X-Gitlab-Event", "Pipeline Hook"),
			payload:        []byte(`{"project":{"id":10001},"object_attributes":{"ref":"main","status":"SUCCESS","sha":"abc123","id":88,"source":"PUSH"}}`),
			wantRuleID:     "deploy-gitlab-main",
			wantRepository: "application",
		},
		{
			name: "GitHub workflow run", provider: "github", sourceID: "github-example", event: "pipeline",
			headers:        publicExampleHeader("X-GitHub-Event", "workflow_run"),
			payload:        []byte(`{"repository":{"id":20001},"workflow_run":{"head_branch":"main","conclusion":"SUCCESS","status":"completed","head_sha":"abc123","id":88,"event":"push"}}`),
			wantRuleID:     "deploy-github-main",
			wantRepository: "application",
		},
		{
			name: "Harbor artifact push", provider: "harbor", sourceID: "harbor-example", event: "artifact_push",
			headers:        publicExampleHeader("Content-Type", "application/json"),
			payload:        []byte(`{"type":"PUSH_ARTIFACT","event_data":{"resources":[{"digest":"sha256:abc123","tag":"latest","resource_url":"registry.example.invalid/example/application:latest"}],"repository":{"repo_full_name":"example/application"}}}`),
			wantRuleID:     "deploy-harbor-latest",
			wantRepository: "application-image",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, found := generation.Source(test.provider, test.sourceID)
			if !found {
				t.Fatalf("source %s/%s is missing", test.provider, test.sourceID)
			}
			event, incoming, err := source.Normalize(test.headers, test.payload)
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if !incoming.Supported || event.Event != test.event || event.Repository != test.wantRepository {
				t.Fatalf("Normalize() event/incoming = %#v/%#v", event, incoming)
			}
			decision, err := generation.Route(event)
			if err != nil {
				t.Fatalf("Route() error = %v", err)
			}
			if decision.RoutingResult != domain.RoutingDeploy || decision.RuleID != test.wantRuleID {
				t.Fatalf("Route() decision = %#v, want deploy via %q", decision, test.wantRuleID)
			}
			if len(decision.Deliveries) != 1 || decision.Deliveries[0].TargetID != "production" {
				t.Fatalf("Route() deliveries = %#v, want only production", decision.Deliveries)
			}
		})
	}
}

func publicExampleHeader(name, value string) http.Header {
	header := make(http.Header)
	header.Set(name, value)
	return header
}

func publicExampleBundle() (*config.Bundle, error) {
	lookup := func(name string) (string, bool) {
		value, ok := map[string]string{
			"GITLAB_TOKEN":            "test-gitlab-token",
			"GITHUB_WEBHOOK_SECRET":   "test-github-secret",
			"HARBOR_AUTHORIZATION":    "Bearer test-harbor-secret",
			"DOKPLOY_API_KEY":         "test-dokploy-key",
			"APPLICATION_ADMIN_TOKEN": "test-application-admin-token",
		}[name]
		return value, ok
	}
	candidate, err := config.Discover(filepath.Join("..", "..", "configs", "routing.example", "hookfly.yaml"))
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
	return bundle, nil
}
