package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/config"
	"github.com/hoomoli/hookfly/internal/runtimecfg"
	"github.com/hoomoli/hookfly/internal/store"
)

func TestGitLabAndGitHubDeliverToOneDokployCompose(t *testing.T) {
	const composeID = "shared-compose"
	var mu sync.Mutex
	attemptIDs := make([]string, 0, 2)
	dokploy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/compose.deploy":
			var request struct {
				ComposeID   string `json:"composeId"`
				Description string `json:"description"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if request.ComposeID != composeID || !strings.HasPrefix(request.Description, "attempt_id=") {
				t.Errorf("deploy request = %#v", request)
			}
			mu.Lock()
			attemptIDs = append(attemptIDs, strings.TrimPrefix(request.Description, "attempt_id="))
			mu.Unlock()
			_, _ = io.WriteString(w, `true`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/deployment.allByCompose":
			if r.URL.Query().Get("composeId") != composeID {
				t.Errorf("compose query = %q", r.URL.RawQuery)
			}
			mu.Lock()
			ids := append([]string(nil), attemptIDs...)
			mu.Unlock()
			deployments := make([]map[string]any, 0, len(ids))
			for index := len(ids) - 1; index >= 0; index-- {
				attemptID := ids[index]
				deployments = append(deployments, map[string]any{
					"deploymentId": "deployment-" + attemptID, "composeId": composeID,
					"description": "Commit: abc123", "status": "done",
					"createdAt": time.Unix(int64(index+1), 0).UTC(),
				})
			}
			_ = json.NewEncoder(w).Encode(deployments)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer dokploy.Close()

	temporaryDirectory := t.TempDir()
	durable, err := store.Open(context.Background(), filepath.Join(temporaryDirectory, "hookfly.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer durable.Close()
	configPath := filepath.Join(temporaryDirectory, "hookfly.yaml")
	configDirectory := filepath.Join(temporaryDirectory, "conf.d")
	if err := os.MkdirAll(configDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		configPath: `kind: Hookfly
config_dir: conf.d
history: {}
polling:
  interval: 1ms
  timeout: 1m
`,
		filepath.Join(configDirectory, "gitlab.yaml"): `kind: GitLabSources
sources:
  - id: gitlab-primary
    token: gitlab-token
    repositories:
      - id: application
        name: Application
        project_id: 7
`,
		filepath.Join(configDirectory, "github.yaml"): `kind: GitHubSources
sources:
  - id: github-primary
    secret: github-secret
    repositories:
      - id: application
        name: Application
        repository_id: 101
`,
		filepath.Join(configDirectory, "targets.yaml"): `kind: DokployTargets
connections:
  - id: shared
    base_url: ` + dokploy.URL + `
    api_key: test-key
targets:
  - id: production
    type: dokploy
    connection: shared
    resource_type: compose
    resource_id: shared-compose
`,
		filepath.Join(configDirectory, "routes.yaml"): `kind: Routes
routes:
  - id: gitlab-deploy
    priority: 1
    match:
      source: gitlab-primary
      repository: application
      event: pipeline
    action:
      type: deploy
      targets: [production]
  - id: github-deploy
    priority: 2
    match:
      source: github-primary
      repository: application
      event: pipeline
    action:
      type: deploy
      targets: [production]
`,
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	logger := discardAppLogger()
	generation, err := runtimecfg.Load(configPath, nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	manager := runtimecfg.NewManager(generation, durable, runtimecfg.Options{Path: configPath, Logger: logger})
	application := newTestApp(manager, durable, logger)
	publicServer, _ := application.servers()

	gitlabBody := `{"project":{"id":7},"object_attributes":{"id":88,"ref":"main","status":"success","source":"push","sha":"abc123"}}`
	gitlabRequest := httptest.NewRequest(http.MethodPost, "/hooks/gitlab", strings.NewReader(gitlabBody))
	gitlabRequest.Header.Set("X-Gitlab-Event", "Pipeline Hook")
	gitlabRequest.Header.Set("X-Gitlab-Token", "gitlab-token")
	gitlabRequest.Header.Set("X-Gitlab-Event-UUID", "gitlab-delivery")
	assertAccepted(t, publicServer.Handler, gitlabRequest)

	githubBody := `{"repository":{"id":101},"workflow_run":{"id":91,"head_branch":"main","conclusion":"success","head_sha":"abc123","event":"push"}}`
	githubRequest := httptest.NewRequest(http.MethodPost, "/hooks/github/github-primary", strings.NewReader(githubBody))
	githubRequest.Header.Set("X-GitHub-Event", "workflow_run")
	githubRequest.Header.Set("X-GitHub-Delivery", "github-delivery")
	mac := hmac.New(sha256.New, []byte("github-secret"))
	_, _ = mac.Write([]byte(githubBody))
	githubRequest.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	assertAccepted(t, publicServer.Handler, githubRequest)

	page, err := durable.ListEvents(context.Background(), store.EventQuery{Page: 1, PageSize: 10})
	if err != nil || page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("events = %#v, %v", page, err)
	}
	expectedRules := map[string]bool{"gitlab-deploy": false, "github-deploy": false}
	persistedAttemptIDs := make(map[string]struct{}, 2)
	for _, item := range page.Items {
		detail, detailErr := durable.GetEventDetail(context.Background(), item.ID)
		if detailErr != nil {
			t.Fatalf("detail = %#v, %v", detail, detailErr)
		}
		if detail.RuleID == nil || expectedRules[*detail.RuleID] {
			t.Fatalf("event %q rule ID = %v", item.ID, detail.RuleID)
		}
		if _, known := expectedRules[*detail.RuleID]; !known {
			t.Fatalf("event %q selected unexpected rule %q", item.ID, *detail.RuleID)
		}
		expectedRules[*detail.RuleID] = true
		if len(detail.Deliveries) != 1 || detail.Deliveries[0].TargetID != "production" {
			t.Fatalf("event %q deliveries = %#v", item.ID, detail.Deliveries)
		}
		delivery := detail.Deliveries[0]
		if delivery.CurrentAttemptID == "" {
			t.Fatalf("event %q current attempt ID is empty", item.ID)
		}
		if _, duplicate := persistedAttemptIDs[delivery.CurrentAttemptID]; duplicate {
			t.Fatalf("duplicate persisted attempt ID %q", delivery.CurrentAttemptID)
		}
		persistedAttemptIDs[delivery.CurrentAttemptID] = struct{}{}
		if len(delivery.Attempts) != 1 || delivery.Attempts[0].ID != delivery.CurrentAttemptID || !delivery.Attempts[0].Current {
			t.Fatalf("event %q attempts = %#v", item.ID, delivery.Attempts)
		}
		var binding struct {
			BindingVersion int    `json:"binding_version"`
			ResourceID     string `json:"resource_id"`
		}
		if err := json.Unmarshal(delivery.TargetSnapshot, &binding); err != nil {
			t.Fatal(err)
		}
		if binding.BindingVersion != 2 || binding.ResourceID != composeID {
			t.Fatalf("event %q binding = %#v", item.ID, binding)
		}
	}
	for ruleID, seen := range expectedRules {
		if !seen {
			t.Fatalf("rule %q was not selected", ruleID)
		}
	}
	if len(persistedAttemptIDs) != 2 {
		t.Fatalf("persisted attempt IDs = %#v", persistedAttemptIDs)
	}
	for range 2 {
		worked, dispatchErr := application.dispatcher.RunOnce(context.Background())
		if dispatchErr != nil || !worked {
			t.Fatalf("dispatch = %v, %v", worked, dispatchErr)
		}
		if err := application.poller.RunOnce(context.Background(), time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	postedAttemptIDs := append([]string(nil), attemptIDs...)
	mu.Unlock()
	posted := make(map[string]struct{}, len(postedAttemptIDs))
	for _, attemptID := range postedAttemptIDs {
		if _, known := persistedAttemptIDs[attemptID]; !known {
			t.Fatalf("Dokploy attempt ID %q was not persisted", attemptID)
		}
		if _, duplicate := posted[attemptID]; duplicate {
			t.Fatalf("duplicate Dokploy attempt ID %q", attemptID)
		}
		posted[attemptID] = struct{}{}
	}
	if len(posted) != 2 {
		t.Fatalf("Dokploy attempt IDs = %#v", postedAttemptIDs)
	}
	page, err = durable.ListEvents(context.Background(), store.EventQuery{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		detail, detailErr := durable.GetEventDetail(context.Background(), item.ID)
		if detailErr != nil || detail.Deliveries[0].DeploymentStatus != "done" {
			t.Fatalf("reconciled detail = %#v, %v", detail, detailErr)
		}
	}
}

func TestReleaseRegressionTable(t *testing.T) {
	// Each case names a release contract and exercises the public file, HTTP, or store boundary
	// whose regression would violate it.
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{name: "absent and empty configuration directory", run: releaseAbsentAndEmptyDirectory},
		{name: "filename permutation", run: releaseFilenamePermutation},
		{name: "unknown fields kinds and versions", run: releaseStrictDocuments},
		{name: "cross-file duplicates report both locations", run: releaseDuplicateLocations},
		{name: "invalid references", run: releaseInvalidReferences},
		{name: "overlap rejection", run: releaseOverlapRejection},
		{name: "failed reload preserves published generation", run: releaseFailedReloadPreservation},
		{name: "GitLab token authentication", run: releaseGitLabAuthentication},
		{name: "GitHub HMAC authentication", run: releaseGitHubAuthentication},
		{name: "provider-neutral canonical events", run: releaseCanonicalEvents},
		{name: "shared target delivery", run: releaseSharedTargetDelivery},
		{name: "v1 schema rejection", run: releaseV1SchemaRejection},
		{name: "secret surfaces", run: releaseSecretSurfaces},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

func releaseAbsentAndEmptyDirectory(t *testing.T) {
	for _, createDirectory := range []bool{false, true} {
		root := t.TempDir()
		global := filepath.Join(root, "hookfly.yaml")
		writeReleaseFile(t, global, releaseGlobalYAML())
		if createDirectory {
			if err := os.Mkdir(filepath.Join(root, "conf.d"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		generation, err := runtimecfg.Load(global, nil, discardAppLogger())
		if err != nil || generation.Digest() == "" || len(generation.Repositories()) != 0 || len(generation.Targets()) != 0 {
			t.Fatalf("createDirectory=%v generation/error = %#v/%v", createDirectory, generation, err)
		}
	}
}

func releaseFilenamePermutation(t *testing.T) {
	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	first := map[string]string{"gitlab": "a.yaml", "github": "b.yaml", "targets": "c.yaml", "routes": "d.yaml"}
	second := map[string]string{"gitlab": "z.yaml", "github": "y.yaml", "targets": "x.yaml", "routes": "w.yaml"}
	writeReleaseConfiguration(t, firstRoot, first, "release")
	writeReleaseConfiguration(t, secondRoot, second, "release")
	firstGeneration, err := runtimecfg.Load(filepath.Join(firstRoot, "hookfly.yaml"), nil, discardAppLogger())
	if err != nil {
		t.Fatal(err)
	}
	secondGeneration, err := runtimecfg.Load(filepath.Join(secondRoot, "hookfly.yaml"), nil, discardAppLogger())
	if err != nil {
		t.Fatal(err)
	}
	if firstGeneration.Digest() != secondGeneration.Digest() {
		t.Fatalf("filename permutation changed digest: %s != %s", firstGeneration.Digest(), secondGeneration.Digest())
	}
}

func releaseStrictDocuments(t *testing.T) {
	for _, test := range []struct {
		name, global, resource string
	}{
		{name: "unknown global field", global: releaseGlobalYAML() + "unexpected: true\n"},
		{name: "unknown kind", global: releaseGlobalYAML(), resource: "kind: Unknown\n"},
		{name: "multiple documents", global: releaseGlobalYAML() + "---\nkind: Hookfly\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeReleaseFile(t, filepath.Join(root, "hookfly.yaml"), test.global)
			if test.resource != "" {
				writeReleaseFile(t, filepath.Join(root, "conf.d", "resource.yaml"), test.resource)
			}
			if _, err := runtimecfg.Load(filepath.Join(root, "hookfly.yaml"), nil, discardAppLogger()); err == nil {
				t.Fatal("Load() error = nil")
			}
		})
	}
}

func releaseDuplicateLocations(t *testing.T) {
	root := t.TempDir()
	writeReleaseConfiguration(t, root, map[string]string{"gitlab": "source-a.yaml", "github": "github.yaml", "targets": "targets.yaml", "routes": "routes.yaml"}, "release")
	writeReleaseFile(t, filepath.Join(root, "conf.d", "source-b.yaml"), releaseGitLabYAML("release"))
	_, err := runtimecfg.Load(filepath.Join(root, "hookfly.yaml"), nil, discardAppLogger())
	if err == nil || !strings.Contains(err.Error(), "source-a.yaml") || !strings.Contains(err.Error(), "source-b.yaml") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func releaseInvalidReferences(t *testing.T) {
	root := t.TempDir()
	writeReleaseConfiguration(t, root, releaseFilenames(), "release")
	routes := strings.Replace(releaseRoutesYAML("release"), "targets: [production]", "targets: [missing]", 1)
	writeReleaseFile(t, filepath.Join(root, "conf.d", "routes.yaml"), routes)
	if _, err := runtimecfg.Load(filepath.Join(root, "hookfly.yaml"), nil, discardAppLogger()); err == nil || !strings.Contains(err.Error(), "unknown target") {
		t.Fatalf("invalid reference error = %v", err)
	}
}

func releaseOverlapRejection(t *testing.T) {
	root := t.TempDir()
	writeReleaseConfiguration(t, root, releaseFilenames(), "release")
	overlap := releaseRoutesYAML("release") + "  - id: overlap\n    priority: 1\n    match:\n      source: gitlab-primary\n      repository: application\n      event: pipeline\n    action:\n      type: record_only\n"
	writeReleaseFile(t, filepath.Join(root, "conf.d", "routes.yaml"), overlap)
	if _, err := runtimecfg.Load(filepath.Join(root, "hookfly.yaml"), nil, discardAppLogger()); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("overlap error = %v", err)
	}
}

func releaseFailedReloadPreservation(t *testing.T) {
	fixture := newReleaseFixture(t, "reload")
	before := fixture.manager.Current().Digest()
	writeReleaseFile(t, filepath.Join(fixture.root, "conf.d", "routes.yaml"), strings.Replace(releaseRoutesYAML("reload"), "targets: [production]", "targets: [missing]", 1))
	if outcome, err := fixture.manager.Reload(context.Background(), "release-test"); outcome != "" || !errors.Is(err, runtimecfg.ErrInvalidConfig) {
		t.Fatalf("Reload() = %q, %v", outcome, err)
	}
	if after := fixture.manager.Current().Digest(); after != before {
		t.Fatalf("failed reload changed digest: %s != %s", after, before)
	}
}

func releaseGitLabAuthentication(t *testing.T) {
	fixture := newReleaseFixture(t, "auth")
	if response := fixture.postGitLab("wrong"); response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d", response.Code)
	}
	if response := fixture.postGitLab("gitlab-token-auth"); response.Code != http.StatusAccepted {
		t.Fatalf("correct token status = %d: %s", response.Code, response.Body.String())
	}
}

func releaseGitHubAuthentication(t *testing.T) {
	fixture := newReleaseFixture(t, "auth")
	if response := fixture.postGitHub("wrong"); response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong HMAC status = %d", response.Code)
	}
	if response := fixture.postGitHub("github-secret-auth"); response.Code != http.StatusAccepted {
		t.Fatalf("correct HMAC status = %d: %s", response.Code, response.Body.String())
	}
}

func releaseCanonicalEvents(t *testing.T) {
	fixture := newReleaseFixture(t, "canonical")
	if fixture.postGitLab("gitlab-token-canonical").Code != http.StatusAccepted || fixture.postGitHub("github-secret-canonical").Code != http.StatusAccepted {
		t.Fatal("provider webhook was not accepted")
	}
	page, err := fixture.store.ListEvents(context.Background(), store.EventQuery{Page: 1, PageSize: 10})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("ListEvents() = %#v, %v", page, err)
	}
	for _, event := range page.Items {
		if event.Repository != "application" || event.EventType != "pipeline" || event.Ref == nil || *event.Ref != "main" || event.Revision == nil || *event.Revision != "abc123" {
			t.Fatalf("canonical event = %#v", event)
		}
	}
}

func releaseSharedTargetDelivery(t *testing.T) {
	fixture := newReleaseFixture(t, "shared")
	responses := []*httptest.ResponseRecorder{fixture.postGitLab("gitlab-token-shared"), fixture.postGitHub("github-secret-shared")}
	for _, response := range responses {
		if response.Code != http.StatusAccepted {
			t.Fatalf("webhook status = %d: %s", response.Code, response.Body.String())
		}
	}
	page, err := fixture.store.ListEvents(context.Background(), store.EventQuery{Page: 1, PageSize: 10})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("ListEvents() = %#v, %v", page, err)
	}
	for _, event := range page.Items {
		detail, err := fixture.store.GetEventDetail(context.Background(), event.ID)
		if err != nil || len(detail.Deliveries) != 1 || detail.Deliveries[0].TargetID != "production" || !strings.Contains(string(detail.Deliveries[0].TargetSnapshot), `"resource_id":"compose-shared"`) {
			t.Fatalf("shared delivery = %#v, %v", detail, err)
		}
	}
}

func releaseV1SchemaRejection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hookfly.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("CREATE TABLE events (project_id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(context.Background(), path); !errors.Is(err, store.ErrIncompatibleSchema) {
		t.Fatalf("Open(v1) error = %v", err)
	}
}

func assertAccepted(t *testing.T, handler http.Handler, request *http.Request) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("webhook status = %d; body=%s", response.Code, response.Body.String())
	}
}

type releaseFixture struct {
	root    string
	store   *store.Store
	manager *runtimecfg.Manager
	public  http.Handler
}

func newReleaseFixture(t *testing.T, marker string) *releaseFixture {
	t.Helper()
	root := t.TempDir()
	writeReleaseConfiguration(t, root, releaseFilenames(), marker)
	durable, err := store.Open(context.Background(), filepath.Join(root, "hookfly.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = durable.Close() })
	logger := discardAppLogger()
	generation, err := runtimecfg.Load(filepath.Join(root, "hookfly.yaml"), nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	manager := runtimecfg.NewManager(generation, durable, runtimecfg.Options{Path: filepath.Join(root, "hookfly.yaml"), Logger: logger})
	application := newTestApp(manager, durable, logger)
	public, _ := application.servers()
	return &releaseFixture{root: root, store: durable, manager: manager, public: public.Handler}
}

func (fixture *releaseFixture) postGitLab(token string) *httptest.ResponseRecorder {
	body := `{"project":{"id":7},"object_attributes":{"id":88,"ref":"main","status":"success","source":"push","sha":"abc123"}}`
	request := httptest.NewRequest(http.MethodPost, "/hooks/gitlab", strings.NewReader(body))
	request.Header.Set("X-Gitlab-Event", "Pipeline Hook")
	request.Header.Set("X-Gitlab-Token", token)
	request.Header.Set("X-Gitlab-Event-UUID", "gitlab-release")
	response := httptest.NewRecorder()
	fixture.public.ServeHTTP(response, request)
	return response
}

func (fixture *releaseFixture) postGitHub(secret string) *httptest.ResponseRecorder {
	body := `{"repository":{"id":101},"workflow_run":{"id":91,"head_branch":"main","conclusion":"success","head_sha":"abc123","event":"push"}}`
	request := httptest.NewRequest(http.MethodPost, "/hooks/github/github-primary", strings.NewReader(body))
	request.Header.Set("X-GitHub-Event", "workflow_run")
	request.Header.Set("X-GitHub-Delivery", "github-release")
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	response := httptest.NewRecorder()
	fixture.public.ServeHTTP(response, request)
	return response
}

func releaseFilenames() map[string]string {
	return map[string]string{"gitlab": "gitlab.yaml", "github": "github.yaml", "targets": "targets.yaml", "routes": "routes.yaml"}
}

func writeReleaseConfiguration(t *testing.T, root string, filenames map[string]string, marker string) {
	t.Helper()
	writeReleaseFile(t, filepath.Join(root, "hookfly.yaml"), releaseGlobalYAML())
	resources := map[string]string{
		"gitlab": releaseGitLabYAML(marker), "github": releaseGitHubYAML(marker),
		"targets": releaseTargetsYAML(marker), "routes": releaseRoutesYAML(marker),
	}
	for logical, content := range resources {
		writeReleaseFile(t, filepath.Join(root, "conf.d", filenames[logical]), content)
	}
}

func writeReleaseFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func releaseGlobalYAML() string {
	return "kind: Hookfly\nconfig_dir: conf.d\nhistory: {}\npolling: {}\n"
}

func releaseGitLabYAML(marker string) string {
	return "kind: GitLabSources\nsources:\n  - id: gitlab-primary\n    token: gitlab-token-" + marker + "\n    repositories:\n      - id: application\n        name: Application\n        project_id: 7\n"
}

func releaseGitHubYAML(marker string) string {
	return "kind: GitHubSources\nsources:\n  - id: github-primary\n    secret: github-secret-" + marker + "\n    repositories:\n      - id: application\n        name: Application\n        repository_id: 101\n"
}

func releaseTargetsYAML(marker string) string {
	return "kind: DokployTargets\nconnections:\n  - id: primary\n    base_url: https://dokploy.example.invalid\n    api_key: dokploy-key-" + marker + "\ntargets:\n  - id: production\n    type: dokploy\n    connection: primary\n    resource_type: compose\n    resource_id: compose-" + marker + "\n"
}

func releaseRoutesYAML(marker string) string {
	return "kind: Routes\nroutes:\n  - id: gitlab-route-" + marker + "\n    priority: 1\n    match:\n      source: gitlab-primary\n      repository: application\n      event: pipeline\n    action:\n      type: deploy\n      targets: [production]\n  - id: github-route-" + marker + "\n    priority: 2\n    match:\n      source: github-primary\n      repository: application\n      event: pipeline\n    action:\n      type: deploy\n      targets: [production]\n"
}

func releaseSecretSurfaces(t *testing.T) {
	const (
		gitlabMarker  = "TASK9_GITLAB_TOKEN_MARKER"
		githubMarker  = "TASK9_GITHUB_SECRET_MARKER"
		dokployMarker = "TASK9_DOKPLOY_KEY_MARKER"
		payloadMarker = "TASK9_PAYLOAD_PRIVATE_MARKER"
	)
	originalDefaultClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/compose.deploy" {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`not found`)), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`true`)), Header: make(http.Header)}, nil
	})}
	defer func() { http.DefaultClient = originalDefaultClient }()

	root := t.TempDir()
	writeReleaseConfiguration(t, root, releaseFilenames(), "secret")
	writeReleaseFile(t, filepath.Join(root, "conf.d", "gitlab.yaml"), strings.Replace(releaseGitLabYAML("secret"), "gitlab-token-secret", gitlabMarker, 1))
	writeReleaseFile(t, filepath.Join(root, "conf.d", "github.yaml"), strings.Replace(releaseGitHubYAML("secret"), "github-secret-secret", githubMarker, 1))
	targets := strings.Replace(releaseTargetsYAML("secret"), "dokploy-key-secret", dokployMarker, 1)
	writeReleaseFile(t, filepath.Join(root, "conf.d", "targets.yaml"), targets)

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	generation, err := runtimecfg.Load(filepath.Join(root, "hookfly.yaml"), nil, logger)
	if err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(root, "hookfly.db")
	durable, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer durable.Close()
	manager := runtimecfg.NewManager(generation, durable, runtimecfg.Options{Path: filepath.Join(root, "hookfly.yaml"), Logger: logger})
	application := newTestApp(manager, durable, logger)
	publicServer, adminServer := application.servers()

	gitlabBody := `{"project":{"id":7},"object_attributes":{"id":88,"ref":"main","status":"success","source":"push","sha":"abc123"},"token":"` + payloadMarker + `"}`
	gitlabRequest := httptest.NewRequest(http.MethodPost, "/hooks/gitlab", strings.NewReader(gitlabBody))
	gitlabRequest.Header.Set("X-Gitlab-Event", "Pipeline Hook")
	gitlabRequest.Header.Set("X-Gitlab-Token", gitlabMarker)
	gitlabRequest.Header.Set("X-Gitlab-Event-UUID", "secret-gitlab")
	gitlabResponse := httptest.NewRecorder()
	publicServer.Handler.ServeHTTP(gitlabResponse, gitlabRequest)

	githubBody := `{"repository":{"id":101},"workflow_run":{"id":91,"head_branch":"main","conclusion":"success","head_sha":"abc123","event":"push"}}`
	githubRequest := httptest.NewRequest(http.MethodPost, "/hooks/github/github-primary", strings.NewReader(githubBody))
	githubRequest.Header.Set("X-GitHub-Event", "workflow_run")
	githubRequest.Header.Set("X-GitHub-Delivery", "secret-github")
	mac := hmac.New(sha256.New, []byte(githubMarker))
	_, _ = mac.Write([]byte(githubBody))
	githubSignature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	githubRequest.Header.Set("X-Hub-Signature-256", githubSignature)
	githubResponse := httptest.NewRecorder()
	publicServer.Handler.ServeHTTP(githubResponse, githubRequest)
	if gitlabResponse.Code != http.StatusAccepted || githubResponse.Code != http.StatusAccepted {
		t.Fatalf("secret webhook responses = %d/%d", gitlabResponse.Code, githubResponse.Code)
	}
	for range 2 {
		worked, err := application.dispatcher.RunOnce(context.Background())
		if err != nil || !worked {
			t.Fatalf("dispatcher RunOnce() = %v, %v", worked, err)
		}
	}

	invalid := &config.Bundle{
		Global:             config.Global{Kind: "Hookfly"},
		GitLabSources:      []config.GitLabSource{{ID: "gitlab", Token: gitlabMarker}},
		GitHubSources:      []config.GitHubSource{{ID: "github", Secret: githubMarker}},
		DokployConnections: []config.DokployConnection{{ID: "dokploy", BaseURL: "https://" + dokployMarker + "@example.invalid", APIKey: dokployMarker}},
	}
	_, compileErr := runtimecfg.Compile(invalid, logger)
	if compileErr == nil {
		t.Fatal("invalid secret-bearing configuration compiled")
	}

	forbidden := []string{gitlabMarker, githubMarker, dokployMarker, githubSignature, "X-Gitlab-Token", "X-Hub-Signature-256"}
	surfaces := map[string][]byte{
		"compile error": []byte(compileErr.Error()), "logs": logs.Bytes(), "digest": []byte(generation.Digest()),
		"GitLab response": gitlabResponse.Body.Bytes(), "GitHub response": githubResponse.Body.Bytes(),
	}
	pageRecorder := httptest.NewRecorder()
	adminServer.Handler.ServeHTTP(pageRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/events", nil))
	surfaces["admin list"] = pageRecorder.Body.Bytes()
	page, err := durable.ListEvents(context.Background(), store.EventQuery{Page: 1, PageSize: 10})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("events = %#v, %v", page, err)
	}
	for _, event := range page.Items {
		detailRecorder := httptest.NewRecorder()
		adminServer.Handler.ServeHTTP(detailRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/events/"+event.ID, nil))
		if detailRecorder.Code != http.StatusOK {
			t.Fatalf("admin detail = %d %s", detailRecorder.Code, detailRecorder.Body.String())
		}
		surfaces["admin detail "+event.ID] = detailRecorder.Body.Bytes()
		if event.Provider == "gitlab" && (bytes.Contains(detailRecorder.Body.Bytes(), []byte(payloadMarker)) || !bytes.Contains(detailRecorder.Body.Bytes(), []byte("[REDACTED]"))) {
			t.Fatalf("management payload was not redacted: %s", detailRecorder.Body.String())
		}
	}

	inspection, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Close()
	rows, err := inspection.Query(`
		SELECT 'event', coalesce(rule_snapshot, X''), coalesce(raw_headers_json, X''), coalesce(raw_payload_json, X'') FROM events
		UNION ALL SELECT 'delivery', coalesce(target_snapshot, X''), X'', X'' FROM deliveries
		UNION ALL SELECT 'attempt', coalesce(request_json, X''), coalesce(response_json, X''), X'' FROM delivery_attempts
		UNION ALL SELECT 'audit', coalesce(before_json, X''), coalesce(after_json, X''), X'' FROM audit_logs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var first, second, third []byte
		if err := rows.Scan(&kind, &first, &second, &third); err != nil {
			t.Fatal(err)
		}
		data := append(append(append([]byte(nil), first...), second...), third...)
		for _, marker := range forbidden {
			if bytes.Contains(data, []byte(marker)) {
				t.Fatalf("SQLite %s leaked marker %q", kind, marker)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var rawPayload []byte
	if err := inspection.QueryRow("SELECT raw_payload_json FROM events WHERE provider = 'gitlab'").Scan(&rawPayload); err != nil {
		t.Fatal(err)
	}
	if len(rawPayload) > 2<<20 || !bytes.Contains(rawPayload, []byte(payloadMarker)) {
		t.Fatalf("raw payload length/evidence = %d/%t", len(rawPayload), bytes.Contains(rawPayload, []byte(payloadMarker)))
	}
	for surface, data := range surfaces {
		for _, marker := range forbidden {
			if bytes.Contains(data, []byte(marker)) {
				t.Fatalf("%s leaked marker %q", surface, marker)
			}
		}
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
