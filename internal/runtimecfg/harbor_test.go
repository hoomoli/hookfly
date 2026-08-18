package runtimecfg

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/hoomoli/hookfly/internal/config"
	"github.com/hoomoli/hookfly/internal/domain"
	"github.com/hoomoli/hookfly/internal/store"
)

func TestCompileCreatesHarborSourceAndRoutesArtifactPush(t *testing.T) {
	priority := 10
	bundle := &config.Bundle{
		Global: config.Global{Kind: "Hookfly"},
		HarborSources: []config.HarborSource{{
			ID: "harbor-primary", Authorization: "Bearer harbor-secret",
			Repositories: []config.Repository{{ID: "application-image", Name: "Application Image", ExternalID: "team/application"}},
		}},
		Routes: []config.Route{{
			ID: "record-harbor", Priority: &priority,
			Match:  config.RouteMatch{Source: "harbor-primary", Repository: "application-image", Event: "artifact_push"},
			Action: config.Action{Type: "record_only"},
		}},
	}
	generation, err := Compile(bundle, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	source, found := generation.Source("harbor", "harbor-primary")
	if !found {
		t.Fatal("Harbor source is missing")
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer harbor-secret")
	headers.Set("Content-Type", "application/json")
	body := []byte(`{"type":"PUSH_ARTIFACT","event_data":{"resources":[{"digest":"sha256:abc123","tag":"latest"}],"repository":{"repo_full_name":"team/application"}}}`)
	if !source.Authenticate(headers, body) {
		t.Fatal("Authenticate() = false")
	}
	event, incoming, err := source.Normalize(headers, body)
	wantEvent := domain.CanonicalEvent{Provider: "harbor", Source: "harbor-primary", Repository: "application-image", Event: "artifact_push", Ref: "latest", Revision: "sha256:abc123"}
	if err != nil || !incoming.Supported || event != wantEvent {
		t.Fatalf("Normalize() = %#v/%#v/%v", event, incoming, err)
	}
	decision, err := generation.Route(event)
	if err != nil || decision.RoutingResult != domain.RoutingRecordOnly || decision.RuleID != "record-harbor" {
		t.Fatalf("Route() = %#v/%v", decision, err)
	}
	wantRepositories := []store.ConfiguredRepository{{Provider: "harbor", SourceID: "harbor-primary", ID: "application-image", Name: "Application Image"}}
	if got := generation.Repositories(); !reflect.DeepEqual(got, wantRepositories) {
		t.Fatalf("Repositories() = %#v, want %#v", got, wantRepositories)
	}
}
