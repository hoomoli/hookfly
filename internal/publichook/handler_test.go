package publichook_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/hoomoli/hookfly/internal/domain"
	"github.com/hoomoli/hookfly/internal/publichook"
	"github.com/hoomoli/hookfly/internal/runtimecfg"
	"github.com/hoomoli/hookfly/internal/store"
)

func TestProviderWebhookContracts(t *testing.T) {
	manager := webhookManager(t)
	tests := []struct {
		name, provider, source, body string
		headers                      http.Header
		result                       domain.IngestResult
		ingestErr                    error
		wantStatus                   int
		wantCalls                    int
		wantRouting                  domain.RoutingResult
		wantCanonical                domain.CanonicalEvent
		wantTarget                   string
	}{
		{
			name: "GitLab matched", provider: "gitlab", source: "gitlab-primary", body: gitlabPipeline("7"),
			headers: gitlabHeaders("gitlab-token", "gitlab-delivery"), result: domain.IngestResult{EventID: "gitlab-event"},
			wantStatus: http.StatusAccepted, wantCalls: 1, wantRouting: domain.RoutingDeploy,
			wantCanonical: domain.CanonicalEvent{Provider: "gitlab", Source: "gitlab-primary", Repository: "application", Event: "pipeline", Ref: "main", Status: "success", Revision: "abc123", ExternalID: "88", Trigger: "push"},
			wantTarget:    "production",
		},
		{
			name: "GitHub matched", provider: "github", source: "github-primary", body: githubWorkflow("101"),
			headers: githubHeaders("github-secret", "github-delivery", githubWorkflow("101")), result: domain.IngestResult{EventID: "github-event"},
			wantStatus: http.StatusAccepted, wantCalls: 1, wantRouting: domain.RoutingDeploy,
			wantCanonical: domain.CanonicalEvent{Provider: "github", Source: "github-primary", Repository: "application", Event: "pipeline", Ref: "main", Status: "success", Revision: "abc123", ExternalID: "91", Trigger: "push"},
			wantTarget:    "production",
		},
		{
			name: "unconfigured repository", provider: "gitlab", source: "gitlab-primary", body: gitlabPipeline("999"),
			headers: gitlabHeaders("gitlab-token", "unmatched"), wantStatus: http.StatusUnauthorized,
		},
		{
			name: "GitHub unconfigured repository", provider: "github", source: "github-primary", body: githubWorkflow("999"),
			headers: githubHeaders("github-secret", "github-unmatched", githubWorkflow("999")), result: domain.IngestResult{EventID: "github-unmatched"},
			wantStatus: http.StatusAccepted, wantCalls: 1, wantRouting: domain.RoutingUnmatched,
			wantCanonical: domain.CanonicalEvent{Provider: "github", Source: "github-primary", Event: "pipeline", Ref: "main", Status: "success", Revision: "abc123", ExternalID: "91", Trigger: "push"},
		},
		{
			name: "unsupported authenticated", provider: "gitlab", source: "gitlab-primary", body: `{"project_id":7}`,
			headers: gitlabUnsupportedHeaders("gitlab-token"), result: domain.IngestResult{EventID: "unsupported"},
			wantStatus: http.StatusAccepted, wantCalls: 1, wantRouting: domain.RoutingUnsupported,
			wantCanonical: domain.CanonicalEvent{Provider: "gitlab", Source: "gitlab-primary", Repository: "application", Event: "deployment_hook"},
		},
		{
			name: "GitHub unsupported authenticated", provider: "github", source: "github-primary", body: `{"repository":{"id":101}}`,
			headers: githubEventHeaders("github-secret", "github-unsupported", "custom_event", `{"repository":{"id":101}}`), result: domain.IngestResult{EventID: "github-unsupported"},
			wantStatus: http.StatusAccepted, wantCalls: 1, wantRouting: domain.RoutingUnsupported,
			wantCanonical: domain.CanonicalEvent{Provider: "github", Source: "github-primary", Repository: "application", Event: "custom_event"},
		},
		{
			name: "GitLab duplicate", provider: "gitlab", source: "gitlab-primary", body: gitlabPipeline("7"),
			headers: gitlabHeaders("gitlab-token", "gitlab-duplicate"), result: domain.IngestResult{EventID: "gitlab-existing", Duplicate: true},
			wantStatus: http.StatusAccepted, wantCalls: 1, wantRouting: domain.RoutingDeploy,
			wantCanonical: domain.CanonicalEvent{Provider: "gitlab", Source: "gitlab-primary", Repository: "application", Event: "pipeline", Ref: "main", Status: "success", Revision: "abc123", ExternalID: "88", Trigger: "push"},
			wantTarget:    "production",
		},
		{
			name: "duplicate", provider: "github", source: "github-primary", body: githubWorkflow("101"),
			headers: githubHeaders("github-secret", "duplicate", githubWorkflow("101")), result: domain.IngestResult{EventID: "existing", Duplicate: true},
			wantStatus: http.StatusAccepted, wantCalls: 1, wantRouting: domain.RoutingDeploy,
			wantCanonical: domain.CanonicalEvent{Provider: "github", Source: "github-primary", Repository: "application", Event: "pipeline", Ref: "main", Status: "success", Revision: "abc123", ExternalID: "91", Trigger: "push"},
			wantTarget:    "production",
		},
		{
			name: "missing token", provider: "gitlab", source: "gitlab-primary", body: gitlabPipeline("7"),
			headers: gitlabHeaders("", "missing-token"), wantStatus: http.StatusUnauthorized,
		},
		{
			name: "GitHub unknown source", provider: "github", source: "missing", body: githubWorkflow("101"),
			headers: githubHeaders("github-secret", "github-unknown", githubWorkflow("101")), wantStatus: http.StatusNotFound,
		},
		{
			name: "GitLab unknown token", provider: "gitlab", source: "gitlab-primary", body: gitlabPipeline("7"),
			headers: gitlabHeaders("unknown-token", "gitlab-unknown-token"), wantStatus: http.StatusUnauthorized,
		},
		{
			name: "wrong provider source", provider: "github", source: "gitlab-primary", body: githubWorkflow("101"),
			headers: githubHeaders("github-secret", "wrong-provider", githubWorkflow("101")), wantStatus: http.StatusNotFound,
		},
		{
			name: "GitLab invalid authentication before malformed JSON", provider: "gitlab", source: "gitlab-primary", body: `{`,
			headers: gitlabHeaders("wrong-token", "gitlab-invalid"), wantStatus: http.StatusUnauthorized,
		},
		{
			name: "invalid authentication before malformed JSON", provider: "github", source: "github-primary", body: `{`,
			headers: githubHeaders("wrong-secret", "invalid", `{`), wantStatus: http.StatusUnauthorized,
		},
		{
			name: "GitLab malformed authenticated JSON", provider: "gitlab", source: "gitlab-primary", body: `{`,
			headers: gitlabHeaders("gitlab-token", "gitlab-malformed"), wantStatus: http.StatusBadRequest,
		},
		{
			name: "malformed authenticated JSON", provider: "github", source: "github-primary", body: `{`,
			headers: githubHeaders("github-secret", "malformed", `{`), wantStatus: http.StatusBadRequest,
		},
		{
			name: "store failure", provider: "gitlab", source: "gitlab-primary", body: gitlabPipeline("7"),
			headers: gitlabHeaders("gitlab-token", "store-failure"), ingestErr: errors.New("database unavailable"),
			wantStatus: http.StatusServiceUnavailable, wantCalls: 1, wantRouting: domain.RoutingDeploy,
			wantCanonical: domain.CanonicalEvent{Provider: "gitlab", Source: "gitlab-primary", Repository: "application", Event: "pipeline", Ref: "main", Status: "success", Revision: "abc123", ExternalID: "88", Trigger: "push"},
			wantTarget:    "production",
		},
		{
			name: "GitHub store failure", provider: "github", source: "github-primary", body: githubWorkflow("101"),
			headers: githubHeaders("github-secret", "github-store-failure", githubWorkflow("101")), ingestErr: errors.New("database unavailable"),
			wantStatus: http.StatusServiceUnavailable, wantCalls: 1, wantRouting: domain.RoutingDeploy,
			wantCanonical: domain.CanonicalEvent{Provider: "github", Source: "github-primary", Repository: "application", Event: "pipeline", Ref: "main", Status: "success", Revision: "abc123", ExternalID: "91", Trigger: "push"},
			wantTarget:    "production",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ingester := &recordingIngester{result: test.result, err: test.ingestErr}
			handler := providerMux(test.provider, publichook.New(test.provider, manager, ingester, func() time.Time {
				return time.UnixMilli(1_725_000_000_000).UTC()
			}))
			request := httptest.NewRequest(http.MethodPost, providerWebhookPath(test.provider, test.source), strings.NewReader(test.body))
			request.Header = test.headers
			request.Header.Set("Authorization", "Bearer test-only")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			commands := ingester.directCommands()
			deferredCommands := ingester.deferredCommands()
			if len(commands) != test.wantCalls || len(deferredCommands) != 0 {
				t.Fatalf("direct/deferred ingest calls = %d/%d, want %d/0", len(commands), len(deferredCommands), test.wantCalls)
			}
			if test.wantCalls == 0 {
				return
			}
			command := commands[0]
			if command.DeliveryID == "" || command.CanonicalEvent != test.wantCanonical || command.RoutingResult != test.wantRouting {
				t.Fatalf("command = %#v", command)
			}
			if test.wantTarget != "" && (len(command.Deliveries) != 1 || command.Deliveries[0].TargetID != test.wantTarget) {
				t.Fatalf("deliveries = %#v", command.Deliveries)
			}
			lowerHeaders := bytes.ToLower(command.HeadersJSON)
			if bytes.Contains(lowerHeaders, []byte("token")) || bytes.Contains(lowerHeaders, []byte("signature")) ||
				bytes.Contains(lowerHeaders, []byte("authorization")) || bytes.Contains(lowerHeaders, []byte("test-only")) {
				t.Fatalf("unsafe headers persisted: %s", command.HeadersJSON)
			}
			if !json.Valid(command.HeadersJSON) || string(command.PayloadJSON) != test.body {
				t.Fatalf("persisted request = headers %s, payload %s", command.HeadersJSON, command.PayloadJSON)
			}
			if len(command.RequestJSON) != 0 {
				t.Fatalf("non-forward route retained private request: %s", command.RequestJSON)
			}
			if command.ConfigDigest == "" {
				t.Fatal("config digest is empty")
			}
			if test.wantStatus == http.StatusAccepted {
				accepted := decodeAccepted(t, response.Body.Bytes())
				if accepted["event_id"] != test.result.EventID || accepted["status"] != "accepted" || accepted["duplicate"] != test.result.Duplicate {
					t.Fatalf("accepted response = %#v", accepted)
				}
			}
		})
	}
}

func TestGitLabCommonEndpointSelectsSourceByToken(t *testing.T) {
	generation, err := runtimecfg.Compile(&config.Bundle{
		Global: config.Global{Kind: "Hookfly"},
		GitLabSources: []config.GitLabSource{
			{ID: "primary", Token: "primary-token", Repositories: []config.Repository{{ID: "application", ExternalID: "7"}}},
			{ID: "secondary", Token: "secondary-token", Repositories: []config.Repository{{ID: "operations", ExternalID: "7"}}},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager := runtimecfg.NewManager(generation, nil, runtimecfg.Options{})

	for _, test := range []struct {
		token, wantSource, wantRepository string
	}{
		{token: "primary-token", wantSource: "primary", wantRepository: "application"},
		{token: "secondary-token", wantSource: "secondary", wantRepository: "operations"},
	} {
		t.Run(test.wantSource, func(t *testing.T) {
			ingester := &recordingIngester{result: domain.IngestResult{EventID: "event"}}
			handler := providerMux("gitlab", publichook.New("gitlab", manager, ingester, time.Now))
			request := httptest.NewRequest(http.MethodPost, "/hooks/gitlab", strings.NewReader(gitlabPipeline("7")))
			request.Header = gitlabHeaders(test.token, "delivery-"+test.wantSource)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			commands := ingester.directCommands()
			if response.Code != http.StatusAccepted || len(commands) != 1 {
				t.Fatalf("status/commands = %d/%d", response.Code, len(commands))
			}
			if got := commands[0].CanonicalEvent; got.Source != test.wantSource || got.Repository != test.wantRepository {
				t.Fatalf("canonical event = %#v", got)
			}
		})
	}
}

func TestWebhookCapturesPrivateForwardEnvelopeWithoutChangingSafeHeaders(t *testing.T) {
	// Break caught: discarding the original Host/query or persisting only adapter-selected safe headers for forwarding.
	body := gitlabPipeline("7")
	ingester := &recordingIngester{result: domain.IngestResult{EventID: "event"}}
	handler := providerMux("gitlab", publichook.New("gitlab", forwardWebhookManager(t), ingester, time.Now))
	request := httptest.NewRequest(http.MethodPost, "https://hookfly.example.invalid/hooks/gitlab?raw=a%2Bb&raw=second", strings.NewReader(body))
	request.Header = gitlabHeaders("gitlab-token", "delivery")
	request.Header.Add("X-Repeated", "first")
	request.Header.Add("X-Repeated", "second")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	commands := ingester.directCommands()
	if response.Code != http.StatusAccepted || len(commands) != 1 {
		t.Fatalf("status/commands = %d/%d", response.Code, len(commands))
	}
	var envelope struct {
		Version  int         `json:"version"`
		Method   string      `json:"method"`
		Host     string      `json:"host"`
		RawQuery string      `json:"raw_query"`
		Headers  http.Header `json:"headers"`
	}
	if err := json.Unmarshal(commands[0].RequestJSON, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Version != 1 || envelope.Method != http.MethodPost || envelope.Host != "hookfly.example.invalid" || envelope.RawQuery != "raw=a%2Bb&raw=second" {
		t.Fatalf("forward envelope = %#v", envelope)
	}
	if envelope.Headers.Get("X-Gitlab-Token") != "gitlab-token" || !reflect.DeepEqual(envelope.Headers.Values("X-Repeated"), []string{"first", "second"}) {
		t.Fatalf("forward headers = %#v", envelope.Headers)
	}
	if strings.Contains(string(commands[0].HeadersJSON), "gitlab-token") {
		t.Fatalf("safe headers exposed token: %s", commands[0].HeadersJSON)
	}
}

func TestWebhookMissingTypedDependenciesReturnServiceUnavailable(t *testing.T) {
	var missingStore *store.Store
	for _, test := range []struct {
		name     string
		manager  *runtimecfg.Manager
		ingester publichook.Ingester
	}{
		{name: "manager", manager: nil, ingester: &recordingIngester{}},
		{name: "store", manager: webhookManager(t), ingester: missingStore},
	} {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("ServeHTTP() panicked with missing %s: %v", test.name, recovered)
				}
			}()
			handler := providerMux("gitlab", publichook.New("gitlab", test.manager, test.ingester, time.Now))
			request := httptest.NewRequest(http.MethodPost, "/hooks/gitlab", strings.NewReader(gitlabPipeline("7")))
			request.Header = gitlabHeaders("gitlab-token", "missing-dependency")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
			}
		})
	}
}

func TestWebhookIngesterPanicReleasesManagerReadView(t *testing.T) {
	manager := webhookManager(t)
	handler := providerMux("gitlab", publichook.New("gitlab", manager, panicIngester{}, time.Now))
	request := httptest.NewRequest(http.MethodPost, "/hooks/gitlab", strings.NewReader(gitlabPipeline("7")))
	request.Header = gitlabHeaders("gitlab-token", "panic-delivery")
	recovered := func() (recovered any) {
		defer func() { recovered = recover() }()
		handler.ServeHTTP(httptest.NewRecorder(), request)
		return nil
	}()
	if recovered != "ingest panic" {
		t.Fatalf("ServeHTTP() recovered = %v, want ingest panic", recovered)
	}

	done := make(chan struct{})
	go func() {
		manager.SetDraining()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("manager writer remained blocked after ingester panic")
	}
}

func TestWebhookKeepsManagerReadViewUntilPersistenceReturns(t *testing.T) {
	manager := webhookManager(t)
	ingester := &blockingIngester{started: make(chan struct{}), release: make(chan struct{})}
	handler := providerMux("gitlab", publichook.New("gitlab", manager, ingester, time.Now))
	request := httptest.NewRequest(http.MethodPost, "/hooks/gitlab", strings.NewReader(gitlabPipeline("7")))
	request.Header = gitlabHeaders("gitlab-token", "blocking-delivery")
	response := httptest.NewRecorder()

	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(ingester.release) }) }
	defer release()
	requestDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(requestDone)
	}()
	select {
	case <-ingester.started:
	case <-time.After(time.Second):
		t.Fatal("ingester was not called")
	}

	writerStarted := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		close(writerStarted)
		manager.SetDraining()
		close(writerDone)
	}()
	<-writerStarted
	select {
	case <-writerDone:
		t.Fatal("manager writer completed before persistence returned")
	case <-time.After(100 * time.Millisecond):
	}

	release()
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("webhook did not finish after persistence returned")
	}
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("manager writer remained blocked after persistence returned")
	}
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusAccepted)
	}
}

func TestConcurrentDirectoryPublicationNeverMixesGenerations(t *testing.T) {
	// Break caught: resolving each resource path again while a same-name directory is
	// atomically replaced can publish source, route, and target fields from different generations.
	ctx := context.Background()
	root := t.TempDir()
	liveRoot := filepath.Join(root, "live")
	referenceBRoot := filepath.Join(root, "reference-b")
	writePublicationGeneration(t, liveRoot, "a")
	writePublicationGeneration(t, referenceBRoot, "b")
	generationADirectory := filepath.Join(liveRoot, "generation-a")
	if err := os.Rename(filepath.Join(liveRoot, "conf.d"), generationADirectory); err != nil {
		t.Fatal(err)
	}
	writePublicationResources(t, filepath.Join(liveRoot, "generation-b"), "b")
	if err := os.Symlink("generation-a", filepath.Join(liveRoot, "conf.d")); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(liveRoot, "hookfly.yaml")
	logger := discardPublicationLogger()
	generationA, err := runtimecfg.Load(globalPath, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	generationB, err := runtimecfg.Load(filepath.Join(referenceBRoot, "hookfly.yaml"), nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	if generationA.Digest() == generationB.Digest() {
		t.Fatal("A and B have the same digest")
	}

	durable, err := store.Open(ctx, filepath.Join(root, "hookfly.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer durable.Close()
	manager := runtimecfg.NewManager(generationA, durable, runtimecfg.Options{Path: globalPath, Logger: logger})
	ingester := &directOnlyIngester{}
	gitlabHandler := providerMux("gitlab", publichook.New("gitlab", manager, ingester, time.Now))
	githubHandler := providerMux("github", publichook.New("github", manager, ingester, time.Now))
	knownDigests := map[string]bool{generationA.Digest(): true, generationB.Digest(): true}

	liveDirectory := filepath.Join(liveRoot, "conf.d")
	stop := make(chan struct{})
	var requestWorkers sync.WaitGroup
	for _, request := range publicationRequests(gitlabHandler, githubHandler) {
		request := request
		requestWorkers.Add(1)
		go func() {
			defer requestWorkers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					request()
				}
			}
		}()
	}

	swapDone := make(chan struct{})
	swapErrors := make(chan error, 1)
	go func() {
		defer close(swapDone)
		marker := "b"
		for {
			select {
			case <-stop:
				return
			default:
			}
			next := filepath.Join(liveRoot, "next-conf.d")
			if err := os.Symlink("generation-"+marker, next); err != nil {
				swapErrors <- fmt.Errorf("create next configuration symlink: %w", err)
				return
			}
			if err := os.Rename(next, liveDirectory); err != nil {
				swapErrors <- fmt.Errorf("publish next configuration symlink: %w", err)
				return
			}
			if marker == "a" {
				marker = "b"
			} else {
				marker = "a"
			}
			time.Sleep(100 * time.Microsecond)
		}
	}()
	var stopOnce sync.Once
	stopWorkers := func() {
		stopOnce.Do(func() { close(stop) })
		<-swapDone
		requestWorkers.Wait()
	}
	defer stopWorkers()

	for range 120 {
		outcome, reloadErr := manager.Reload(ctx, "publication-stress")
		switch {
		case reloadErr == nil && outcome == runtimecfg.ReloadApplied:
			current := manager.Current()
			if digest := current.Digest(); !knownDigests[digest] {
				t.Fatalf("published mixed generation digest %q: %s", digest, describePublicationGeneration(t, current))
			}
			if idle, idleErr := manager.SetIdleIfQueueEmpty(ctx); idleErr != nil || !idle {
				t.Fatalf("SetIdleIfQueueEmpty() = %v, %v", idle, idleErr)
			}
		case errors.Is(reloadErr, runtimecfg.ErrInvalidConfig), errors.Is(reloadErr, runtimecfg.ErrPreWaitUnavailable):
			// An intentionally unstable discovery is a rejected candidate.
		default:
			t.Fatalf("Reload() = %q, %v", outcome, reloadErr)
		}
	}
	stopWorkers()
	select {
	case swapErr := <-swapErrors:
		t.Fatal(swapErr)
	default:
	}

	commands := ingester.directCommands()
	if len(commands) == 0 {
		t.Fatal("no webhook command was accepted during publication stress")
	}
	// Breaks caught: a swap worker terminating silently, or reload/auth never making one
	// complete generation observable to accepted requests. A and B are explicit fixture
	// expectations, independent of the commands that happened to be recorded.
	seenAcceptedGeneration := map[string]bool{"a": false, "b": false}
	// These literals were independently derived from the provider/source/delivery framing.
	expectedDeliveryIDs := map[string]string{
		"gitlab/a": "ceae04a426aa8e54c4169a9ea1954258e8b8cccfa0ffa6d7b3723494b2590553",
		"gitlab/b": "9e791010c3b895b70d9f52472c15624c8f4a849872d3d44d3bdf8307c8b9f073",
		"github/a": "6c4dea6dd5681d9544c4936fdfd202aaa2c90033b42e9ae51053066dee124a86",
		"github/b": "3abfb285a165247079727db720fffc03b6349b4440012907e8b6a5decbf9856b",
	}
	for _, command := range commands {
		var wantMarker string
		switch command.ConfigDigest {
		case generationA.Digest():
			wantMarker = "a"
		case generationB.Digest():
			wantMarker = "b"
		default:
			t.Fatalf("accepted mixed generation digest %q", command.ConfigDigest)
		}
		var wantRule string
		switch command.CanonicalEvent.Provider {
		case "gitlab":
			wantRule = "route-" + wantMarker
		case "github":
			wantRule = "github-route-" + wantMarker
		default:
			t.Fatalf("accepted command has unknown provider %q", command.CanonicalEvent.Provider)
		}
		wantDeliveryID := expectedDeliveryIDs[command.CanonicalEvent.Provider+"/"+wantMarker]
		if command.DeliveryID != wantDeliveryID {
			t.Fatalf("accepted %s command has source/auth delivery ID %q, want %q", wantMarker, command.DeliveryID, wantDeliveryID)
		}
		if command.RuleID != wantRule || len(command.Deliveries) != 1 {
			t.Fatalf("accepted %s command has mixed route/deliveries: %#v", wantMarker, command)
		}
		var target struct {
			ResourceID string `json:"resource_id"`
			Connection struct {
				BaseURL string `json:"base_url"`
			} `json:"connection"`
		}
		if err := json.Unmarshal(command.Deliveries[0].TargetSnapshot, &target); err != nil {
			t.Fatal(err)
		}
		if target.ResourceID != "compose-"+wantMarker || target.Connection.BaseURL != "https://"+wantMarker+".example.invalid" {
			t.Fatalf("accepted %s command has mixed target snapshot: %s", wantMarker, command.Deliveries[0].TargetSnapshot)
		}
		if !strings.Contains(string(command.RuleSnapshot), `"id":"`+wantRule+`"`) {
			t.Fatalf("accepted %s command has mixed rule snapshot: %s", wantMarker, command.RuleSnapshot)
		}
		seenAcceptedGeneration[wantMarker] = true
	}
	if !seenAcceptedGeneration["a"] || !seenAcceptedGeneration["b"] {
		t.Fatalf("accepted generation coverage = A:%t B:%t, want A:true B:true", seenAcceptedGeneration["a"], seenAcceptedGeneration["b"])
	}
}

func TestWebhookBodyLimitAcceptsExactly2MiBAndRejectsNextByte(t *testing.T) {
	const twoMiB = 2 * 1024 * 1024
	manager := webhookManager(t)
	providers := []struct {
		name, provider, source string
		headers                func(string) http.Header
	}{
		{name: "GitLab", provider: "gitlab", source: "gitlab-primary", headers: func(string) http.Header {
			return gitlabUnsupportedHeaders("gitlab-token")
		}},
		{name: "GitHub", provider: "github", source: "github-primary", headers: func(body string) http.Header {
			return githubEventHeaders("github-secret", "github-boundary", "custom_event", body)
		}},
	}
	boundaries := []struct {
		name       string
		bodyBytes  int
		wantStatus int
		wantCalls  int
	}{
		{name: "exactly 2 MiB", bodyBytes: twoMiB, wantStatus: http.StatusAccepted, wantCalls: 1},
		{name: "2 MiB plus 1 byte", bodyBytes: twoMiB + 1, wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, provider := range providers {
		for _, boundary := range boundaries {
			t.Run(provider.name+"/"+boundary.name, func(t *testing.T) {
				const wrapper = `{"padding":""}`
				body := `{"padding":"` + strings.Repeat("x", boundary.bodyBytes-len(wrapper)) + `"}`
				ingester := &recordingIngester{result: domain.IngestResult{EventID: "boundary"}}
				handler := providerMux(provider.provider, publichook.New(provider.provider, manager, ingester, time.Now))
				request := httptest.NewRequest(http.MethodPost, providerWebhookPath(provider.provider, provider.source), strings.NewReader(body))
				request.Header = provider.headers(body)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != boundary.wantStatus || len(ingester.directCommands()) != boundary.wantCalls || len(ingester.deferredCommands()) != 0 {
					t.Fatalf("status/direct/deferred = %d/%d/%d, want %d/%d/0", response.Code, len(ingester.directCommands()), len(ingester.deferredCommands()), boundary.wantStatus, boundary.wantCalls)
				}
			})
		}
	}
}

func TestDeliveryIdentityIsNamespacedAndHasStableFallback(t *testing.T) {
	manager := webhookManager(t)
	post := func(providerName, source, body string, headers http.Header) string {
		ingester := &recordingIngester{result: domain.IngestResult{EventID: "event"}}
		handler := providerMux(providerName, publichook.New(providerName, manager, ingester, time.Now))
		request := httptest.NewRequest(http.MethodPost, providerWebhookPath(providerName, source), strings.NewReader(body))
		request.Header = headers
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusAccepted || len(ingester.directCommands()) != 1 {
			t.Fatalf("post = %d, %#v", response.Code, ingester.directCommands())
		}
		return ingester.directCommands()[0].DeliveryID
	}
	gitlabBody := gitlabPipeline("7")
	gitlabID := post("gitlab", "gitlab-primary", gitlabBody, gitlabHeaders("gitlab-token", "shared"))
	githubBody := githubWorkflow("101")
	githubID := post("github", "github-primary", githubBody, githubHeaders("github-secret", "shared", githubBody))
	if gitlabID == githubID {
		t.Fatal("provider namespaces produced the same delivery identity")
	}
	fallbackHeaders := gitlabHeaders("gitlab-token", "")
	first := post("gitlab", "gitlab-primary", gitlabBody, fallbackHeaders)
	second := post("gitlab", "gitlab-primary", gitlabBody, fallbackHeaders)
	if first == "" || first != second || first == gitlabID {
		t.Fatalf("fallback identities = %q, %q; delivered=%q", first, second, gitlabID)
	}
}

func TestFallbackDeliveryIdentitySurvivesLogicalRepositoryRemap(t *testing.T) {
	body := gitlabPipeline("7")
	post := func(manager *runtimecfg.Manager) string {
		ingester := &recordingIngester{result: domain.IngestResult{EventID: "event"}}
		handler := providerMux("gitlab", publichook.New("gitlab", manager, ingester, time.Now))
		request := httptest.NewRequest(http.MethodPost, "/hooks/gitlab", strings.NewReader(body))
		request.Header = gitlabHeaders("gitlab-token", "")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		commands := ingester.directCommands()
		if response.Code != http.StatusAccepted || len(commands) != 1 {
			t.Fatalf("post = %d, %#v", response.Code, commands)
		}
		return commands[0].DeliveryID
	}

	before := post(gitlabRepositoryManager(t, "application"))
	after := post(gitlabRepositoryManager(t, "renamed-application"))
	if before == "" || before != after {
		t.Fatalf("fallback identity changed across logical remap: %q != %q", before, after)
	}
}

func TestGitHubTagPushRoutesToTagRule(t *testing.T) {
	body := `{"repository":{"id":101},"ref":"refs/tags/v1.2.3","after":"abc123"}`
	ingester := &recordingIngester{result: domain.IngestResult{EventID: "tag-event"}}
	handler := providerMux("github", publichook.New("github", webhookManager(t), ingester, time.Now))
	request := httptest.NewRequest(http.MethodPost, "/hooks/github/github-primary", strings.NewReader(body))
	request.Header = githubEventHeaders("github-secret", "github-tag", "push", body)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	commands := ingester.directCommands()
	if response.Code != http.StatusAccepted || len(commands) != 1 {
		t.Fatalf("tag webhook = %d, %#v", response.Code, commands)
	}
	command := commands[0]
	wantEvent := domain.CanonicalEvent{
		Provider: "github", Source: "github-primary", Repository: "application",
		Event: "tag_push", Ref: "v1.2.3", Revision: "abc123",
	}
	if command.CanonicalEvent != wantEvent || command.RoutingResult != domain.RoutingDeploy || command.RuleID != "deploy-github-tag" ||
		len(command.Deliveries) != 1 || command.Deliveries[0].TargetID != "production" {
		t.Fatalf("tag command = %#v", command)
	}
}

func TestSupportedWebhookDefersDuringReloadButUnsupportedDoesNot(t *testing.T) {
	providers := []struct {
		name, provider, source         string
		supportedBody, unsupportedBody string
		supportedHeaders               http.Header
		unsupportedHeaders             http.Header
	}{
		{
			name: "GitLab", provider: "gitlab", source: "gitlab-primary",
			supportedBody: gitlabPipeline("7"), unsupportedBody: `{"project_id":7}`,
			supportedHeaders: gitlabHeaders("gitlab-token", "gitlab-deferred"), unsupportedHeaders: gitlabUnsupportedHeaders("gitlab-token"),
		},
		{
			name: "GitHub", provider: "github", source: "github-primary",
			supportedBody: githubWorkflow("101"), unsupportedBody: `{"repository":{"id":101}}`,
			supportedHeaders:   githubHeaders("github-secret", "github-deferred", githubWorkflow("101")),
			unsupportedHeaders: githubEventHeaders("github-secret", "github-unsupported-deferred", "custom_event", `{"repository":{"id":101}}`),
		},
	}
	states := []struct {
		name    string
		manager func(*testing.T) *runtimecfg.Manager
	}{
		{
			name: "Draining",
			manager: func(t *testing.T) *runtimecfg.Manager {
				manager := webhookManager(t)
				manager.SetDraining()
				return manager
			},
		},
		{name: "Waiting", manager: waitingWebhookManager},
	}
	for _, state := range states {
		t.Run(state.name, func(t *testing.T) {
			for _, provider := range providers {
				t.Run(provider.name, func(t *testing.T) {
					manager := state.manager(t)
					ingester := &recordingIngester{result: domain.IngestResult{EventID: "event"}}
					handler := providerMux(provider.provider, publichook.New(provider.provider, manager, ingester, time.Now))
					post := func(body string, headers http.Header) int {
						request := httptest.NewRequest(http.MethodPost, providerWebhookPath(provider.provider, provider.source), strings.NewReader(body))
						request.Header = headers
						response := httptest.NewRecorder()
						handler.ServeHTTP(response, request)
						return response.Code
					}
					if status := post(provider.supportedBody, provider.supportedHeaders); status != http.StatusAccepted {
						t.Fatalf("supported status = %d", status)
					}
					if status := post(provider.unsupportedBody, provider.unsupportedHeaders); status != http.StatusAccepted {
						t.Fatalf("unsupported status = %d", status)
					}
					deferred := ingester.deferredCommands()
					direct := ingester.directCommands()
					if len(deferred) != 1 || deferred[0].RoutingResult != domain.RoutingDeferred || len(deferred[0].Deliveries) != 0 ||
						len(direct) != 1 || direct[0].RoutingResult != domain.RoutingUnsupported || len(direct[0].Deliveries) != 0 {
						t.Fatalf("direct/deferred = %#v/%#v", direct, deferred)
					}
					if len(deferred[0].RequestJSON) == 0 || len(direct[0].RequestJSON) != 0 {
						t.Fatalf("private request retention = deferred:%d direct:%d", len(deferred[0].RequestJSON), len(direct[0].RequestJSON))
					}
				})
			}
		})
	}
}

type recordingIngester struct {
	mu       sync.Mutex
	direct   []domain.IngestCommand
	deferred []domain.IngestCommand
	result   domain.IngestResult
	err      error
}

type directOnlyIngester struct {
	recordingIngester
}

func (*directOnlyIngester) IngestDeferred(context.Context, domain.IngestCommand) (domain.IngestResult, error) {
	return domain.IngestResult{}, errors.New("deferred command intentionally rejected by publication stress test")
}

type panicIngester struct{}

func (panicIngester) Ingest(context.Context, domain.IngestCommand) (domain.IngestResult, error) {
	panic("ingest panic")
}

func (panicIngester) IngestDeferred(context.Context, domain.IngestCommand) (domain.IngestResult, error) {
	panic("ingest panic")
}

type blockingIngester struct {
	started chan struct{}
	release chan struct{}
}

func (i *blockingIngester) Ingest(context.Context, domain.IngestCommand) (domain.IngestResult, error) {
	close(i.started)
	<-i.release
	return domain.IngestResult{EventID: "blocking-event"}, nil
}

func (i *blockingIngester) IngestDeferred(context.Context, domain.IngestCommand) (domain.IngestResult, error) {
	return domain.IngestResult{}, errors.New("unexpected deferred ingest")
}

func (i *recordingIngester) Ingest(_ context.Context, command domain.IngestCommand) (domain.IngestResult, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.direct = append(i.direct, command)
	return i.result, i.err
}

func (i *recordingIngester) IngestDeferred(_ context.Context, command domain.IngestCommand) (domain.IngestResult, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.deferred = append(i.deferred, command)
	return i.result, i.err
}

func (i *recordingIngester) directCommands() []domain.IngestCommand {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]domain.IngestCommand(nil), i.direct...)
}

func (i *recordingIngester) deferredCommands() []domain.IngestCommand {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]domain.IngestCommand(nil), i.deferred...)
}

func webhookManager(t *testing.T) *runtimecfg.Manager {
	t.Helper()
	priorityOne, priorityTwo, priorityThree, priorityFour := 1, 2, 3, 4
	bundle := &config.Bundle{
		Global:             config.Global{Kind: "Hookfly"},
		GitLabSources:      []config.GitLabSource{{ID: "gitlab-primary", Token: "gitlab-token", Repositories: []config.Repository{{ID: "application", Name: "Application", ExternalID: "7"}}}},
		GitHubSources:      []config.GitHubSource{{ID: "github-primary", Secret: "github-secret", Repositories: []config.Repository{{ID: "application", Name: "Application", ExternalID: "101"}}}},
		HarborSources:      []config.HarborSource{{ID: "harbor-primary", Authorization: "Bearer harbor-secret", Repositories: []config.Repository{{ID: "application-image", Name: "Application Image", ExternalID: "team/application"}}}},
		DokployConnections: []config.DokployConnection{{ID: "primary", BaseURL: "https://dokploy.example.invalid", APIKey: "dokploy-key"}},
		Targets:            []config.Target{{ID: "production", Type: "dokploy", Connection: "primary", ResourceType: "compose", ResourceID: "compose-production"}},
		Routes: []config.Route{
			{ID: "deploy-gitlab", Priority: &priorityOne, Match: config.RouteMatch{Source: "gitlab-primary", Repository: "application", Event: "pipeline"}, Action: config.Action{Type: "deploy", Targets: []string{"production"}}},
			{ID: "deploy-github", Priority: &priorityTwo, Match: config.RouteMatch{Source: "github-primary", Repository: "application", Event: "pipeline"}, Action: config.Action{Type: "deploy", Targets: []string{"production"}}},
			{ID: "deploy-github-tag", Priority: &priorityThree, Match: config.RouteMatch{Source: "github-primary", Repository: "application", Event: "tag_push"}, Action: config.Action{Type: "deploy", Targets: []string{"production"}}},
			{ID: "deploy-harbor", Priority: &priorityFour, Match: config.RouteMatch{Source: "harbor-primary", Repository: "application-image", Event: "artifact_push"}, Action: config.Action{Type: "deploy", Targets: []string{"production"}}},
		},
	}
	generation, err := runtimecfg.Compile(bundle, nil)
	if err != nil {
		t.Fatal(err)
	}
	return runtimecfg.NewManager(generation, nil, runtimecfg.Options{})
}

func forwardWebhookManager(t *testing.T) *runtimecfg.Manager {
	t.Helper()
	priority := 1
	generation, err := runtimecfg.Compile(&config.Bundle{
		Global:        config.Global{Kind: "Hookfly"},
		GitLabSources: []config.GitLabSource{{ID: "gitlab-primary", Token: "gitlab-token", Repositories: []config.Repository{{ID: "application", ExternalID: "7"}}}},
		Targets:       []config.Target{{ID: "downstream", Type: "forward", URL: "https://receiver.example.invalid/hooks/gitlab"}},
		Routes: []config.Route{{
			ID: "forward-gitlab", Priority: &priority,
			Match:  config.RouteMatch{Source: "gitlab-primary", Repository: "application", Event: "pipeline"},
			Action: config.Action{Type: "deploy", Targets: []string{"downstream"}},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return runtimecfg.NewManager(generation, nil, runtimecfg.Options{})
}

func waitingWebhookManager(t *testing.T) *runtimecfg.Manager {
	t.Helper()
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hookfly.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = durable.Close() })
	active := domain.IngestCommand{
		DeliveryID: "active-delivery",
		CanonicalEvent: domain.CanonicalEvent{
			Provider: "github", Source: "lifecycle", Event: "push",
		},
		ReceivedAt: time.UnixMilli(1), RoutingResult: domain.RoutingDeploy,
		Deliveries: []domain.NewDelivery{{TargetID: "production", TargetSnapshot: []byte(`{"binding_version":2}`)}},
	}
	if _, err := durable.Ingest(context.Background(), active); err != nil {
		t.Fatal(err)
	}
	deferred := active
	deferred.DeliveryID = "deferred-delivery"
	deferred.RoutingResult = domain.RoutingDeferred
	deferred.Deliveries = nil
	if _, err := durable.IngestDeferred(context.Background(), deferred); err != nil {
		t.Fatal(err)
	}
	manager := runtimecfg.NewManager(webhookManager(t).Current(), durable, runtimecfg.Options{})
	if err := manager.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.Read()
	defer view.Unlock()
	if view.State != runtimecfg.StateWaiting {
		t.Fatalf("manager state = %q, want %q", view.State, runtimecfg.StateWaiting)
	}
	return manager
}

func gitlabRepositoryManager(t *testing.T, repositoryID string) *runtimecfg.Manager {
	t.Helper()
	generation, err := runtimecfg.Compile(&config.Bundle{
		Global: config.Global{Kind: "Hookfly"},
		GitLabSources: []config.GitLabSource{{
			ID: "gitlab-primary", Token: "gitlab-token",
			Repositories: []config.Repository{{ID: repositoryID, Name: "Application", ExternalID: "7"}},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return runtimecfg.NewManager(generation, nil, runtimecfg.Options{})
}

func providerMux(providerName string, handler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST "+providerWebhookPath(providerName, "{source_id}"), handler)
	return mux
}

func providerWebhookPath(providerName, sourceID string) string {
	if providerName == "gitlab" {
		return "/hooks/gitlab"
	}
	return "/hooks/" + providerName + "/" + sourceID
}

func gitlabPipeline(repositoryID string) string {
	return `{"project":{"id":` + repositoryID + `},"object_attributes":{"id":88,"ref":"main","status":"success","source":"push","sha":"abc123"}}`
}

func githubWorkflow(repositoryID string) string {
	return `{"repository":{"id":` + repositoryID + `},"workflow_run":{"id":91,"head_branch":"main","conclusion":"success","head_sha":"abc123","event":"push"}}`
}

func gitlabHeaders(token, deliveryID string) http.Header {
	headers := make(http.Header)
	headers.Set("X-Gitlab-Event", "Pipeline Hook")
	headers.Set("X-Gitlab-Token", token)
	if deliveryID != "" {
		headers.Set("X-Gitlab-Event-UUID", deliveryID)
	}
	return headers
}

func gitlabUnsupportedHeaders(token string) http.Header {
	headers := gitlabHeaders(token, "unsupported")
	headers.Set("X-Gitlab-Event", "Deployment Hook")
	return headers
}

func githubHeaders(secret, deliveryID, body string) http.Header {
	return githubEventHeaders(secret, deliveryID, "workflow_run", body)
}

func githubEventHeaders(secret, deliveryID, event, body string) http.Header {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	headers := make(http.Header)
	headers.Set("X-GitHub-Event", event)
	headers.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	if deliveryID != "" {
		headers.Set("X-GitHub-Delivery", deliveryID)
	}
	return headers
}

func decodeAccepted(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func writePublicationGeneration(t *testing.T, root, marker string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	global := "kind: Hookfly\nconfig_dir: conf.d\nhistory: {}\npolling: {}\n"
	if err := os.WriteFile(filepath.Join(root, "hookfly.yaml"), []byte(global), 0o600); err != nil {
		t.Fatal(err)
	}
	writePublicationResources(t, filepath.Join(root, "conf.d"), marker)
}

func writePublicationResources(t *testing.T, directory, marker string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"000-gitlab.yaml":  "kind: GitLabSources\nsources:\n  - id: gitlab-primary\n    token: gitlab-token-" + marker + "\n    repositories:\n      - id: application\n        project_id: 7\n",
		"010-github.yaml":  "kind: GitHubSources\nsources:\n  - id: github-primary\n    secret: github-secret-" + marker + "\n    repositories:\n      - id: application\n        repository_id: 101\n",
		"900-targets.yaml": "kind: DokployTargets\nconnections:\n  - id: primary\n    base_url: https://" + marker + ".example.invalid\n    api_key: dokploy-key-" + marker + "\ntargets:\n  - id: production\n    type: dokploy\n    connection: primary\n    resource_type: compose\n    resource_id: compose-" + marker + "\n",
		"950-routes.yaml":  "kind: Routes\nroutes:\n  - id: route-" + marker + "\n    priority: 1\n    match:\n      source: gitlab-primary\n      repository: application\n      event: pipeline\n    action:\n      type: deploy\n      targets: [production]\n  - id: github-route-" + marker + "\n    priority: 2\n    match:\n      source: github-primary\n      repository: application\n      event: pipeline\n    action:\n      type: deploy\n      targets: [production]\n",
	}
	padding := "kind: Routes\nroutes: []\n# " + strings.Repeat("publication-padding-", 4096) + "\n"
	for index := range 24 {
		files[fmt.Sprintf("%03d-padding.yaml", index+100)] = padding
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func publicationRequests(gitlabHandler, githubHandler http.Handler) []func() {
	gitlabBody := gitlabPipeline("7")
	githubBody := githubWorkflow("101")
	requests := []func(){}
	for _, marker := range []string{"a", "b"} {
		marker := marker
		requests = append(requests, func() {
			request := httptest.NewRequest(http.MethodPost, "/hooks/gitlab", strings.NewReader(gitlabBody))
			request.Header = gitlabHeaders("gitlab-token-"+marker, "gitlab-"+marker+"-delivery")
			gitlabHandler.ServeHTTP(httptest.NewRecorder(), request)
		}, func() {
			request := httptest.NewRequest(http.MethodPost, "/hooks/github/github-primary", strings.NewReader(githubBody))
			request.Header = githubHeaders("github-secret-"+marker, "github-"+marker+"-delivery", githubBody)
			githubHandler.ServeHTTP(httptest.NewRecorder(), request)
		})
	}
	return requests
}

func discardPublicationLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func describePublicationGeneration(t *testing.T, generation *runtimecfg.Generation) string {
	t.Helper()
	markers := make([]string, 0, 5)
	for _, providerName := range []string{"gitlab", "github"} {
		source, found := generation.Source(providerName, providerName+"-primary")
		if !found {
			markers = append(markers, providerName+"-missing")
			continue
		}
		for _, marker := range []string{"a", "b"} {
			body := []byte(gitlabPipeline("7"))
			headers := gitlabHeaders("gitlab-token-"+marker, "diagnostic")
			if providerName == "github" {
				body = []byte(githubWorkflow("101"))
				headers = githubHeaders("github-secret-"+marker, "diagnostic", string(body))
			}
			if source.Authenticate(headers, body) {
				markers = append(markers, providerName+"-"+marker)
				event, _, err := source.Normalize(headers, body)
				if err != nil {
					t.Fatal(err)
				}
				decision, err := generation.Route(event)
				if err != nil {
					t.Fatal(err)
				}
				markers = append(markers, "rule-"+decision.RuleID)
			}
		}
	}
	target, found := generation.Target("production")
	if found {
		markers = append(markers, "target-"+target.ComposeID)
	}
	return strings.Join(markers, ",")
}
