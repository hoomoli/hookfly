package httpx_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hoomoli/hookfly/internal/httpx"
)

func TestRequestContextInjectsIDAndWritesOneSafeAccessRecord(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	var downstreamRequestID string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downstreamRequestID = httpx.RequestID(r.Context())
		w.WriteHeader(http.StatusAccepted)
	})
	handler := httpx.RequestContext(logger, next)
	req := httptest.NewRequest(http.MethodPost, "/hooks/gitlab?source=test", strings.NewReader(`{"password":"body-secret"}`))
	req.Header.Set("X-Gitlab-Token", "header-secret")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rr.Code)
	}
	if _, err := uuid.Parse(downstreamRequestID); err != nil {
		t.Fatalf("downstream request ID = %q: %v", downstreamRequestID, err)
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("access records = %d, want 1: %s", len(lines), logs.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("decode access record: %v", err)
	}
	if record["request_id"] != downstreamRequestID || record["method"] != http.MethodPost || record["path"] != "/hooks/gitlab" {
		t.Fatalf("access metadata = %#v", record)
	}
	if record["status"] != float64(http.StatusAccepted) {
		t.Fatalf("logged status = %#v, want 202", record["status"])
	}
	if _, exists := record["duration_ms"]; !exists {
		t.Fatalf("duration_ms missing from %#v", record)
	}
	if strings.Contains(logs.String(), "header-secret") || strings.Contains(logs.String(), "body-secret") || strings.Contains(logs.String(), "X-Gitlab-Token") {
		t.Fatalf("access record contains request secret: %s", logs.String())
	}
}

func TestRequestContextCapturesImplicitOKStatus(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := httpx.RequestContext(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record); err != nil {
		t.Fatal(err)
	}
	if record["status"] != float64(http.StatusOK) {
		t.Fatalf("logged status = %#v, want 200", record["status"])
	}
}

func TestRequestContextLogsOneServerErrorWhenHandlerPanics(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := httpx.RequestContext(logger, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	func() {
		defer func() { _ = recover() }()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/panic", nil))
	}()

	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("access records = %d, want 1: %s", len(lines), logs.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatal(err)
	}
	if record["status"] != float64(http.StatusInternalServerError) {
		t.Fatalf("logged status = %#v, want 500", record["status"])
	}
}
