package dokploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testAPIKey = "test-dokploy-key-do-not-log"

func TestDiscoverComposeResourcesUsesProjectAllAndReturnsStableSafeSubset(t *testing.T) {
	// Break caught: querying an unverified endpoint, forwarding raw Dokploy objects, or returning unstable resource order.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/project.all" || r.URL.RawQuery != "" {
			t.Errorf("request = %s %s", r.Method, r.URL.RequestURI())
		}
		if got := r.Header.Get("x-api-key"); got != testAPIKey {
			t.Errorf("x-api-key = %q", got)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `[
			{"projectId":"project-z","name":"Zulu","env":"project-secret","environments":[
				{"environmentId":"environment-z","name":"Production","env":"environment-secret","compose":[
					{"composeId":"compose-b","name":"Worker","appName":"worker-xyz","composeStatus":"done","env":"compose-secret"},
					{"composeId":"compose-a","name":"API","appName":"api-xyz","composeStatus":"running"}
				]}
			]},
			{"projectId":"project-a","name":"Alpha","environments":[
				{"environmentId":"environment-a","name":"Development","compose":[
					{"composeId":"compose-z","name":"Console","appName":null,"composeStatus":null,"apiKey":"raw-secret"}
				]}
			]}
		]`)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, http.DefaultClient, io.Discard)
	resources, err := client.DiscoverComposeResources(context.Background())
	if err != nil {
		t.Fatalf("DiscoverComposeResources() error = %v", err)
	}
	want := []ComposeResource{
		{ProjectName: "Alpha", EnvironmentName: "Development", Name: "Console", ResourceID: "compose-z"},
		{ProjectName: "Zulu", EnvironmentName: "Production", Name: "API", AppName: "api-xyz", ResourceID: "compose-a", Status: "running"},
		{ProjectName: "Zulu", EnvironmentName: "Production", Name: "Worker", AppName: "worker-xyz", ResourceID: "compose-b", Status: "done"},
	}
	if !composeResourcesEqual(resources, want) {
		t.Fatalf("resources = %#v, want %#v", resources, want)
	}
	encoded, err := json.Marshal(resources)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"project-secret", "environment-secret", "compose-secret", "raw-secret", testAPIKey, "base_url", "apiKey", "env"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("safe resources leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestDiscoverComposeResourcesReturnsAnEmptyNonNilList(t *testing.T) {
	// Break caught: treating an installed Dokploy with no Projects as malformed or emitting JSON null to the API.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[]`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, http.DefaultClient, io.Discard)
	resources, err := client.DiscoverComposeResources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resources == nil || len(resources) != 0 {
		t.Fatalf("resources = %#v, want empty non-nil list", resources)
	}
}

func TestDiscoverComposeContainersReturnsOnlySafeRuntimeFacts(t *testing.T) {
	// Break caught: matching Compose containers without the official label filter or leaking Docker config fields.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/docker.getContainersByAppNameMatch":
			if r.URL.Query().Get("appName") != "api-abc" || r.URL.Query().Get("appType") != "docker-compose" {
				t.Errorf("container query = %#v", r.URL.Query())
			}
			_, _ = io.WriteString(w, `[{"containerId":"container-1","name":"fixed-api","state":"running","status":"Up 1 hour (healthy)","image":"must-not-return"}]`)
		case "/api/docker.getConfig":
			if r.URL.Query().Get("containerId") != "container-1" {
				t.Errorf("config query = %#v", r.URL.Query())
			}
			_, _ = io.WriteString(w, `{"Name":"/fixed-api","State":{"Status":"running","Health":{"Status":"healthy"}},"RestartCount":2,"Config":{"Env":["TOKEN=must-not-return"]},"Mounts":[{"Source":"/secret"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, http.DefaultClient, io.Discard)
	containers, err := client.DiscoverComposeContainers(context.Background(), "api-abc")
	if err != nil {
		t.Fatal(err)
	}
	want := []ComposeContainer{{Name: "fixed-api", State: "running", Health: "healthy", RestartCount: 2}}
	if !reflect.DeepEqual(containers, want) {
		t.Fatalf("containers = %#v, want %#v", containers, want)
	}
	encoded, _ := json.Marshal(containers)
	for _, forbidden := range []string{"container-1", "must-not-return", "TOKEN", "Mounts", "/secret", testAPIKey} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("safe containers leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestDiscoverComposeResourcesRejectsUnsafeUpstreamResponses(t *testing.T) {
	// Break caught: accepting unbounded, non-JSON, malformed, trailing, or identity-less upstream data and exposing response content.
	tests := []struct {
		name        string
		status      int
		contentType string
		body        []byte
	}{
		{name: "non-2xx", status: http.StatusInternalServerError, contentType: "application/json", body: []byte(`{"message":"` + testAPIKey + `"}`)},
		{name: "wrong content type", status: http.StatusOK, contentType: "text/html", body: []byte(`<p>` + testAPIKey + `</p>`)},
		{name: "malformed JSON", status: http.StatusOK, contentType: "application/json", body: []byte(`[{"name":"` + testAPIKey + `"`)},
		{name: "wrong root", status: http.StatusOK, contentType: "application/json", body: []byte(`{"projects":[]}`)},
		{name: "trailing JSON", status: http.StatusOK, contentType: "application/json", body: []byte(`[] {}`)},
		{name: "missing Compose ID", status: http.StatusOK, contentType: "application/json", body: []byte(`[{"name":"Project","environments":[{"name":"Production","compose":[{"name":"API"}]}]}]`)},
		{name: "oversize", status: http.StatusOK, contentType: "application/json", body: bytes.Repeat([]byte("x"), (1<<20)+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				w.WriteHeader(test.status)
				_, _ = w.Write(test.body)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, http.DefaultClient, io.Discard)
			resources, err := client.DiscoverComposeResources(context.Background())
			if err == nil || resources != nil {
				t.Fatalf("DiscoverComposeResources() = %#v/%v, want safe error", resources, err)
			}
			for _, forbidden := range []string{testAPIKey, "<p>", "raw-secret"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("error leaked %q: %v", forbidden, err)
				}
			}
		})
	}
}

func TestDiscoverComposeResourcesHonorsCancellationAndRequestDeadlines(t *testing.T) {
	// Break caught: starting canceled discovery work or allowing a slow project inventory request to run without a deadline.
	t.Run("already canceled", func(t *testing.T) {
		var calls atomic.Int32
		transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("unexpected request")
		})
		client := newTestClient(t, "http://dokploy.invalid", &http.Client{Transport: transport}, io.Discard)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if resources, err := client.DiscoverComposeResources(ctx); !errors.Is(err, context.Canceled) || resources != nil {
			t.Fatalf("DiscoverComposeResources() = %#v/%v", resources, err)
		}
		if calls.Load() != 0 {
			t.Fatalf("transport calls = %d", calls.Load())
		}
	})

	t.Run("caller deadline", func(t *testing.T) {
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		})
		client := newTestClient(t, "http://dokploy.invalid", &http.Client{Transport: transport}, io.Discard)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if resources, err := client.DiscoverComposeResources(ctx); !errors.Is(err, context.DeadlineExceeded) || resources != nil {
			t.Fatalf("DiscoverComposeResources() = %#v/%v", resources, err)
		}
	})

	t.Run("default deadline", func(t *testing.T) {
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			deadline, ok := request.Context().Deadline()
			if !ok {
				t.Fatal("request context has no deadline")
			}
			remaining := time.Until(deadline)
			if remaining < 9*time.Second || remaining > 10*time.Second {
				t.Fatalf("request deadline remaining = %v", remaining)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`[]`)),
				Request:    request,
			}, nil
		})
		client := newTestClient(t, "http://dokploy.invalid", &http.Client{Transport: transport}, io.Discard)
		if _, err := client.DiscoverComposeResources(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDeployUsesOfficialComposeContractAndAcceptsAny2xxBody(t *testing.T) {
	// Break caught: changing the endpoint, auth header, payload fields, or parsing a version-specific success body.
	bodies := []string{
		`{"success":true,"message":"Deployment queued","composeId":"compose-123"}`,
		`true`,
		``,
	}
	for index, body := range bodies {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/compose.deploy" || r.URL.RawQuery != "" {
					t.Errorf("request = %s %s", r.Method, r.URL.RequestURI())
				}
				if got := r.Header.Get("x-api-key"); got != testAPIKey {
					t.Errorf("x-api-key = %q", got)
				}
				var got map[string]any
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Errorf("decode request: %v", err)
				}
				want := map[string]any{
					"composeId":   "compose-123",
					"title":       "Hookfly deployment",
					"description": "attempt_id=00000000-0000-0000-0000-000000000123 source=pipeline",
				}
				if !mapsEqual(got, want) {
					t.Errorf("request body = %#v, want %#v", got, want)
				}
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()

			client := newTestClient(t, server.URL, http.DefaultClient, io.Discard)
			response, err := client.Deploy(context.Background(), DeployRequest{
				ComposeID: "compose-123", Title: "Hookfly deployment",
				Description: "attempt_id=00000000-0000-0000-0000-000000000123 source=pipeline",
			})
			if err != nil {
				t.Fatalf("Deploy() error = %v", err)
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", response.StatusCode)
			}
		})
	}
}

func TestFindDeploymentUsesOfficialContractAndReturnsNewestExactAttempt(t *testing.T) {
	// Break caught: using the wrong endpoint/query/header, expecting an items envelope, or substring-matching a newer unrelated attempt.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/deployment.allByCompose" {
			t.Errorf("request = %s %s", r.Method, r.URL.RequestURI())
		}
		if got := r.URL.Query().Get("composeId"); got != "compose /?&" || len(r.URL.Query()) != 1 {
			t.Errorf("query = %#v", r.URL.Query())
		}
		if got := r.Header.Get("x-api-key"); got != testAPIKey {
			t.Errorf("x-api-key = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[
			{"deploymentId":"substring-newest","composeId":"compose /?&","description":"attempt_id=00000000-0000-0000-0000-000000000001-extra","status":"done","createdAt":"2026-08-05T10:00:01Z"},
			{"deploymentId":"exact-newer","composeId":"compose /?&","description":"source=pipeline attempt_id=00000000-0000-0000-0000-000000000001","status":"running","createdAt":"2026-08-05T10:00:00.123456789Z","ignored":{"secret":"not-read"}},
			{"deploymentId":"exact-older","composeId":"compose /?&","description":"attempt_id=00000000-0000-0000-0000-000000000001 source=pipeline","status":"done","createdAt":"2026-08-05T09:59:59Z"}
		]`)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, http.DefaultClient, io.Discard)
	deployment, found, err := client.FindDeployment(context.Background(), "compose /?&", "00000000-0000-0000-0000-000000000001", "")
	if err != nil {
		t.Fatalf("FindDeployment() error = %v", err)
	}
	if !found {
		t.Fatal("FindDeployment() did not find exact attempt")
	}
	if deployment.DeploymentID != "exact-newer" || deployment.ComposeID != "compose /?&" {
		t.Fatalf("deployment identity = %#v", deployment)
	}
	if deployment.Status == nil || *deployment.Status != "running" {
		t.Fatalf("deployment status = %#v", deployment.Status)
	}
	wantTime, err := time.Parse(time.RFC3339Nano, "2026-08-05T10:00:00.123456789Z")
	if err != nil {
		t.Fatal(err)
	}
	if !deployment.CreatedAt.Equal(wantTime) {
		t.Fatalf("createdAt = %s, want %s", deployment.CreatedAt, wantTime)
	}
}

func TestLatestDeploymentIDUsesNewestHistoryRecordAndDistinguishesEmptyHistory(t *testing.T) {
	// Break caught: starting a deployment without a durable exclusive boundary, or treating an empty history as a lookup failure.
	var body atomic.Value
	body.Store(`[
		{"deploymentId":"newest","composeId":"compose-1","description":"Commit: abc","status":"done","createdAt":"2026-08-12T03:00:01Z"},
		{"deploymentId":"older","composeId":"compose-1","description":null,"status":"error","createdAt":"2026-08-12T03:00:00Z"}
	]`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/deployment.allByCompose" || r.URL.Query().Get("composeId") != "compose-1" {
			t.Errorf("request = %s %s", r.Method, r.URL.RequestURI())
		}
		_, _ = io.WriteString(w, body.Load().(string))
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, http.DefaultClient, io.Discard)

	deploymentID, found, err := client.LatestDeploymentID(context.Background(), "compose-1")
	if err != nil || !found || deploymentID != "newest" {
		t.Fatalf("LatestDeploymentID() = %q/%v/%v", deploymentID, found, err)
	}
	body.Store(`[]`)
	deploymentID, found, err = client.LatestDeploymentID(context.Background(), "compose-1")
	if err != nil || found || deploymentID != "" {
		t.Fatalf("empty LatestDeploymentID() = %q/%v/%v", deploymentID, found, err)
	}
}

func TestFindDeploymentAfterBindsOnePostCursorRecordWithMutableDescription(t *testing.T) {
	// Break caught: requiring Dokploy to preserve Hookfly's description after Git metadata replaces it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[
			{"deploymentId":"created-after-send","composeId":"compose-1","description":"Commit: c05faf4","status":"error","createdAt":"2026-08-12T02:59:50.825Z"},
			{"deploymentId":"cursor","composeId":"compose-1","description":"Commit: older","status":"done","createdAt":"2026-08-12T02:53:11.817Z"}
		]`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, http.DefaultClient, io.Discard)

	deployment, lookup, err := client.FindDeploymentAfter(context.Background(), "compose-1", "attempt-1", "cursor")
	if err != nil || lookup != DeploymentFound || deployment.DeploymentID != "created-after-send" {
		t.Fatalf("FindDeploymentAfter() = %#v/%q/%v", deployment, lookup, err)
	}
}

func TestFindDeploymentAfterRejectsAmbiguityUnlessOneExactAttemptTokenSurvives(t *testing.T) {
	// Break caught: silently binding a manual or automatic deployment when more than one record appeared after the cursor.
	tests := []struct {
		name       string
		body       string
		wantLookup DeploymentLookup
		wantID     string
	}{
		{
			name: "ambiguous",
			body: `[
				{"deploymentId":"manual","composeId":"compose-1","description":"Commit: manual","status":"done","createdAt":"2026-08-12T03:00:02Z"},
				{"deploymentId":"hookfly","composeId":"compose-1","description":"Commit: hookfly","status":"done","createdAt":"2026-08-12T03:00:01Z"},
				{"deploymentId":"cursor","composeId":"compose-1","description":"Commit: old","status":"done","createdAt":"2026-08-12T03:00:00Z"}
			]`,
			wantLookup: DeploymentAmbiguous,
		},
		{
			name: "one exact legacy token",
			body: `[
				{"deploymentId":"manual","composeId":"compose-1","description":"Commit: manual","status":"done","createdAt":"2026-08-12T03:00:02Z"},
				{"deploymentId":"hookfly","composeId":"compose-1","description":"source=pipeline attempt_id=attempt-1","status":"running","createdAt":"2026-08-12T03:00:01Z"},
				{"deploymentId":"cursor","composeId":"compose-1","description":"Commit: old","status":"done","createdAt":"2026-08-12T03:00:00Z"}
			]`,
			wantLookup: DeploymentFound,
			wantID:     "hookfly",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, http.DefaultClient, io.Discard)
			deployment, lookup, err := client.FindDeploymentAfter(context.Background(), "compose-1", "attempt-1", "cursor")
			if err != nil || lookup != test.wantLookup || deployment.DeploymentID != test.wantID {
				t.Fatalf("FindDeploymentAfter() = %#v/%q/%v", deployment, lookup, err)
			}
		})
	}
}

func TestFindDeploymentAfterReportsNoCandidateAndMissingCursor(t *testing.T) {
	// Break caught: treating an unchanged history as terminal or treating pruned history as entirely new.
	tests := []struct {
		name       string
		cursor     string
		body       string
		wantLookup DeploymentLookup
	}{
		{name: "no candidate", cursor: "cursor", body: `[{"deploymentId":"cursor","composeId":"compose-1","description":null,"status":"done","createdAt":"2026-08-12T03:00:00Z"}]`, wantLookup: DeploymentNotFound},
		{name: "missing cursor", cursor: "pruned", body: `[{"deploymentId":"retained","composeId":"compose-1","description":"Commit: retained","status":"done","createdAt":"2026-08-12T03:00:01Z"}]`, wantLookup: DeploymentCursorMissing},
		{name: "empty initial history has one candidate", cursor: "", body: `[{"deploymentId":"first","composeId":"compose-1","description":"Commit: first","status":"done","createdAt":"2026-08-12T03:00:01Z"}]`, wantLookup: DeploymentFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, http.DefaultClient, io.Discard)
			deployment, lookup, err := client.FindDeploymentAfter(context.Background(), "compose-1", "attempt-1", test.cursor)
			if err != nil || lookup != test.wantLookup {
				t.Fatalf("FindDeploymentAfter() = %#v/%q/%v", deployment, lookup, err)
			}
			if lookup == DeploymentFound && deployment.DeploymentID != "first" {
				t.Fatalf("deployment = %#v", deployment)
			}
		})
	}
}

func TestFindDeploymentStopsReadingAfterFirstExactAttempt(t *testing.T) {
	// Break caught: materializing or validating unrelated older history after the newest exact deployment was decoded.
	body := &failAfterReader{first: []byte(`[{"deploymentId":"exact","composeId":"compose-1","description":"attempt_id=attempt-1","status":"done"}`)}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: request}, nil
	})
	client := newTestClient(t, "http://dokploy.invalid", &http.Client{Transport: transport}, io.Discard)
	deployment, found, err := client.FindDeployment(context.Background(), "compose-1", "attempt-1", "")
	if err != nil || !found || deployment.DeploymentID != "exact" {
		t.Fatalf("FindDeployment() = %#v/%v/%v", deployment, found, err)
	}
	if body.failedReads != 0 {
		t.Fatalf("reads after exact match = %d", body.failedReads)
	}
}

func TestFindDeploymentRejectsInvalidRequestsAndReturnsSafeErrors(t *testing.T) {
	// Break caught: sending empty identifiers or exposing response bodies/API keys from HTTP, JSON, and transport errors.
	var calls atomic.Int32
	var status atomic.Int32
	status.Store(http.StatusInternalServerError)
	var responseBody atomic.Value
	responseBody.Store(`{"message":"` + testAPIKey + `"}`)
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(int(status.Load()))
		_, _ = io.WriteString(w, responseBody.Load().(string))
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, http.DefaultClient, &logs)
	if deployment, found, err := client.FindDeployment(context.Background(), "", "attempt-1", ""); err == nil || found || deployment.DeploymentID != "" {
		t.Fatalf("empty Compose ID FindDeployment() = %#v/%v/%v", deployment, found, err)
	}
	if deployment, found, err := client.FindDeployment(context.Background(), "compose-1", "", ""); err == nil || found || deployment.DeploymentID != "" {
		t.Fatalf("empty attempt ID FindDeployment() = %#v/%v/%v", deployment, found, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("empty identifier calls = %d", calls.Load())
	}
	if _, _, err := client.FindDeployment(context.Background(), "compose-1", "attempt-1", ""); err == nil || strings.Contains(err.Error()+logs.String(), testAPIKey) {
		t.Fatalf("HTTP FindDeployment() error/logs = %v/%q", err, logs.String())
	}

	status.Store(http.StatusOK)
	responseBody.Store(`{"` + testAPIKey + `":"response-body-secret"}`)
	if _, _, err := client.FindDeployment(context.Background(), "compose-1", "attempt-1", ""); err == nil || strings.Contains(err.Error()+logs.String(), testAPIKey) || strings.Contains(err.Error()+logs.String(), "response-body-secret") {
		t.Fatalf("invalid top-level FindDeployment() error/logs = %v/%q", err, logs.String())
	}

	responseBody.Store(`[{"deploymentId":"broken","description":"` + testAPIKey + `"`)
	if _, _, err := client.FindDeployment(context.Background(), "compose-1", "attempt-1", ""); err == nil || strings.Contains(err.Error()+logs.String(), testAPIKey) {
		t.Fatalf("decode FindDeployment() error/logs = %v/%q", err, logs.String())
	}

	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport exposed " + testAPIKey)
	})
	client = newTestClient(t, "http://dokploy.invalid", &http.Client{Transport: transport}, &logs)
	if _, _, err := client.FindDeployment(context.Background(), "compose-1", "attempt-1", ""); err == nil || strings.Contains(err.Error()+logs.String(), testAPIKey) {
		t.Fatalf("transport FindDeployment() error/logs = %v/%q", err, logs.String())
	}
}

func TestNewClientRejectsBaseURLPath(t *testing.T) {
	// Break caught: appending the official endpoint to a configured prefix and sending to /prefix/api/compose.deploy.
	client, err := NewClient("https://dokploy.invalid/prefix", testAPIKey, http.DefaultClient, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || client != nil {
		t.Fatalf("NewClient() returned client=%v error=%v, want rejected base path", client != nil, err)
	}
}

func TestCanonicalBaseURLNormalizesEquivalentOrigins(t *testing.T) {
	tests := map[string]string{
		"https://DOKPLOY.EXAMPLE.INVALID:443/": "https://dokploy.example.invalid",
		"http://Dokploy.Example.Invalid:80":    "http://dokploy.example.invalid",
		"https://dokploy.example.invalid":      "https://dokploy.example.invalid",
	}
	for input, want := range tests {
		got, err := CanonicalBaseURL(input)
		if err != nil {
			t.Fatalf("CanonicalBaseURL(%q) error = %v", input, err)
		}
		if got != want {
			t.Fatalf("CanonicalBaseURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCanonicalBaseURLRejectsNonOriginComponents(t *testing.T) {
	for _, input := range []string{
		"https://user@dokploy.example.invalid",
		"https://dokploy.example.invalid/path",
		"https://dokploy.example.invalid?query=1",
		"https://dokploy.example.invalid#fragment",
	} {
		if _, err := CanonicalBaseURL(input); err == nil {
			t.Fatalf("CanonicalBaseURL(%q) succeeded", input)
		}
	}
}

func TestNewClientRejectsForceQueryBaseURL(t *testing.T) {
	// Break caught: accepting a trailing question mark and turning the official endpoint into query text on the root path.
	client, err := NewClient("https://dokploy.invalid?", testAPIKey, http.DefaultClient, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || client != nil {
		t.Fatalf("NewClient() returned client=%v error=%v, want rejected forced query", client != nil, err)
	}
}

func TestDeployOmitsEmptyOptionalFields(t *testing.T) {
	// Break caught: sending empty optional title/description fields that older Dokploy validators may reject.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got["composeId"] != "compose-only" {
			t.Errorf("request body = %#v", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, http.DefaultClient, io.Discard)
	if _, err := client.Deploy(context.Background(), DeployRequest{ComposeID: "compose-only"}); err != nil {
		t.Fatal(err)
	}
}

func TestDeployAppliesTenSecondRequestDeadline(t *testing.T) {
	// Break caught: removing or lengthening the bounded POST context.
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		deadline, ok := request.Context().Deadline()
		if !ok {
			t.Fatal("request context has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining < 9*time.Second || remaining > 10*time.Second {
			t.Fatalf("request deadline remaining = %v", remaining)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("true")),
			Request:    request,
		}, nil
	})
	client := newTestClient(t, "http://dokploy.invalid", &http.Client{Transport: transport}, io.Discard)
	if _, err := client.Deploy(context.Background(), DeployRequest{ComposeID: "compose-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestDeployWithRequestEvidenceRecordsOnlyAfterPreparationAndBeforeRoundTrip(t *testing.T) {
	// Break caught: persisting evidence for a request rejected before preparation, or recording only after transport begins.
	var recorded atomic.Bool
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if !recorded.Load() {
			t.Fatal("RoundTrip started before request evidence was recorded")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("true")),
			Request:    request,
		}, nil
	})
	client := newTestClient(t, "http://dokploy.invalid", &http.Client{Transport: transport}, io.Discard)
	var evidence []byte
	if _, err := client.DeployWithRequestEvidence(context.Background(), DeployRequest{ComposeID: "compose-1"}, func(value []byte) error {
		evidence = append([]byte(nil), value...)
		recorded.Store(true)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if string(evidence) != `{"version":1,"method":"POST","url":"http://dokploy.invalid/api/compose.deploy","headers":{"Content-Type":"application/json"},"body":{"composeId":"compose-1"}}` {
		t.Fatalf("evidence = %s", evidence)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	if _, err := client.DeployWithRequestEvidence(ctx, DeployRequest{ComposeID: "compose-1"}, func([]byte) error {
		called = true
		return nil
	}); err == nil {
		t.Fatal("canceled deployment succeeded")
	}
	if called {
		t.Fatal("canceled deployment recorded request evidence")
	}
}

func TestDeployWithRequestEvidenceOmitsEvidenceThatMatchesAPIKey(t *testing.T) {
	// Break caught: persisting the API key when it happens to equal a deployment body value.
	var posted map[string]any
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(request.Body).Decode(&posted); err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("true")),
			Request:    request,
		}, nil
	})
	client, err := NewClient("http://dokploy.invalid", `compose-"secret`, &http.Client{Transport: transport}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	recorded := []byte("not-called")
	if _, err := client.DeployWithRequestEvidence(context.Background(), DeployRequest{ComposeID: `compose-"secret`}, func(value []byte) error {
		recorded = append([]byte(nil), value...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if recorded != nil {
		t.Fatalf("credential-colliding evidence = %q", recorded)
	}
	if posted["composeId"] != `compose-"secret` {
		t.Fatalf("posted body = %#v", posted)
	}
}

func TestDeployBodyCannotBeReplayedByTransport(t *testing.T) {
	// Break caught: using a rewindable request body that lets DefaultTransport transparently replay the POST.
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.GetBody != nil {
			t.Error("POST request has GetBody replay hook")
		}
		if request.ContentLength <= 0 {
			t.Errorf("ContentLength = %d, want explicit nonzero length", request.ContentLength)
		}
		if _, err := io.ReadAll(request.Body); err != nil {
			t.Errorf("read request body: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("true")),
			Request:    request,
		}, nil
	})
	client := newTestClient(t, "http://dokploy.invalid", &http.Client{Transport: transport}, io.Discard)
	if _, err := client.Deploy(context.Background(), DeployRequest{ComposeID: "compose-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestDeployBoundsResponseBodyAt64KiB(t *testing.T) {
	// Break caught: retaining an unbounded or partially secret response prefix after truncation.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxResponseBody+17))
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, http.DefaultClient, io.Discard)
	response, err := client.Deploy(context.Background(), DeployRequest{ComposeID: "compose-1"})
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Body) != `{"redacted":true,"truncated":true}` || !response.Truncated {
		t.Fatalf("body length/truncated = %d/%v", len(response.Body), response.Truncated)
	}
}

func TestDeployScrubsUnicodeEscapedAPIKeyFromReturnedJSON(t *testing.T) {
	// Break caught: byte-level replacement missing a JSON-escaped key that decodes to the configured API key.
	escapedKey := jsonUnicodeEscapeASCII(testAPIKey)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"message":{"items":["`+escapedKey+`"]}}`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, http.DefaultClient, io.Discard)
	response, err := client.Deploy(context.Background(), DeployRequest{ComposeID: "compose-1"})
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(response.Body, &decoded); err != nil {
		t.Fatalf("returned body is not canonical JSON: %q: %v", response.Body, err)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(canonical, []byte(testAPIKey)) || !bytes.Contains(canonical, []byte("[REDACTED]")) {
		t.Fatalf("returned JSON leaked escaped API key: %s", canonical)
	}
}

func TestDeployScrubsAPIKeyFromJSONScalars(t *testing.T) {
	// Break caught: preserving a number, boolean, or null response value whose JSON representation equals the configured API key.
	tests := []struct {
		name     string
		apiKey   string
		body     string
		wantBody string
	}{
		{
			name:     "number",
			apiKey:   "12345",
			body:     `{"secret":12345,"unrelated_number":12346,"unrelated_bool":true,"unrelated_null":null}`,
			wantBody: `{"secret":"[REDACTED]","unrelated_bool":true,"unrelated_null":null,"unrelated_number":12346}`,
		},
		{
			name:     "true",
			apiKey:   "true",
			body:     `{"secret":true,"unrelated_number":1,"unrelated_bool":false,"unrelated_null":null}`,
			wantBody: `{"secret":"[REDACTED]","unrelated_bool":false,"unrelated_null":null,"unrelated_number":1}`,
		},
		{
			name:     "false",
			apiKey:   "false",
			body:     `{"secret":false,"unrelated_number":0,"unrelated_bool":true,"unrelated_null":null}`,
			wantBody: `{"secret":"[REDACTED]","unrelated_bool":true,"unrelated_null":null,"unrelated_number":0}`,
		},
		{
			name:     "null",
			apiKey:   "null",
			body:     `{"secret":null,"unrelated_number":0,"unrelated_bool":true}`,
			wantBody: `{"secret":"[REDACTED]","unrelated_bool":true,"unrelated_number":0}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, test.apiKey, http.DefaultClient, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Deploy(context.Background(), DeployRequest{ComposeID: "compose-1"})
			if err != nil {
				t.Fatal(err)
			}
			if string(response.Body) != test.wantBody {
				t.Fatalf("body = %s, want %s", response.Body, test.wantBody)
			}
		})
	}
}

func TestDeployDropsTruncatedPrefixContainingAPIKeyFragment(t *testing.T) {
	// Break caught: returning the first half of an API key when the full key straddles the 64 KiB read boundary.
	fragmentLength := len(testAPIKey) / 2
	body := append(bytes.Repeat([]byte("x"), maxResponseBody-fragmentLength), []byte(testAPIKey)...)
	body = append(body, byte('y'))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, http.DefaultClient, io.Discard)
	response, err := client.Deploy(context.Background(), DeployRequest{ComposeID: "compose-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Truncated || string(response.Body) != `{"redacted":true,"truncated":true}` {
		t.Fatalf("body length/truncated = %d/%v", len(response.Body), response.Truncated)
	}
	if bytes.Contains(response.Body, []byte(testAPIKey[:fragmentLength])) {
		t.Fatalf("returned body contains API key fragment: %q", response.Body)
	}
}

func TestDeployReturnsDefinitiveForHTTPFailure(t *testing.T) {
	// Break caught: treating an observed non-2xx response as an uncertain send.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"message":"failed"}`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, http.DefaultClient, io.Discard)
	response, err := client.Deploy(context.Background(), DeployRequest{ComposeID: "compose-1"})
	assertErrorKind(t, err, TransportDefinitive)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestDeployDoesNotFollowRedirects(t *testing.T) {
	// Break caught: a 307 causing a second POST and forwarding x-api-key to a redirected host.
	var redirectedCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedCalls.Add(1)
		if r.Header.Get("x-api-key") != "" {
			t.Errorf("redirected request exposed x-api-key")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", destination.URL+"/api/compose.deploy")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	client := newTestClient(t, origin.URL, http.DefaultClient, io.Discard)
	response, err := client.Deploy(context.Background(), DeployRequest{ComposeID: "compose-1"})
	assertErrorKind(t, err, TransportDefinitive)
	if response.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if redirectedCalls.Load() != 0 {
		t.Fatalf("redirected POST calls = %d", redirectedCalls.Load())
	}
}

func TestDeployReturnsUncertainAfterDeadlineOrReset(t *testing.T) {
	// Break caught: automatically retrying or definitively failing a POST that may have reached Dokploy.
	t.Run("deadline", func(t *testing.T) {
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			<-release
		}))
		defer func() {
			close(release)
			server.Close()
		}()
		client := newTestClient(t, server.URL, http.DefaultClient, io.Discard)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_, err := client.Deploy(ctx, DeployRequest{ComposeID: "compose-1"})
		assertErrorKind(t, err, TransportUncertain)
	})

	t.Run("connection reset", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("response writer does not support hijacking")
			}
			connection, _, err := hijacker.Hijack()
			if err != nil {
				t.Fatal(err)
			}
			_ = r.Body.Close()
			_ = connection.Close()
		}))
		defer server.Close()
		client := newTestClient(t, server.URL, http.DefaultClient, io.Discard)
		_, err := client.Deploy(context.Background(), DeployRequest{ComposeID: "compose-1"})
		assertErrorKind(t, err, TransportUncertain)
	})
}

func TestDeployReturnsDefinitiveBeforeConnection(t *testing.T) {
	// Break caught: classifying DNS/refused pre-connect failures as possibly enqueued.
	t.Run("already canceled", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		client := newTestClient(t, server.URL, http.DefaultClient, io.Discard)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := client.Deploy(ctx, DeployRequest{ComposeID: "compose-1"})
		assertErrorKind(t, err, TransportDefinitive)
		if calls.Load() != 0 {
			t.Fatalf("POST calls = %d", calls.Load())
		}
	})

	t.Run("refused", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		baseURL := "http://" + listener.Addr().String()
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		client := newTestClient(t, baseURL, http.DefaultClient, io.Discard)
		_, deployErr := client.Deploy(context.Background(), DeployRequest{ComposeID: "compose-1"})
		assertErrorKind(t, deployErr, TransportDefinitive)
	})

	t.Run("dns", func(t *testing.T) {
		transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, &net.DNSError{Err: "no such host", Name: "dokploy.invalid", IsNotFound: true}
		})
		client := newTestClient(t, "http://dokploy.invalid", &http.Client{Transport: transport}, io.Discard)
		_, err := client.Deploy(context.Background(), DeployRequest{ComposeID: "compose-1"})
		assertErrorKind(t, err, TransportDefinitive)
	})
}

func TestDeployNeverReturnsOrLogsAPIKey(t *testing.T) {
	// Break caught: leaking x-api-key through echoed bodies, wrapped transport errors, or structured logs.
	var logs bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"api_key":"`+testAPIKey+`"}`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, http.DefaultClient, &logs)
	response, err := client.Deploy(context.Background(), DeployRequest{ComposeID: "compose-1"})
	if err == nil {
		t.Fatal("expected HTTP error")
	}
	combined := string(response.Body) + err.Error() + logs.String()
	if strings.Contains(combined, testAPIKey) {
		t.Fatalf("API key leaked: %q", combined)
	}

	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport exposed " + testAPIKey)
	})
	client = newTestClient(t, "http://dokploy.invalid", &http.Client{Transport: transport}, &logs)
	_, err = client.Deploy(context.Background(), DeployRequest{ComposeID: "compose-1"})
	if err == nil || strings.Contains(err.Error(), testAPIKey) {
		t.Fatalf("transport error = %v", err)
	}
}

func newTestClient(t *testing.T, baseURL string, httpClient *http.Client, logs io.Writer) *Client {
	t.Helper()
	client, err := NewClient(baseURL, testAPIKey, httpClient, slog.New(slog.NewJSONHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func assertErrorKind(t *testing.T, err error, want TransportErrorKind) {
	t.Helper()
	var transportError *TransportError
	if !errors.As(err, &transportError) {
		t.Fatalf("error = %v, want TransportError", err)
	}
	if transportError.Kind != want {
		t.Fatalf("error kind = %q, want %q (%v)", transportError.Kind, want, err)
	}
}

func mapsEqual(got, want map[string]any) bool {
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	return bytes.Equal(gotJSON, wantJSON)
}

func composeResourcesEqual(got, want []ComposeResource) bool {
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	return bytes.Equal(gotJSON, wantJSON)
}

func jsonUnicodeEscapeASCII(value string) string {
	const hexadecimal = "0123456789abcdef"
	var escaped strings.Builder
	for _, character := range []byte(value) {
		escaped.WriteString(`\u00`)
		escaped.WriteByte(hexadecimal[character>>4])
		escaped.WriteByte(hexadecimal[character&0x0f])
	}
	return escaped.String()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type failAfterReader struct {
	first       []byte
	failedReads int
}

func (r *failAfterReader) Read(output []byte) (int, error) {
	if len(r.first) > 0 {
		count := copy(output, r.first)
		r.first = r.first[count:]
		return count, nil
	}
	r.failedReads++
	return 0, errors.New("read past exact deployment")
}

func (r *failAfterReader) Close() error { return nil }
