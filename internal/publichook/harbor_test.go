package publichook_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/domain"
	"github.com/hoomoli/hookfly/internal/publichook"
)

func TestHarborWebhookContracts(t *testing.T) {
	tests := []struct {
		name        string
		source      string
		authorize   string
		body        string
		wantStatus  int
		wantCalls   int
		wantRouting domain.RoutingResult
		wantEvent   domain.CanonicalEvent
	}{
		{
			name: "matched artifact push", source: "harbor-primary", authorize: "Bearer harbor-secret",
			body:       `{"type":"PUSH_ARTIFACT","event_data":{"resources":[{"digest":"sha256:abc123","tag":"latest"}],"repository":{"repo_full_name":"team/application"}}}`,
			wantStatus: http.StatusAccepted, wantCalls: 1, wantRouting: domain.RoutingDeploy,
			wantEvent: domain.CanonicalEvent{Provider: "harbor", Source: "harbor-primary", Repository: "application-image", Event: "artifact_push", Ref: "latest", Revision: "sha256:abc123"},
		},
		{
			name: "unconfigured repository", source: "harbor-primary", authorize: "Bearer harbor-secret",
			body:       `{"type":"PUSH_ARTIFACT","event_data":{"resources":[{"digest":"sha256:abc123","tag":"latest"}],"repository":{"repo_full_name":"team/other"}}}`,
			wantStatus: http.StatusAccepted, wantCalls: 1, wantRouting: domain.RoutingUnmatched,
			wantEvent: domain.CanonicalEvent{Provider: "harbor", Source: "harbor-primary", Event: "artifact_push", Ref: "latest", Revision: "sha256:abc123"},
		},
		{
			name: "unsupported pull", source: "harbor-primary", authorize: "Bearer harbor-secret",
			body:       `{"type":"PULL_ARTIFACT","event_data":{"resources":[{"digest":"sha256:abc123","tag":"latest"}],"repository":{"repo_full_name":"team/application"}}}`,
			wantStatus: http.StatusAccepted, wantCalls: 1, wantRouting: domain.RoutingUnsupported,
			wantEvent: domain.CanonicalEvent{Provider: "harbor", Source: "harbor-primary", Repository: "application-image", Event: "artifact_pull"},
		},
		{
			name: "invalid authorization", source: "harbor-primary", authorize: "Bearer other",
			body:       `{"type":"PUSH_ARTIFACT","event_data":{"resources":[{"digest":"sha256:abc123"}],"repository":{"repo_full_name":"team/application"}}}`,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "unknown source", source: "missing", authorize: "Bearer harbor-secret",
			body:       `{"type":"PUSH_ARTIFACT","event_data":{"resources":[{"digest":"sha256:abc123"}],"repository":{"repo_full_name":"team/application"}}}`,
			wantStatus: http.StatusNotFound,
		},
	}

	manager := webhookManager(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ingester := &recordingIngester{result: domain.IngestResult{EventID: "harbor-event"}}
			handler := providerMux("harbor", publichook.New("harbor", manager, ingester, func() time.Time {
				return time.UnixMilli(1_725_000_000_000).UTC()
			}))
			request := httptest.NewRequest(http.MethodPost, providerWebhookPath("harbor", tt.source), strings.NewReader(tt.body))
			request.Header.Set("Authorization", tt.authorize)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, tt.wantStatus, response.Body.String())
			}
			commands := ingester.directCommands()
			if len(commands) != tt.wantCalls {
				t.Fatalf("ingest calls = %d, want %d", len(commands), tt.wantCalls)
			}
			if tt.wantCalls == 0 {
				return
			}
			command := commands[0]
			if command.CanonicalEvent != tt.wantEvent || command.RoutingResult != tt.wantRouting {
				t.Fatalf("command = %#v", command)
			}
			if strings.Contains(strings.ToLower(string(command.HeadersJSON)), "authorization") || strings.Contains(string(command.HeadersJSON), "harbor-secret") {
				t.Fatalf("unsafe headers persisted: %s", command.HeadersJSON)
			}
		})
	}
}
