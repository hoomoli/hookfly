package adminapi

import (
	"context"
	"github.com/hoomoli/hookfly/internal/auth"
	"github.com/hoomoli/hookfly/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatisticsValidationAndAuthorization(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	handler := New(Dependencies{Store: s, Provider: auth.NoAuthProvider{}})
	for _, test := range []struct {
		query string
		code  int
	}{{"", 200}, {"?from=bad", 400}, {"?source=a&source=b", 400}, {"?unknown=x", 400}, {"?from=2026-01-01T00:00:00Z&to=2026-06-01T00:00:00Z", 400}} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/statistics"+test.query, nil))
		if rr.Code != test.code {
			t.Fatalf("query %s: %d", test.query, rr.Code)
		}
		if rr.Code == 200 && !strings.Contains(rr.Body.String(), `"runs":[]`) {
			t.Fatal(rr.Body.String())
		}
	}
	anonymous := New(Dependencies{Store: s})
	for _, path := range []string{"/api/v1/statistics", "/api/v1/changes/stream"} {
		rr := httptest.NewRecorder()
		anonymous.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != 401 {
			t.Fatalf("%s status %d", path, rr.Code)
		}
	}
}

func TestChangesStreamInitialInvalidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	handler := New(Dependencies{Store: &fakeStore{}, Provider: auth.NoAuthProvider{}})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/changes/stream", nil).WithContext(ctx))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "event: change") || rr.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("stream=%d %s", rr.Code, rr.Body.String())
	}
}
