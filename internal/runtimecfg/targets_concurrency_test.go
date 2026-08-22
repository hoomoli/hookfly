package runtimecfg

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/config"
)

func TestTargetInventoryQueriesIndependentComposesConcurrently(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/project.all":
			io.WriteString(w, `[{"name":"Project","environments":[{"name":"Production","compose":[{"composeId":"compose-1","name":"API"},{"composeId":"compose-2","name":"Worker"}]}]}]`)
		case "/api/compose.one":
			started <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			io.WriteString(w, `{"appName":"app"}`)
		case "/api/docker.getContainersByAppNameMatch":
			io.WriteString(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer close(release)
	bundle := candidateBundle("initial", "compose-1")
	bundle.DokployConnections[0].BaseURL = server.URL
	bundle.Targets = append(bundle.Targets, config.Target{ID: "second", Type: "dokploy", Connection: "primary", ResourceType: "compose", ResourceID: "compose-2"})
	generation, err := Compile(bundle, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan TargetInventory, 1)
	go func() { done <- NewManager(generation, &managerStore{}, Options{}).TargetInventory(ctx) }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("independent Compose queries were serialized")
		}
	}
	// Cancel the in-flight work; the inventory must still retain both configured targets.
	cancel()
	select {
	case result := <-done:
		if len(result.Targets) != 2 {
			t.Fatalf("inventory = %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("inventory ignored cancellation")
	}
}

func TestTargetInventoryRetainsBindingsWhenRuntimeDeadlineExpires(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/project.all" {
			io.WriteString(w, `[{"name":"Project","environments":[{"name":"Production","compose":[{"composeId":"compose-1","name":"API","composeStatus":"done"}]}]}]`)
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	bundle := candidateBundle("initial", "compose-1")
	bundle.DokployConnections[0].BaseURL = server.URL
	generation, err := Compile(bundle, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result := NewManager(generation, &managerStore{}, Options{}).TargetInventory(ctx)
	if len(result.Targets) != 1 || result.Targets[0].Name != "API" || result.Targets[0].Status != "done" || result.Targets[0].RuntimeStatus != TargetRuntimeUnknown {
		t.Fatalf("partial inventory = %#v", result)
	}
}
