package runtimecfg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/config"
)

func TestReloadLocksOneCandidateWhileWaitingAndRejectsSecondReload(t *testing.T) {
	store := &managerStore{active: 1}
	reads := 0
	manager := newTestManager(t, store, "initial", func(string) ([]byte, error) {
		reads++
		return []byte(candidateYAML("candidate-one", "resource-one")), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = manager.Run(ctx) }()

	outcome, err := manager.Reload(context.Background(), "operator")
	if err != nil || outcome != ReloadWaiting {
		t.Fatalf("Reload() = %q, %v", outcome, err)
	}
	manager.loadGeneration = func(string, config.EnvLookup, *slog.Logger) (*Generation, error) {
		reads++
		return Compile(candidateBundle("candidate-two", "resource-two"), discardLogger())
	}
	if _, err := manager.Reload(context.Background(), "operator"); !errors.Is(err, ErrReloadInProgress) {
		t.Fatalf("second Reload() error = %v", err)
	}
	store.setActive(0)
	manager.NotifyTerminal()
	eventually(t, func() bool { return manager.statusState() == StateDraining })
	if reads != 1 {
		t.Fatalf("config reads = %d, want 1", reads)
	}
	view := manager.Read()
	defer view.Unlock()
	if got := view.Generation.Repositories()[0].Name; got != "candidate-one" {
		t.Fatalf("published repository = %q", got)
	}
}

func TestReadViewUnlockIsIdempotentAndReleasesWriter(t *testing.T) {
	manager := NewManager(nil, nil, Options{})
	view := manager.Read()
	view.Unlock()
	view.Unlock()

	done := make(chan struct{})
	go func() {
		manager.SetDraining()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("writer remained blocked after ReadView.Unlock")
	}
}

func TestDiscoverConnectionResourcesSnapshotsTheConnectionWithoutHoldingTheGenerationGate(t *testing.T) {
	// Break caught: accepting an unknown ID, resolving by arbitrary URL, or holding reload publication while upstream discovery waits.
	emptyManager := NewManager(nil, &managerStore{}, Options{})
	if resources, err := emptyManager.DiscoverConnectionResources(context.Background(), "missing"); !errors.Is(err, ErrConnectionNotFound) || resources != nil {
		t.Fatalf("empty manager DiscoverConnectionResources() = %#v/%v", resources, err)
	}

	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/project.all" || r.Header.Get("x-api-key") != "secret-key" {
			t.Errorf("request = %s key=%q", r.URL.RequestURI(), r.Header.Get("x-api-key"))
		}
		close(requestStarted)
		<-releaseRequest
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"name":"Project","environments":[{"name":"Production","compose":[{"composeId":"compose-1","name":"API","appName":"api-abc","composeStatus":"done","env":"must-not-return"}]}]}]`)
	}))
	defer server.Close()

	bundle := candidateBundle("initial", "resource-initial")
	bundle.DokployConnections[0].BaseURL = server.URL
	generation, err := Compile(bundle, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(generation, &managerStore{}, Options{})
	if resources, err := manager.DiscoverConnectionResources(context.Background(), "missing"); !errors.Is(err, ErrConnectionNotFound) || resources != nil {
		t.Fatalf("missing DiscoverConnectionResources() = %#v/%v", resources, err)
	}

	type result struct {
		resources []ConnectionResource
		err       error
	}
	resultCh := make(chan result, 1)
	go func() {
		resources, discoverErr := manager.DiscoverConnectionResources(context.Background(), "primary")
		resultCh <- result{resources: resources, err: discoverErr}
	}()
	<-requestStarted

	gateReleased := make(chan struct{})
	go func() {
		manager.SetDraining()
		close(gateReleased)
	}()
	select {
	case <-gateReleased:
	case <-time.After(time.Second):
		t.Fatal("resource discovery held the generation gate during upstream I/O")
	}
	close(releaseRequest)
	discovered := <-resultCh
	if discovered.err != nil {
		t.Fatal(discovered.err)
	}
	want := []ConnectionResource{{
		Type: "compose", ProjectName: "Project", EnvironmentName: "Production",
		Name: "API", AppName: "api-abc", ResourceID: "compose-1", Status: "done",
	}}
	if !reflect.DeepEqual(discovered.resources, want) {
		encoded, _ := json.Marshal(discovered.resources)
		t.Fatalf("resources = %s, want %#v", encoded, want)
	}
}

func TestTargetInventoryJoinsConfiguredTargetsWithOneSafeConnectionDiscovery(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/project.all":
			_, _ = io.WriteString(w, `[{"name":"Project","environments":[{"name":"Production","compose":[{"composeId":"compose-1","name":"API","appName":"stale-app","composeStatus":"done","env":"must-not-return"}]}]}]`)
		case "/api/compose.one":
			if r.URL.Query().Get("composeId") != "compose-1" {
				t.Errorf("compose query = %#v", r.URL.Query())
			}
			_, _ = io.WriteString(w, `{"composeId":"compose-1","appName":"api-abc","env":"must-not-return"}`)
		case "/api/docker.getContainersByAppNameMatch":
			_, _ = io.WriteString(w, `[{"containerId":"container-1","name":"api","state":"running","status":"Up (healthy)"}]`)
		case "/api/docker.getConfig":
			_, _ = io.WriteString(w, `{"State":{"Status":"running","Health":{"Status":"healthy"}},"RestartCount":0,"Config":{"Env":["SECRET=must-not-return"]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	bundle := candidateBundle("initial", "compose-1")
	bundle.DokployConnections[0].BaseURL = server.URL
	bundle.Targets = append(bundle.Targets, config.Target{ID: "missing", Type: "dokploy", Connection: "primary", ResourceType: "compose", ResourceID: "compose-missing"})
	generation, err := Compile(bundle, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(generation, &managerStore{}, Options{Clock: func() time.Time { return time.Unix(10, 0).UTC() }})
	result := manager.TargetInventory(context.Background())
	if requests != 4 || !result.RefreshedAt.Equal(time.Unix(10, 0).UTC()) || len(result.Errors) != 0 || len(result.Targets) != 2 {
		t.Fatalf("inventory = %#v requests=%d", result, requests)
	}
	if got := result.Targets[0]; got.ID != "missing" || got.ConnectionID != "primary" || got.ResourceType != "compose" || got.ResourceID != "compose-missing" || got.Condition != TargetConditionUnavailable {
		t.Fatalf("missing target = %#v", got)
	}
	if got := result.Targets[1]; got.ID != "target" || got.ProjectName != "Project" || got.EnvironmentName != "Production" || got.Name != "API" || got.AppName != "api-abc" || got.Status != "done" || got.Condition != TargetConditionAvailable || got.RuntimeStatus != TargetRuntimeAllRunning || !reflect.DeepEqual(got.Containers, []TargetContainer{{Name: "api", State: "running", Health: "healthy", RestartCount: 0}}) {
		t.Fatalf("discovered target = %#v", got)
	}
	encoded, _ := json.Marshal(result)
	for _, forbidden := range []string{"secret-key", server.URL, "must-not-return", "base_url", "api_key"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("inventory leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestTargetInventoryMarksConfiguredHTTPTargetsAvailableWithoutDiscovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("HTTP inventory unexpectedly sent %s", r.URL.String())
	}))
	defer server.Close()
	bundle := generationBundle("https://dokploy.example.invalid")
	bundle.DokployConnections = nil
	bundle.HTTPConnections = []config.HTTPConnection{{
		ID: "admin", BaseURL: server.URL, AllowPrivateNetwork: true,
		Auth: config.HTTPAuthentication{Type: "bearer", Value: "token"},
	}}
	bundle.Targets = []config.Target{{ID: "application-admin", Type: "http", Connection: "admin", Method: http.MethodPost, Path: "/api/deploy"}}
	bundle.Routes[0].Action.Targets = []string{"application-admin"}
	generation, err := Compile(bundle, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	result := NewManager(generation, &managerStore{}, Options{}).TargetInventory(context.Background())
	if len(result.Errors) != 0 || len(result.Targets) != 1 {
		t.Fatalf("inventory = %#v", result)
	}
	if got := result.Targets[0]; got.ID != "application-admin" || got.Condition != TargetConditionAvailable || got.Name != "application-admin" || len(got.Containers) != 0 {
		t.Fatalf("target = %#v", got)
	}
}

func TestTargetInventoryRetainsConfiguredTargetsWhenDiscoveryFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"api_key":"must-not-return"}`, http.StatusBadGateway)
	}))
	defer server.Close()
	bundle := candidateBundle("initial", "compose-1")
	bundle.DokployConnections[0].BaseURL = server.URL
	generation, err := Compile(bundle, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	result := NewManager(generation, &managerStore{}, Options{}).TargetInventory(context.Background())
	if len(result.Targets) != 1 || result.Targets[0].ID != "target" || result.Targets[0].Condition != TargetConditionRefreshFailed || len(result.Errors) != 1 || result.Errors[0].ConnectionID != "primary" || result.Errors[0].Code != "upstream_unavailable" {
		t.Fatalf("failed inventory = %#v", result)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "must-not-return") || strings.Contains(string(encoded), server.URL) {
		t.Fatalf("failed inventory leaked upstream data: %s", encoded)
	}
}

func TestSummarizeTargetRuntimeKeepsDeploymentAndRuntimeIndependent(t *testing.T) {
	tests := []struct {
		name       string
		containers []TargetContainer
		want       TargetRuntimeStatus
	}{
		{name: "empty", containers: nil, want: TargetRuntimeEmpty},
		{name: "all running", containers: []TargetContainer{{State: "running"}, {State: "running", Health: "healthy"}}, want: TargetRuntimeAllRunning},
		{name: "partially unhealthy", containers: []TargetContainer{{State: "running", Health: "healthy"}, {State: "running", Health: "unhealthy"}}, want: TargetRuntimeDegraded},
		{name: "partially stopped", containers: []TargetContainer{{State: "running"}, {State: "exited"}}, want: TargetRuntimeDegraded},
		{name: "restarting", containers: []TargetContainer{{State: "restarting"}}, want: TargetRuntimeDegraded},
		{name: "stopped", containers: []TargetContainer{{State: "exited"}, {State: "dead"}}, want: TargetRuntimeStopped},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := summarizeTargetRuntime(test.containers); got != test.want {
				t.Fatalf("summarizeTargetRuntime() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTargetInventoryReusesRuntimeForAliasesOfOneCompose(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/project.all":
			_, _ = io.WriteString(w, `[{"name":"Project","environments":[{"name":"Production","compose":[{"composeId":"compose-1","name":"API","appName":"stale-app","composeStatus":"done"}]}]}]`)
		case "/api/compose.one":
			_, _ = io.WriteString(w, `{"composeId":"compose-1","appName":"api-abc"}`)
		case "/api/docker.getContainersByAppNameMatch":
			if r.URL.Query().Get("appName") != "api-abc" {
				t.Errorf("app name = %q", r.URL.Query().Get("appName"))
			}
			_, _ = io.WriteString(w, `[{"containerId":"container-1","name":"api"}]`)
		case "/api/docker.getConfig":
			_, _ = io.WriteString(w, `{"Name":"/api","State":{"Status":"running"},"RestartCount":0}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	bundle := candidateBundle("initial", "compose-1")
	bundle.DokployConnections[0].BaseURL = server.URL
	bundle.Targets = append(bundle.Targets, config.Target{ID: "alias", Type: "dokploy", Connection: "primary", ResourceType: "compose", ResourceID: "compose-1"})
	generation, err := Compile(bundle, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	result := NewManager(generation, &managerStore{}, Options{}).TargetInventory(context.Background())
	if requests != 4 || len(result.Targets) != 2 {
		t.Fatalf("inventory = %#v requests=%d", result, requests)
	}
	for _, target := range result.Targets {
		if target.AppName != "api-abc" || target.RuntimeStatus != TargetRuntimeAllRunning || !reflect.DeepEqual(target.Containers, []TargetContainer{{Name: "api", State: "running"}}) {
			t.Fatalf("target = %#v", target)
		}
	}
}

func TestReloadFailuresBeforeWaitingReturnIdleWithoutChangingGeneration(t *testing.T) {
	tests := []struct {
		name     string
		readFile func(string) ([]byte, error)
		storeErr error
		wantErr  error
	}{
		{
			name: "invalid config", readFile: func(string) ([]byte, error) { return []byte("version: 2\n"), nil },
			wantErr: ErrInvalidConfig,
		},
		{
			name: "read unavailable", readFile: func(string) ([]byte, error) { return nil, errors.New("permission denied") },
			wantErr: ErrInvalidConfig,
		},
		{
			name: "active query unavailable", readFile: func(string) ([]byte, error) { return []byte(candidateYAML("next", "resource")), nil },
			storeErr: errors.New("sqlite unavailable"), wantErr: ErrPreWaitUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &managerStore{activeErr: tt.storeErr}
			manager := newTestManager(t, store, "initial", tt.readFile)
			before := manager.Current().Digest()
			if _, err := manager.Reload(context.Background(), "operator"); !errors.Is(err, tt.wantErr) {
				t.Fatalf("Reload() error = %v, want %v", err, tt.wantErr)
			}
			if manager.statusState() != StateIdle || manager.Current().Digest() != before {
				t.Fatalf("state/digest changed: %s/%s", manager.statusState(), manager.Current().Digest())
			}
		})
	}
}

func TestReloadLoadsOnePathCandidateAndRetainsThePublishedGenerationOnFailure(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "hookfly.yaml")
	writeGeneration(t, path, "compose-a")
	initial, err := Load(path, nil, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	store := &managerStore{}
	manager := NewManager(initial, store, Options{Path: path, Logger: discardLogger()})

	writeGeneration(t, path, "compose-b")
	if outcome, err := manager.Reload(context.Background(), "operator"); err != nil || outcome != ReloadApplied {
		t.Fatalf("Reload() = %q, %v", outcome, err)
	}
	published := manager.Current()
	if published == initial || published.Digest() == initial.Digest() {
		t.Fatal("generation B was not published")
	}
	if idle, err := manager.SetIdleIfQueueEmpty(context.Background()); err != nil || !idle {
		t.Fatalf("SetIdleIfQueueEmpty() = %v, %v", idle, err)
	}

	assertFailedReload := func(name string) {
		t.Helper()
		if _, err := manager.Reload(context.Background(), "operator"); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("%s Reload() error = %v", name, err)
		}
		if manager.Current() != published || manager.Current().Digest() != published.Digest() || manager.statusState() != StateIdle || manager.candidate != nil {
			t.Fatalf("%s replaced or retained candidate: current=%p state=%s candidate=%p", name, manager.Current(), manager.statusState(), manager.candidate)
		}
	}

	if err := os.WriteFile(filepath.Join(directory, "conf.d", "sources.yaml"), []byte("kind: Unknown\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertFailedReload("unknown kind")
	writeGeneration(t, path, "compose-b")
	if err := os.WriteFile(filepath.Join(directory, "conf.d", "duplicate.yaml"), []byte(sourcesYAML()), 0o600); err != nil {
		t.Fatal(err)
	}
	assertFailedReload("duplicate source ID")
	if err := os.Remove(filepath.Join(directory, "conf.d", "duplicate.yaml")); err != nil {
		t.Fatal(err)
	}
	manager.loadGeneration = func(string, config.EnvLookup, *slog.Logger) (*Generation, error) {
		return nil, errors.New("configuration directory changed during discovery")
	}
	assertFailedReload("discovery add/remove race")
}

func TestReloadPathErrorsRemainUnavailableAndPreserveGeneration(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "hookfly.yaml")
	writeGeneration(t, path, "compose-a")
	initial, err := Load(path, nil, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(initial, &managerStore{}, Options{Path: path, Logger: discardLogger()})

	assertUnavailable := func(name string) {
		t.Helper()
		if _, err := manager.Reload(context.Background(), "operator"); !errors.Is(err, ErrPreWaitUnavailable) {
			t.Fatalf("%s Reload() error = %v", name, err)
		}
		if manager.Current() != initial || manager.statusState() != StateIdle || manager.candidate != nil {
			t.Fatalf("%s changed manager state", name)
		}
	}

	manager.path = filepath.Join(directory, "missing.yaml")
	assertUnavailable("missing global path")
	manager.path = path
	manager.loadGeneration = func(string, config.EnvLookup, *slog.Logger) (*Generation, error) {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
	}
	assertUnavailable("unreadable global path")
}

func TestReloadResourceRemovalPathErrorIsInvalidAndPreservesGeneration(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "hookfly.yaml")
	writeGeneration(t, path, "compose-a")
	initial, err := Load(path, nil, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(initial, &managerStore{}, Options{Path: path, Logger: discardLogger()})
	resourcePath := filepath.Join(directory, "conf.d", "sources.yaml")
	manager.loadGeneration = func(string, config.EnvLookup, *slog.Logger) (*Generation, error) {
		return nil, fmt.Errorf("read configuration resource: %w", &fs.PathError{Op: "open", Path: resourcePath, Err: fs.ErrNotExist})
	}
	if _, err := manager.Reload(context.Background(), "operator"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Reload() error = %v", err)
	}
	if manager.Current() != initial || manager.Current().Digest() != initial.Digest() || manager.statusState() != StateIdle || manager.candidate != nil {
		t.Fatalf("resource removal changed manager state")
	}
}

func TestWaitingQueryFailureRetainsCandidateAndRetriesToPublish(t *testing.T) {
	store := &managerStore{active: 1}
	manager := newTestManager(t, store, "initial", func(string) ([]byte, error) {
		return []byte(candidateYAML("candidate", "resource")), nil
	})
	if outcome, err := manager.Reload(context.Background(), "operator"); err != nil || outcome != ReloadWaiting {
		t.Fatalf("Reload() = %q, %v", outcome, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = manager.Run(ctx) }()
	store.setError(errors.New("temporary query failure"))
	manager.NotifyTerminal()
	eventually(t, func() bool { return manager.lastResultCode() == "waiting_retry" })
	if manager.statusState() != StateWaiting {
		t.Fatalf("state = %s", manager.statusState())
	}
	store.setError(nil)
	store.setActive(0)
	manager.NotifyTerminal()
	eventually(t, func() bool { return manager.statusState() == StateDraining })
	if manager.Current().Repositories()[0].Name != "candidate" {
		t.Fatal("locked candidate was not published")
	}
}

func TestInitializeChoosesIdleWaitingOrDrainingFromDurableState(t *testing.T) {
	tests := []struct {
		name           string
		active, queued int
		want           State
		recovery       bool
	}{
		{"empty", 0, 0, StateIdle, false},
		{"backlog with active", 1, 2, StateWaiting, true},
		{"backlog without active", 0, 2, StateDraining, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &managerStore{active: tt.active, queued: tt.queued}
			manager := newTestManager(t, store, "initial", nil)
			if err := manager.Initialize(context.Background()); err != nil {
				t.Fatal(err)
			}
			status, err := manager.Status(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if status.State != tt.want || status.Recovery != tt.recovery {
				t.Fatalf("status = %#v", status)
			}
		})
	}
}

func TestSetIdleIfQueueEmptyLinearizesTheDrainBoundary(t *testing.T) {
	store := &managerStore{queued: 1}
	manager := newTestManager(t, store, "initial", nil)
	if err := manager.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if idle, err := manager.SetIdleIfQueueEmpty(context.Background()); err != nil || idle {
		t.Fatalf("SetIdleIfQueueEmpty() = %v, %v", idle, err)
	}
	store.setQueued(0)
	if idle, err := manager.SetIdleIfQueueEmpty(context.Background()); err != nil || !idle {
		t.Fatalf("SetIdleIfQueueEmpty() = %v, %v", idle, err)
	}
	if manager.statusState() != StateIdle {
		t.Fatalf("state = %s", manager.statusState())
	}
}

func TestStatusContainsOnlySafeLifecycleMetadata(t *testing.T) {
	store := &managerStore{active: 2, queued: 3}
	manager := newTestManager(t, store, "initial", nil)
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"secret-token", "secret-key", "dokploy.example.invalid", "resource-initial", "config.yaml"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("status exposed %q: %s", forbidden, text)
		}
	}
	if status.ActiveDeliveries != 2 || status.QueuedEvents != 3 || status.CurrentDigest == "" {
		t.Fatalf("status = %#v", status)
	}
}

type managerStore struct {
	mu        sync.Mutex
	active    int
	queued    int
	activeErr error
	queuedErr error
}

func (s *managerStore) CountActive(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active, s.activeErr
}

func (s *managerStore) CountQueued(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queued, s.queuedErr
}

func (s *managerStore) setActive(active int) {
	s.mu.Lock()
	s.active = active
	s.mu.Unlock()
}

func (s *managerStore) setQueued(queued int) {
	s.mu.Lock()
	s.queued = queued
	s.mu.Unlock()
}

func (s *managerStore) setError(err error) {
	s.mu.Lock()
	s.activeErr = err
	s.mu.Unlock()
}

func newTestManager(t *testing.T, store lifecycleStore, initialName string, readFile func(string) ([]byte, error)) *Manager {
	t.Helper()
	initial, err := Compile(candidateBundle(initialName, "resource-"+initialName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(initial, store, Options{
		Path: "config.yaml", Clock: func() time.Time { return time.Unix(10, 0).UTC() },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if readFile != nil {
		manager.loadGeneration = func(path string, lookup config.EnvLookup, logger *slog.Logger) (*Generation, error) {
			content, err := readFile(path)
			if err != nil {
				return nil, err
			}
			parts := strings.Split(string(content), "\x00")
			if len(parts) != 2 {
				return nil, errors.New("invalid test candidate")
			}
			return Compile(candidateBundle(parts[0], parts[1]), logger)
		}
	}
	manager.retryMinimum = time.Millisecond
	manager.retryMaximum = 5 * time.Millisecond
	return manager
}

func candidateYAML(projectName, resourceID string) string {
	return projectName + "\x00" + resourceID
}

func candidateBundle(repositoryName, resourceID string) *config.Bundle {
	return &config.Bundle{
		Global:             config.Global{Kind: "Hookfly", History: config.GlobalHistory{GlobalLimit: 10, DefaultRepositoryLimit: 5}, Polling: config.Polling{Interval: config.Duration{Duration: time.Second}, Timeout: config.Duration{Duration: 30 * time.Minute}}},
		GitHubSources:      []config.GitHubSource{{ID: "github-primary", Secret: "secret-token", Repositories: []config.Repository{{ID: "application", Name: repositoryName, ExternalID: "101"}}}},
		DokployConnections: []config.DokployConnection{{ID: "primary", BaseURL: "https://dokploy.example.invalid", APIKey: "secret-key"}},
		Targets:            []config.Target{{ID: "target", Type: "dokploy", Connection: "primary", ResourceType: "compose", ResourceID: resourceID}},
	}
}

func writeGeneration(t *testing.T, path, resourceID string) {
	t.Helper()
	directory := filepath.Dir(path)
	if err := os.MkdirAll(filepath.Join(directory, "conf.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		path: "kind: Hookfly\nconfig_dir: conf.d\nhistory:\n  global_limit: 100\n  default_repository_limit: 10\npolling:\n  interval: 1s\n  timeout: 30m\n",
		filepath.Join(directory, "conf.d", "sources.yaml"): sourcesYAML(),
		filepath.Join(directory, "conf.d", "targets.yaml"): "kind: DokployTargets\nconnections:\n  - id: primary\n    base_url: https://dokploy.example.invalid\n    api_key: test-key\ntargets:\n  - id: production\n    type: dokploy\n    connection: primary\n    resource_type: compose\n    resource_id: " + resourceID + "\n",
		filepath.Join(directory, "conf.d", "routes.yaml"):  "kind: Routes\nroutes:\n  - id: record\n    priority: 1\n    match:\n      source: github-primary\n      repository: application\n      event: push\n    action:\n      type: record_only\n",
	}
	for file, content := range files {
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func sourcesYAML() string {
	return "kind: GitHubSources\nsources:\n  - id: github-primary\n    secret: test-secret\n    repositories:\n      - id: application\n        name: Application\n        repository_id: 101\n"
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not satisfied")
}
