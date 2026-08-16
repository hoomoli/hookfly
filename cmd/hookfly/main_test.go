package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/auth"
)

func TestManagementAuthDefaultsToOIDCAndFailsClosedWhenUnconfigured(t *testing.T) {
	_, err := buildManagementAuth(context.Background(), func(string) (string, bool) { return "", false }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "HOOKFLY_EXTERNAL_URL") {
		t.Fatalf("buildManagementAuth() error = %v", err)
	}
}

func TestManagementAuthAllowsOnlyExplicitNoneModeWithoutDiscovery(t *testing.T) {
	service, err := buildManagementAuth(context.Background(), func(key string) (string, bool) {
		if key == "HOOKFLY_AUTH_MODE" {
			return "none", true
		}
		return "", false
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	principal, err := service.Authenticate(request)
	if err != nil || principal.ID != "wireguard-anonymous" {
		t.Fatalf("development principal = %#v/%v", principal, err)
	}
}

func TestManagementAuthBoundsOIDCDiscovery(t *testing.T) {
	// Break caught: an unavailable identity provider can otherwise block process startup indefinitely.
	environment := map[string]string{
		"HOOKFLY_EXTERNAL_URL":        "https://hookfly.example.invalid",
		"AUTHENTIK_ISSUER":            "https://authentik.example.invalid/application/o/hookfly/",
		"AUTHENTIK_CLIENT_ID":         "client-id",
		"AUTHENTIK_CLIENT_SECRET":     "client-secret",
		"HOOKFLY_AUTH_SESSION_SECRET": "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	}
	lookup := func(key string) (string, bool) {
		value, ok := environment[key]
		return value, ok
	}
	var remaining time.Duration
	_, err := buildManagementAuthWithOIDC(context.Background(), lookup, slog.New(slog.NewTextHandler(io.Discard, nil)), func(ctx context.Context, _ auth.Settings, _ *slog.Logger) (auth.Service, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("OIDC discovery context has no deadline")
		}
		remaining = time.Until(deadline)
		return auth.NewDevelopmentService(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if remaining < 9*time.Second || remaining > 10*time.Second {
		t.Fatalf("OIDC discovery deadline = %v, want approximately 10s", remaining)
	}
}

func TestEnvironmentDefaultsAndOverrides(t *testing.T) {
	tests := []struct {
		name     string
		env      string
		fallback string
		override string
	}{
		{name: "config", env: "HOOKFLY_CONFIG", fallback: "config.yaml", override: "/tmp/test-config.yaml"},
		{name: "database", env: "HOOKFLY_DB", fallback: "hookfly.db", override: "/tmp/test-hookfly.db"},
		{name: "public address", env: "HOOKFLY_PUBLIC_ADDR", fallback: ":8080", override: "127.0.0.1:18080"},
		{name: "admin address", env: "HOOKFLY_ADMIN_ADDR", fallback: ":8081", override: "127.0.0.1:18081"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.env, "")
			if got := envOrDefault(tt.env, tt.fallback); got != tt.fallback {
				t.Fatalf("empty value = %q, want default %q", got, tt.fallback)
			}
			t.Setenv(tt.env, tt.override)
			if got := envOrDefault(tt.env, tt.fallback); got != tt.override {
				t.Fatalf("override = %q, want %q", got, tt.override)
			}
		})
	}
}

func TestDispatchWithoutCommandPreservesServerStartup(t *testing.T) {
	// Break caught: treating an empty argument list as a completed CLI command and skipping normal server startup.
	handled, err := dispatchCommand(nil, time.Second)
	if handled || err != nil {
		t.Fatalf("dispatchCommand() = handled %v, error %v", handled, err)
	}
}

func TestHealthcheckCommandAcceptsHealthyAdminEndpoint(t *testing.T) {
	// Break caught: accepting an HTTP response without requiring both status 200 and {"status":"ok"}.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	defer server.Close()

	handled, err := dispatchCommand([]string{"healthcheck", "--url", server.URL}, time.Second)
	if !handled || err != nil {
		t.Fatalf("dispatchCommand() = handled %v, error %v", handled, err)
	}
}

func TestHealthcheckCommandRejectsUnavailableEndpoint(t *testing.T) {
	// Break caught: treating any completed HTTP request, including 503, as healthy.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"status":"unavailable"}`)
	}))
	defer server.Close()

	handled, err := dispatchCommand([]string{"healthcheck", "--url", server.URL}, time.Second)
	if !handled || err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("dispatchCommand() = handled %v, error %v", handled, err)
	}
}

func TestHealthcheckCommandRejectsUnexpectedHealthyBody(t *testing.T) {
	// Break caught: accepting any HTTP 200 response without verifying the admin health contract.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":"degraded"}`)
	}))
	defer server.Close()

	handled, err := dispatchCommand([]string{"healthcheck", "--url", server.URL}, time.Second)
	if !handled || err == nil {
		t.Fatalf("dispatchCommand() = handled %v, error %v", handled, err)
	}
}

func TestHealthcheckCommandRejectsNetworkError(t *testing.T) {
	// Break caught: swallowing connection errors and reporting a stopped admin listener as healthy.
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	handled, err := dispatchCommand([]string{"healthcheck", "--url", url}, time.Second)
	if !handled || err == nil {
		t.Fatalf("dispatchCommand() = handled %v, error %v", handled, err)
	}
}

func TestHealthcheckCommandHasBoundedTimeout(t *testing.T) {
	// Break caught: allowing an unresponsive health endpoint to block container health checks indefinitely.
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	started := time.Now()
	handled, err := dispatchCommand([]string{"healthcheck", "--url", server.URL}, 25*time.Millisecond)
	elapsed := time.Since(started)
	if !handled || err == nil {
		t.Fatalf("dispatchCommand() = handled %v, error %v", handled, err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("dispatchCommand() took %v, want at most 500ms", elapsed)
	}
}

func TestDefaultConfigPathUsesGlobalFile(t *testing.T) {
	if defaultConfigPath != "hookfly.yaml" {
		t.Fatalf("defaultConfigPath = %q", defaultConfigPath)
	}
}
