package adminapi

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hoomoli/hookfly/internal/auth"
	"github.com/hoomoli/hookfly/internal/domain"
	"github.com/hoomoli/hookfly/internal/runtimecfg"
	"github.com/hoomoli/hookfly/internal/service"
	"github.com/hoomoli/hookfly/internal/store"
)

type fakeStore struct {
	readyErr          error
	clearResult       store.ClearHistoryResult
	clearErr          error
	clearCalled       bool
	query             store.EventQuery
	page              store.EventPage
	detail            store.EventDetail
	detailErr         error
	notificationQuery store.NotificationQuery
	notificationPage  store.NotificationPage
	notificationErr   error
}

func (f *fakeStore) Ready(context.Context) error { return f.readyErr }
func (f *fakeStore) ClearHistory(context.Context) (store.ClearHistoryResult, error) {
	f.clearCalled = true
	return f.clearResult, f.clearErr
}
func (f *fakeStore) ListRepositories(_ context.Context, configured []store.ConfiguredRepository) ([]store.ConfiguredRepository, error) {
	return append([]store.ConfiguredRepository(nil), configured...), nil
}
func (f *fakeStore) ListEvents(_ context.Context, query store.EventQuery) (store.EventPage, error) {
	f.query = query
	return f.page, nil
}
func (f *fakeStore) GetEventDetail(context.Context, string) (store.EventDetail, error) {
	return f.detail, f.detailErr
}
func (f *fakeStore) ListNotifications(_ context.Context, query store.NotificationQuery) (store.NotificationPage, error) {
	f.notificationQuery = query
	return f.notificationPage, f.notificationErr
}

type fakeAttempts struct {
	result                              service.AttemptResult
	err                                 error
	deliveryID, expected, actor, reason string
	operation                           domain.Operation
}

type fakeConfigController struct {
	status       runtimecfg.Status
	statusErr    error
	outcome      runtimecfg.ReloadOutcome
	reloadErr    error
	actor        string
	repositories []store.ConfiguredRepository
	binding      runtimecfg.BindingStatus
	connections  []runtimecfg.ConnectionSummary
	resources    []runtimecfg.ConnectionResource
	targets      runtimecfg.TargetInventory
	resourceID   string
	resourceErr  error
}

func (f *fakeConfigController) Status(context.Context) (runtimecfg.Status, error) {
	return f.status, f.statusErr
}

func (f *fakeConfigController) Reload(_ context.Context, actor string) (runtimecfg.ReloadOutcome, error) {
	f.actor = actor
	return f.outcome, f.reloadErr
}

func (f *fakeConfigController) Repositories() []store.ConfiguredRepository {
	return append([]store.ConfiguredRepository(nil), f.repositories...)
}

func (f *fakeConfigController) Connections() []runtimecfg.ConnectionSummary {
	return append([]runtimecfg.ConnectionSummary(nil), f.connections...)
}

func (f *fakeConfigController) DiscoverConnectionResources(_ context.Context, id string) ([]runtimecfg.ConnectionResource, error) {
	f.resourceID = id
	return append([]runtimecfg.ConnectionResource(nil), f.resources...), f.resourceErr
}

func (f *fakeConfigController) TargetInventory(context.Context) runtimecfg.TargetInventory {
	return f.targets
}

func (f *fakeConfigController) BindingStatus(string, []byte) runtimecfg.BindingStatus {
	if f.binding == "" {
		return runtimecfg.BindingCompatible
	}
	return f.binding
}

func (f *fakeAttempts) Execute(_ context.Context, deliveryID, expected string, operation domain.Operation, actor, reason string) (service.AttemptResult, error) {
	f.deliveryID, f.expected, f.operation, f.actor, f.reason = deliveryID, expected, operation, actor, reason
	return f.result, f.err
}

type providerFunc func(*http.Request) (auth.Principal, error)

func (f providerFunc) Authenticate(r *http.Request) (auth.Principal, error) { return f(r) }

func TestHealthBypassesAuthAndUsesOnlyStoreReadiness(t *testing.T) {
	authCalls := 0
	provider := providerFunc(func(*http.Request) (auth.Principal, error) {
		authCalls++
		return auth.Principal{}, errors.New("must not authenticate health")
	})
	for _, test := range []struct {
		err  error
		code int
		body string
	}{
		{nil, http.StatusOK, `{"status":"ok"}` + "\n"},
		{errors.New("sqlite unavailable"), http.StatusServiceUnavailable, `{"error":{"code":"unavailable","message":"service unavailable"}}` + "\n"},
	} {
		handler := New(Dependencies{Store: &fakeStore{readyErr: test.err}, Provider: provider})
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
		if rr.Code != test.code || rr.Body.String() != test.body {
			t.Fatalf("health = %d %q", rr.Code, rr.Body.String())
		}
	}
	if authCalls != 0 {
		t.Fatalf("health auth calls = %d", authCalls)
	}
}

func TestAuthenticationRoutesDispatchToTheConfiguredService(t *testing.T) {
	// Break caught: protecting the login routes with the session middleware or forgetting to expose them on the management listener.
	service := &authServiceFake{NoAuthProvider: auth.NoAuthProvider{}, origin: "https://webhook.example.com"}
	handler := New(Dependencies{Store: &fakeStore{}, Provider: service, Auth: service})
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/auth/callback", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil),
	} {
		if request.Method == http.MethodPost {
			request.Header.Set("Origin", service.origin)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("%s %s = %d", request.Method, request.URL.Path, recorder.Code)
		}
	}
	if strings.Join(service.calls, ",") != "login,callback,session,logout" {
		t.Fatalf("auth calls = %#v", service.calls)
	}
}

func TestAuthenticationRouteMethodErrorsAreNeverCached(t *testing.T) {
	service := &authServiceFake{NoAuthProvider: auth.NoAuthProvider{}, origin: "https://webhook.example.com"}
	handler := New(Dependencies{Store: &fakeStore{}, Provider: service, Auth: service})
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/auth/session", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/auth/logout", nil),
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s = %d, want 405", request.Method, request.URL.Path, recorder.Code)
		}
		if recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s Cache-Control = %q", request.Method, request.URL.Path, recorder.Header().Get("Cache-Control"))
		}
	}
}

func TestUnsafeBusinessRoutesRequireTheConfiguredOrigin(t *testing.T) {
	// Break caught: a browser using the valid session cookie can submit a cross-site deployment mutation.
	service := &authServiceFake{NoAuthProvider: auth.NoAuthProvider{}, origin: "https://webhook.example.com"}
	handler := New(Dependencies{Store: &fakeStore{}, Attempts: &fakeAttempts{}, Provider: service, Auth: service})
	for _, origin := range []string{"", "https://evil.example.com", "https://webhook.example.com.evil.invalid"} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/deliveries/delivery-1/attempts", strings.NewReader(`{"expected_current_attempt_id":"attempt-1","operation":"retry"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", origin)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden || recorder.Body.String() != `{"error":{"code":"forbidden","message":"origin not allowed"}}`+"\n" {
			t.Fatalf("origin %q response = %d %q", origin, recorder.Code, recorder.Body.String())
		}
		if recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("origin %q Cache-Control = %q, want no-store", origin, recorder.Header().Get("Cache-Control"))
		}
	}
}

func TestDeleteEventsClearsHistoryWithPermissionAndExactOrigin(t *testing.T) {
	// Break caught: registering the destructive route without same-origin protection or not returning the deleted count.
	service := &authServiceFake{NoAuthProvider: auth.NoAuthProvider{}, origin: "https://webhook.example.com"}
	storeFake := &fakeStore{clearResult: store.ClearHistoryResult{Deleted: 2}}
	handler := New(Dependencies{Store: storeFake, Provider: service, Auth: service})
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/events", nil)
	request.Header.Set("Origin", service.origin)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"deleted":2}`+"\n" || !storeFake.clearCalled {
		t.Fatalf("delete events = %d %q called=%t", recorder.Code, recorder.Body.String(), storeFake.clearCalled)
	}
}

func TestDeleteEventsMapsClearHistoryErrors(t *testing.T) {
	// Break caught: leaking private database details or treating active history as an unavailable service.
	for _, test := range []struct {
		name     string
		err      error
		wantCode int
		wantBody string
	}{
		{name: "active", err: store.ErrHistoryActive, wantCode: http.StatusConflict, wantBody: `{"error":{"code":"history_active","message":"active deployments or queued events must finish first"}}` + "\n"},
		{name: "storage failure", err: errors.New("private database detail"), wantCode: http.StatusServiceUnavailable, wantBody: `{"error":{"code":"unavailable","message":"service unavailable"}}` + "\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			storeFake := &fakeStore{clearErr: test.err}
			handler := New(Dependencies{Store: storeFake, Provider: auth.NoAuthProvider{}})
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/api/v1/events", nil))
			if recorder.Code != test.wantCode || recorder.Body.String() != test.wantBody || !storeFake.clearCalled {
				t.Fatalf("delete events = %d %q called=%t", recorder.Code, recorder.Body.String(), storeFake.clearCalled)
			}
		})
	}
}

func TestDeleteEventsRejectsReadOnlyPrincipalWithoutClearingHistory(t *testing.T) {
	// Break caught: granting destructive access to a principal with only event-read permission.
	storeFake := &fakeStore{}
	provider := providerFunc(func(*http.Request) (auth.Principal, error) {
		return auth.Principal{ID: "reader", Permissions: map[string]bool{auth.PermissionEventsRead: true}}, nil
	})
	handler := New(Dependencies{Store: storeFake, Provider: provider})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/api/v1/events", nil))
	if recorder.Code != http.StatusForbidden || storeFake.clearCalled {
		t.Fatalf("delete events = %d called=%t", recorder.Code, storeFake.clearCalled)
	}
}

func TestDeleteEventsRejectsMissingOrDifferentOriginWithoutClearingHistory(t *testing.T) {
	// Break caught: accepting a cross-site request made with a valid management session.
	service := &authServiceFake{NoAuthProvider: auth.NoAuthProvider{}, origin: "https://webhook.example.com"}
	for _, origin := range []string{"", "https://evil.example.com"} {
		storeFake := &fakeStore{}
		handler := New(Dependencies{Store: storeFake, Provider: service, Auth: service})
		request := httptest.NewRequest(http.MethodDelete, "/api/v1/events", nil)
		request.Header.Set("Origin", origin)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden || storeFake.clearCalled {
			t.Fatalf("origin %q = %d called=%t", origin, recorder.Code, storeFake.clearCalled)
		}
	}
}

func TestSafeBusinessReadsDoNotRequireOrigin(t *testing.T) {
	service := &authServiceFake{NoAuthProvider: auth.NoAuthProvider{}, origin: "https://webhook.example.com"}
	handler := New(Dependencies{Store: &fakeStore{}, Provider: service, Auth: service})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/repositories", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("safe read status = %d, want downstream 503", recorder.Code)
	}
}

type authServiceFake struct {
	auth.NoAuthProvider
	origin string
	calls  []string
}

func (f *authServiceFake) ExpectedOrigin() string { return f.origin }
func (f *authServiceFake) Login(w http.ResponseWriter, _ *http.Request) {
	f.calls = append(f.calls, "login")
	w.WriteHeader(http.StatusNoContent)
}
func (f *authServiceFake) Callback(w http.ResponseWriter, _ *http.Request) {
	f.calls = append(f.calls, "callback")
	w.WriteHeader(http.StatusNoContent)
}
func (f *authServiceFake) Session(w http.ResponseWriter, _ *http.Request) {
	f.calls = append(f.calls, "session")
	w.WriteHeader(http.StatusNoContent)
}
func (f *authServiceFake) Logout(w http.ResponseWriter, _ *http.Request) {
	f.calls = append(f.calls, "logout")
	w.WriteHeader(http.StatusNoContent)
}

func TestEveryBusinessRouteUsesAuthenticationAndPermission(t *testing.T) {
	provider := providerFunc(func(*http.Request) (auth.Principal, error) { return auth.Principal{}, errors.New("missing") })
	handler := New(Dependencies{Store: &fakeStore{}, Attempts: &fakeAttempts{}, Provider: provider})
	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/repositories", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/connections", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/connections/primary/resources", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/targets", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/events", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/events/event-1", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/notifications", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/deliveries/delivery-1/attempts", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodDelete, "/api/v1/events", nil),
		httptest.NewRequest(http.MethodGet, "/api/v1/config/status", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/config/reload", nil),
	}
	for _, request := range requests {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, request)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s = %d", request.Method, request.URL.Path, rr.Code)
		}
	}
}

func TestNotificationsExposeCursorFeedAndValidateQuery(t *testing.T) {
	storeFake := &fakeStore{notificationPage: store.NotificationPage{Items: []store.NotificationFact{{
		ID: "2:audit:a", Cursor: "cursor-a", Category: "failure", Outcome: "timeout", EventID: "event-1",
		DeliveryID: "delivery-1", TargetID: "api", Provider: "gitlab", SourceID: "gitlab-a", Repository: "app", Summary: "Deployment needs attention", OccurredAt: time.Unix(2, 0).UTC(),
	}}, LatestCursor: "cursor-a"}}
	handler := New(Dependencies{Store: storeFake, Provider: auth.NoAuthProvider{}})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/notifications?after=cursor-old&limit=25", nil))
	want := `{"items":[{"id":"2:audit:a","cursor":"cursor-a","category":"failure","outcome":"timeout","event_id":"event-1","delivery_id":"delivery-1","target_id":"api","provider":"gitlab","source_id":"gitlab-a","repository":"app","summary":"Deployment needs attention","occurred_at":"1970-01-01T00:00:02Z"}],"latest_cursor":"cursor-a"}` + "\n"
	if rr.Code != http.StatusOK || rr.Body.String() != want || storeFake.notificationQuery.After != "cursor-old" || storeFake.notificationQuery.Limit != 25 {
		t.Fatalf("notifications = %d %s / %#v", rr.Code, rr.Body.String(), storeFake.notificationQuery)
	}
	for _, path := range []string{"/api/v1/notifications?unknown=x", "/api/v1/notifications?limit=0", "/api/v1/notifications?limit=101", "/api/v1/notifications?limit=x"} {
		rr = httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s", path, rr.Code, rr.Body.String())
		}
	}
}

func TestConnectionsExposeOnlySafeGenericSummariesAndDiscoveredResources(t *testing.T) {
	// Break caught: exposing configured origins/credentials/raw upstream data or failing to bind discovery to the path connection ID.
	controller := &fakeConfigController{
		connections: []runtimecfg.ConnectionSummary{
			{ID: "alpha", Type: "dokploy", Status: "configured", Capabilities: []string{"resource_discovery"}},
			{ID: "zulu", Type: "future", Status: "configured", Capabilities: []string{}},
		},
		resources: []runtimecfg.ConnectionResource{{
			Type: "compose", ProjectName: "Project", EnvironmentName: "Production",
			Name: "API", AppName: "api-abc", ResourceID: "compose-1", Status: "done",
		}},
	}
	handler := New(Dependencies{Config: controller, Provider: auth.NoAuthProvider{}})

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/connections", nil))
	wantConnections := `{"connections":[{"id":"alpha","type":"dokploy","status":"configured","capabilities":["resource_discovery"]},{"id":"zulu","type":"future","status":"configured","capabilities":[]}]}` + "\n"
	if rr.Code != http.StatusOK || rr.Body.String() != wantConnections {
		t.Fatalf("connections = %d %s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/connections/alpha/resources", nil))
	wantResources := `{"resources":[{"type":"compose","project_name":"Project","environment_name":"Production","name":"API","app_name":"api-abc","resource_id":"compose-1","status":"done"}]}` + "\n"
	if rr.Code != http.StatusOK || rr.Body.String() != wantResources || controller.resourceID != "alpha" {
		t.Fatalf("resources/id = %d %s / %q", rr.Code, rr.Body.String(), controller.resourceID)
	}
	for _, forbidden := range []string{"api_key", "token", "base_url", "environment-secret", "raw_response", "https://"} {
		if strings.Contains(strings.ToLower(wantConnections+wantResources), forbidden) {
			t.Fatalf("safe contract leaked %q", forbidden)
		}
	}
}

func TestTargetsExposeSafeConfiguredInventory(t *testing.T) {
	controller := &fakeConfigController{targets: runtimecfg.TargetInventory{
		RefreshedAt: time.Unix(10, 0).UTC(),
		Targets: []runtimecfg.TargetInventoryItem{{
			ID: "production", ConnectionID: "primary", ResourceType: "compose", ResourceID: "compose-1",
			ProjectName: "Project", EnvironmentName: "Production", Name: "API", AppName: "api-abc", Status: "done", Condition: runtimecfg.TargetConditionAvailable,
			RuntimeStatus: runtimecfg.TargetRuntimeAllRunning, Containers: []runtimecfg.TargetContainer{{Name: "api", State: "running", Health: "healthy", RestartCount: 1}},
		}},
		Errors: []runtimecfg.TargetInventoryError{},
	}}
	handler := New(Dependencies{Config: controller, Provider: auth.NoAuthProvider{}})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/targets", nil))
	want := `{"targets":[{"id":"production","connection_id":"primary","resource_type":"compose","resource_id":"compose-1","project_name":"Project","environment_name":"Production","name":"API","app_name":"api-abc","status":"done","condition":"available","runtime_status":"all_running","containers":[{"name":"api","state":"running","health":"healthy","restart_count":1}]}],"refreshed_at":"1970-01-01T00:00:10Z","errors":[]}` + "\n"
	if rr.Code != http.StatusOK || rr.Body.String() != want {
		t.Fatalf("targets = %d %s", rr.Code, rr.Body.String())
	}
	for _, forbidden := range []string{"api_key", "base_url", "token", "https://"} {
		if strings.Contains(strings.ToLower(rr.Body.String()), forbidden) {
			t.Fatalf("target inventory leaked %q: %s", forbidden, rr.Body.String())
		}
	}
}

func TestConnectionResourceDiscoveryMapsStableSafeErrors(t *testing.T) {
	// Break caught: leaking upstream errors or treating missing/unsupported configured connections as generic server failures.
	controller := &fakeConfigController{}
	handler := New(Dependencies{Config: controller, Provider: auth.NoAuthProvider{}})
	tests := []struct {
		name, code string
		err        error
		status     int
	}{
		{name: "missing", err: runtimecfg.ErrConnectionNotFound, status: http.StatusNotFound, code: "not_found"},
		{name: "capability unavailable", err: runtimecfg.ErrConnectionCapabilityUnavailable, status: http.StatusUnprocessableEntity, code: "capability_unavailable"},
		{name: "upstream", err: errors.New("https://private.example.invalid returned secret-token"), status: http.StatusBadGateway, code: "upstream_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller.resourceErr = test.err
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/connections/primary/resources", nil))
			if rr.Code != test.status || !strings.Contains(rr.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("response = %d %s", rr.Code, rr.Body.String())
			}
			for _, forbidden := range []string{"private.example.invalid", "secret-token"} {
				if strings.Contains(rr.Body.String(), forbidden) {
					t.Fatalf("error leaked %q: %s", forbidden, rr.Body.String())
				}
			}
		})
	}

	missingConfig := New(Dependencies{Provider: auth.NoAuthProvider{}})
	for _, path := range []string{"/api/v1/connections", "/api/v1/connections/primary/resources"} {
		rr := httptest.NewRecorder()
		missingConfig.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), `"code":"unavailable"`) {
			t.Fatalf("missing config %s = %d %s", path, rr.Code, rr.Body.String())
		}
	}
}

func TestConnectionsReadPermissionIsIndependentFromEventLedgerAccess(t *testing.T) {
	// Break caught: granting resource discovery to an event-only reader or requiring deployment mutation privileges for read-only configuration.
	controller := &fakeConfigController{connections: []runtimecfg.ConnectionSummary{}}
	eventReader := providerFunc(func(*http.Request) (auth.Principal, error) {
		return auth.Principal{ID: "event-reader", Permissions: map[string]bool{auth.PermissionEventsRead: true}}, nil
	})
	rr := httptest.NewRecorder()
	New(Dependencies{Config: controller, Provider: eventReader}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/connections", nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("event reader connections = %d %s", rr.Code, rr.Body.String())
	}

	connectionReader := providerFunc(func(*http.Request) (auth.Principal, error) {
		return auth.Principal{ID: "connection-reader", Permissions: map[string]bool{auth.PermissionConnectionsRead: true}}, nil
	})
	rr = httptest.NewRecorder()
	New(Dependencies{Config: controller, Provider: connectionReader}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/connections", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != `{"connections":[]}`+"\n" {
		t.Fatalf("connection reader connections = %d %s", rr.Code, rr.Body.String())
	}
}

func TestConfigStatusAndReloadExposeSafeStableContract(t *testing.T) {
	loadedAt := time.Date(2026, 8, 6, 1, 2, 3, 0, time.UTC)
	lastAt := loadedAt.Add(time.Minute)
	controller := &fakeConfigController{status: runtimecfg.Status{
		State: runtimecfg.StateWaiting, CurrentDigest: "current-digest", LoadedAt: loadedAt,
		PendingDigest: "pending-digest", Recovery: true, ActiveDeliveries: 2, QueuedEvents: 3,
		LastResult: &runtimecfg.LastResult{Code: "waiting_retry", At: lastAt},
	}}
	handler := New(Dependencies{Config: controller, Provider: auth.NoAuthProvider{}})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/config/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rr.Code, rr.Body.String())
	}
	for _, required := range []string{`"state":"waiting"`, `"current_digest":"current-digest"`, `"pending_digest":"pending-digest"`, `"recovery":true`, `"active_deliveries":2`, `"queued_events":3`, `"code":"waiting_retry"`} {
		if !strings.Contains(rr.Body.String(), required) {
			t.Fatalf("status missing %s: %s", required, rr.Body.String())
		}
	}
	for _, forbidden := range []string{"config.yaml", "token", "api_key", "base_url", "resource_id", "secret-value"} {
		if strings.Contains(strings.ToLower(rr.Body.String()), forbidden) {
			t.Fatalf("status leaked %q: %s", forbidden, rr.Body.String())
		}
	}

	tests := []struct {
		name    string
		outcome runtimecfg.ReloadOutcome
		err     error
		code    int
		stable  string
	}{
		{"applied", runtimecfg.ReloadApplied, nil, 200, "applied"},
		{"waiting", runtimecfg.ReloadWaiting, nil, 202, "waiting"},
		{"in progress", "", runtimecfg.ErrReloadInProgress, 409, "reload_in_progress"},
		{"invalid", "", runtimecfg.ErrInvalidConfig, 422, "invalid_config"},
		{"unavailable", "", runtimecfg.ErrPreWaitUnavailable, 503, "unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller.outcome, controller.reloadErr = tt.outcome, tt.err
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/config/reload", nil))
			if rr.Code != tt.code || !strings.Contains(rr.Body.String(), `"`+tt.stable+`"`) {
				t.Fatalf("reload = %d %s", rr.Code, rr.Body.String())
			}
			if controller.actor != "wireguard-anonymous" {
				t.Fatalf("actor = %q", controller.actor)
			}
		})
	}
}

func TestConfigReloadPermissionCanBeNarrowedIndependently(t *testing.T) {
	controller := &fakeConfigController{status: runtimecfg.Status{State: runtimecfg.StateIdle}}
	provider := providerFunc(func(*http.Request) (auth.Principal, error) {
		return auth.Principal{ID: "reader", Permissions: map[string]bool{auth.PermissionEventsRead: true}}, nil
	})
	handler := New(Dependencies{Config: controller, Provider: provider})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/config/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/config/reload", nil))
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), `"code":"forbidden"`) {
		t.Fatalf("reload = %d %s", rr.Code, rr.Body.String())
	}
}

func TestDetailBlocksOperationsForIncompatibleHistoricalTarget(t *testing.T) {
	now := time.Unix(1, 0).UTC()
	storeFake := &fakeStore{detail: store.EventDetail{
		EventSummary: store.EventSummary{ID: "event-1", ReceivedAt: now, EventType: "pipeline"},
		Deliveries: []store.DeliveryDetail{{
			ID: "delivery-1", TargetID: "prod", TargetSnapshot: []byte(`{"resource_id":"private"}`),
			CurrentAttemptID: "attempt-1", TransportStatus: "failed", DeploymentStatus: "not_started",
			AllowedOperations: []domain.Operation{domain.OperationRetry}, CreatedAt: now, UpdatedAt: now,
		}},
	}}
	controller := &fakeConfigController{binding: runtimecfg.BindingChanged}
	handler := New(Dependencies{Store: storeFake, Config: controller, Provider: auth.NoAuthProvider{}})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/events/event-1", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"target_binding_status":"target_changed"`) ||
		!strings.Contains(rr.Body.String(), `"allowed_operations":[]`) || !strings.Contains(rr.Body.String(), `"attention":true`) {
		t.Fatalf("detail = %d %s", rr.Code, rr.Body.String())
	}
	for _, forbidden := range []string{"private", "target_snapshot", "resource_id"} {
		if strings.Contains(rr.Body.String(), forbidden) {
			t.Fatalf("detail leaked %q: %s", forbidden, rr.Body.String())
		}
	}
}

func TestDetailExposesPipelineSourceStateHistory(t *testing.T) {
	now := time.Unix(1, 0).UTC()
	storeFake := &fakeStore{detail: store.EventDetail{
		EventSummary: store.EventSummary{ID: "event-2", ReceivedAt: now, EventType: "pipeline"},
		SourceStates: []store.SourceState{
			{EventID: "event-1", Status: "pending", ReceivedAt: now},
			{EventID: "event-2", Status: "success", ReceivedAt: now.Add(time.Second)},
		},
	}}
	handler := New(Dependencies{Store: storeFake, Provider: auth.NoAuthProvider{}})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/events/event-2", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"source_states":[{"event_id":"event-1","status":"pending","received_at":"1970-01-01T00:00:01Z"},{"event_id":"event-2","status":"success","received_at":"1970-01-01T00:00:02Z"}]`) {
		t.Fatalf("detail source states = %d %s", rr.Code, rr.Body.String())
	}
}

func TestReadRoutesMapMissingStoreToUnavailable(t *testing.T) {
	handler := New(Dependencies{Provider: auth.NoAuthProvider{}})
	for _, path := range []string{"/api/v1/repositories", "/api/v1/events", "/api/v1/events/event-1"} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), `"code":"unavailable"`) {
			t.Fatalf("%s = %d %s", path, rr.Code, rr.Body.String())
		}
	}
}

func TestRepositoriesAndEventsExposeCanonicalContractAndParseExactFilters(t *testing.T) {
	now := time.Date(2026, 8, 5, 1, 2, 3, 0, time.FixedZone("offset", 8*3600))
	rule := "rule-1"
	transport, deployment := "unknown", "locating"
	ref, status, externalID, trigger, revision, commitMessage := "main", "success", "run-99", "push", "deadbeef", "fix checkout timeout"
	storeFake := &fakeStore{page: store.EventPage{
		Items: []store.EventSummary{{ID: "event-1", ReceivedAt: now, EventType: "pipeline", Provider: "gitlab", SourceID: "gitlab-a", Repository: "app", Ref: &ref, Status: &status, ExternalID: &externalID, Trigger: &trigger, Revision: &revision, CommitMessage: &commitMessage, RoutingResult: "deploy", RuleID: &rule, DeliverySummary: store.DeliverySummary{Kind: "uniform", Count: 1, TransportStatus: &transport, DeploymentStatus: &deployment}, Deliveries: []store.DeliveryListItem{{ID: "delivery-1", TargetID: "production", CurrentAttemptID: "attempt-1", TransportStatus: "unknown", DeploymentStatus: "locating", Active: true, Attention: true, AllowedOperations: []domain.Operation{domain.OperationRetry}}}, Active: true, Attention: true}},
		Page:  2, PageSize: 10, Total: 11, HasPrevious: true, HasNext: false, Active: true,
	}}
	controller := &fakeConfigController{repositories: []store.ConfiguredRepository{
		{Provider: "github", SourceID: "github-a", ID: "app", Name: "Application"},
		{Provider: "gitlab", SourceID: "gitlab-a", ID: "app", Name: "Application"},
	}}
	handler := New(Dependencies{Store: storeFake, Config: controller, Provider: auth.NoAuthProvider{}})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/repositories", nil))
	if rr.Code != 200 || rr.Body.String() != `{"repositories":[{"provider":"github","source_id":"github-a","id":"app","name":"Application"},{"provider":"gitlab","source_id":"gitlab-a","id":"app","name":"Application"}]}`+"\n" {
		t.Fatalf("repositories = %d %s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/events?source=gitlab-a&repository=app&target=production&unmatched=false&ref=main&rule=rule-1&id=event-1&status=transport:unknown&status=deployment:locating&page=2&page_size=10", nil))
	if rr.Code != 200 {
		t.Fatalf("events = %d %s", rr.Code, rr.Body.String())
	}
	if storeFake.query.Source == nil || *storeFake.query.Source != "gitlab-a" || storeFake.query.Repository == nil || *storeFake.query.Repository != "app" || storeFake.query.Target == nil || *storeFake.query.Target != "production" || storeFake.query.Unmatched == nil || *storeFake.query.Unmatched || storeFake.query.Ref == nil || *storeFake.query.Ref != "main" || storeFake.query.Rule == nil || *storeFake.query.Rule != "rule-1" || storeFake.query.ID != "event-1" || storeFake.query.TransportStatus == nil || *storeFake.query.TransportStatus != "unknown" || storeFake.query.DeploymentStatus == nil || *storeFake.query.DeploymentStatus != "locating" || storeFake.query.Page != 2 || storeFake.query.PageSize != 10 {
		t.Fatalf("query = %#v", storeFake.query)
	}
	want := `{"items":[{"id":"event-1","received_at":"2026-08-04T17:02:03Z","event_type":"pipeline","provider":"gitlab","source_id":"gitlab-a","repository":"app","ref":"main","status":"success","revision":"deadbeef","commit_message":"fix checkout timeout","external_id":"run-99","trigger":"push","routing_result":"deploy","rule_id":"rule-1","delivery_summary":{"kind":"uniform","count":1,"transport_status":"unknown","deployment_status":"locating"},"deliveries":[{"id":"delivery-1","target_id":"production","current_attempt_id":"attempt-1","transport_status":"unknown","deployment_status":"locating","active":true,"attention":true,"allowed_operations":["retry"]}],"active":true,"attention":true}],"page":2,"page_size":10,"total":11,"has_previous":true,"has_next":false,"active":true}` + "\n"
	if rr.Body.String() != want {
		t.Fatalf("events body = %s", rr.Body.String())
	}
	for _, legacy := range []string{`"project"`, `"branch"`, `"gitlab_status"`, `"pipeline_id"`, `"pipeline_source"`, `"sha"`} {
		if strings.Contains(rr.Body.String(), legacy) {
			t.Fatalf("events retained legacy key %s: %s", legacy, rr.Body.String())
		}
	}
}

func TestEventsExposeNullStringsAndAcceptMaximumPage(t *testing.T) {
	storeFake := &fakeStore{page: store.EventPage{
		Items: []store.EventSummary{},
		Page:  math.MaxInt, PageSize: 50, Total: 1, HasPrevious: true,
	}}
	handler := New(Dependencies{Store: storeFake, Provider: auth.NoAuthProvider{}})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/events?page="+strconv.Itoa(math.MaxInt), nil))
	if rr.Code != http.StatusOK || storeFake.query.Page != math.MaxInt {
		t.Fatalf("maximum page = %d query %#v body %s", rr.Code, storeFake.query, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"items":[]`) {
		t.Fatalf("maximum page returned items: %s", rr.Body.String())
	}

	storeFake.page = store.EventPage{Items: []store.EventSummary{{ID: "event-null", ReceivedAt: time.Unix(1, 0).UTC(), EventType: "pipeline", DeliverySummary: store.DeliverySummary{Kind: "none"}}}, Page: 1, PageSize: 50, Total: 1}
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/events", nil))
	for _, field := range []string{"ref", "status", "revision", "commit_message", "external_id", "trigger"} {
		if !strings.Contains(rr.Body.String(), `"`+field+`":null`) {
			t.Fatalf("%s did not preserve null: %s", field, rr.Body.String())
		}
	}
}

func TestEventsRejectInvalidQueries(t *testing.T) {
	handler := New(Dependencies{Store: &fakeStore{}, Provider: auth.NoAuthProvider{}})
	for _, query := range []string{
		"?unknown=x", "?project=app", "?source=gitlab-a", "?repository=app", "?source=gitlab-a&source=gitlab-b&repository=app", "?unmatched=yes", "?ref=", "?status=bad:x", "?status=transport:", "?status=transport:failed&status=transport:unknown", "?status=transport:nope", "?page=0", "?page_size=201", "?page=1&page=2",
	} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/events"+query, nil))
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), `"code":"invalid_query"`) {
			t.Fatalf("query %q = %d %s", query, rr.Code, rr.Body.String())
		}
	}
}

func TestEventsAcceptUnrecognizedDeploymentFilter(t *testing.T) {
	// Break caught: rejecting the active unrecognized deployment state exposed by the management API.
	storeFake := &fakeStore{page: store.EventPage{Items: []store.EventSummary{}, Page: 1, PageSize: 50}}
	handler := New(Dependencies{Store: storeFake, Provider: auth.NoAuthProvider{}})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/events?status=deployment:unrecognized", nil))
	if rr.Code != http.StatusOK || storeFake.query.DeploymentStatus == nil || *storeFake.query.DeploymentStatus != "unrecognized" {
		t.Fatalf("unrecognized filter = %d/%#v/%s", rr.Code, storeFake.query, rr.Body.String())
	}
}

func TestDetailIntersectsAllowedOperationsWithPrincipalPermission(t *testing.T) {
	now := time.Unix(1, 0).UTC()
	deployCommand := "curl --request POST --url 'https://dokploy.example.invalid/api/compose.deploy'"
	storeFake := &fakeStore{detail: store.EventDetail{EventSummary: store.EventSummary{ID: "event-1", ReceivedAt: now, EventType: "pipeline", DeliverySummary: store.DeliverySummary{Kind: "uniform", Count: 1}}, Deliveries: []store.DeliveryDetail{{ID: "delivery-1", TargetID: "prod", CurrentAttemptID: "attempt-1", TransportStatus: "failed", DeploymentStatus: "not_started", Attention: true, AllowedOperations: []domain.Operation{domain.OperationRetry}, CreatedAt: now, UpdatedAt: now, Attempts: []store.AttemptDetail{{ID: "attempt-1", Kind: "initial", Current: true, DeployCommand: &deployCommand, CreatedAt: now}}}}, Activities: []store.ActivityDetail{}}}
	provider := providerFunc(func(*http.Request) (auth.Principal, error) {
		return auth.Principal{ID: "reader", Permissions: map[string]bool{auth.PermissionEventsRead: true}}, nil
	})
	handler := New(Dependencies{Store: storeFake, Provider: provider})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/events/event-1", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"allowed_operations":[]`) || strings.Contains(rr.Body.String(), `"allowed_operations":["retry"]`) {
		t.Fatalf("detail = %d %s", rr.Code, rr.Body.String())
	}
	for _, required := range []string{`"ref":null`, `"status":null`, `"revision":null`, `"external_id":null`, `"trigger":null`, `"headers":null`, `"payload":null`, `"rule_snapshot":null`, `"request_at":null`} {
		if !strings.Contains(rr.Body.String(), required) {
			t.Fatalf("detail missing %s: %s", required, rr.Body.String())
		}
	}
	if !strings.Contains(rr.Body.String(), `"deploy_command":"curl --request POST --url 'https://dokploy.example.invalid/api/compose.deploy'"`) {
		t.Fatalf("detail missing deploy command: %s", rr.Body.String())
	}
	for _, forbidden := range []string{"target_snapshot", "connection", "base_url", "resource_id"} {
		if strings.Contains(strings.ToLower(rr.Body.String()), forbidden) {
			t.Fatalf("detail leaked %s: %s", forbidden, rr.Body.String())
		}
	}
	storeFake.detailErr = sql.ErrNoRows
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/events/missing", nil))
	if rr.Code != 404 || !strings.Contains(rr.Body.String(), `"code":"not_found"`) {
		t.Fatalf("missing = %d %s", rr.Code, rr.Body.String())
	}
}

func TestManualAttemptUsesPrincipalActorAndMapsTypedErrors(t *testing.T) {
	attempts := &fakeAttempts{result: service.AttemptResult{AttemptID: "attempt-new", Created: true}}
	handler := New(Dependencies{Store: &fakeStore{}, Attempts: attempts, Provider: auth.NoAuthProvider{}})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/deliveries/delivery-1/attempts", strings.NewReader(`{"expected_current_attempt_id":"attempt-old","operation":"retry","reason":"operator request"}`))
	request.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, request)
	if rr.Code != 201 || rr.Body.String() != `{"delivery_id":"delivery-1","attempt_id":"attempt-new"}`+"\n" {
		t.Fatalf("created = %d %s", rr.Code, rr.Body.String())
	}
	if attempts.actor != "wireguard-anonymous" || attempts.expected != "attempt-old" || attempts.operation != domain.OperationRetry || attempts.reason != "operator request" {
		t.Fatalf("execute args = %#v", attempts)
	}

	for _, test := range []struct {
		err    error
		code   int
		stable string
	}{
		{service.ErrOperationNotAllowed, 409, "operation_not_allowed"}, {service.ErrDeploymentStillActive, 409, "deployment_still_active"}, {service.ErrAlreadySucceeded, 409, "already_succeeded"}, {service.ErrStateChanged, 409, "state_changed"}, {service.ErrReconciliationUnavailable, 503, "reconciliation_unavailable"},
		{service.ErrConfigReloadInProgress, 409, "config_reload_in_progress"}, {service.ErrTargetChanged, 409, "target_changed"}, {service.ErrTargetUnavailable, 409, "target_unavailable"},
	} {
		attempts.err = test.err
		attempts.result = service.AttemptResult{}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/deliveries/delivery-1/attempts", strings.NewReader(`{"expected_current_attempt_id":"attempt-old","operation":"redeploy"}`))
		req.Header.Set("Content-Type", "application/json")
		rr = httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != test.code || !strings.Contains(rr.Body.String(), `"code":"`+test.stable+`"`) {
			t.Fatalf("%v = %d %s", test.err, rr.Code, rr.Body.String())
		}
	}
}

func TestManualAttemptRejectsUnsafeBodies(t *testing.T) {
	handler := New(Dependencies{Store: &fakeStore{}, Attempts: &fakeAttempts{}, Provider: auth.NoAuthProvider{}})
	tests := []struct {
		contentType, body string
		code              int
		stable            string
	}{
		{"text/plain", `{}`, 415, "unsupported_media_type"},
		{"application/json", `{"expected_current_attempt_id":"a","operation":"retry","actor":"forged"}`, 400, "invalid_request"},
		{"application/json", `{"expected_current_attempt_id":"a","operation":"retry"} {}`, 400, "invalid_request"},
		{"application/json", `{"expected_current_attempt_id":"","operation":"retry"}`, 400, "invalid_request"},
		{"application/json", `{"expected_current_attempt_id":"a","operation":"bad"}`, 400, "invalid_request"},
	}
	for _, test := range tests {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/deliveries/d/attempts", strings.NewReader(test.body))
		req.Header.Set("Content-Type", test.contentType)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != test.code || !strings.Contains(rr.Body.String(), `"code":"`+test.stable+`"`) {
			t.Fatalf("%q = %d %s", test.body, rr.Code, rr.Body.String())
		}
	}
	large := `{"expected_current_attempt_id":"a","operation":"retry","reason":"` + strings.Repeat("x", 64*1024) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/deliveries/d/attempts", strings.NewReader(large))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != 413 || !strings.Contains(rr.Body.String(), `"code":"payload_too_large"`) {
		t.Fatalf("large = %d %s", rr.Code, rr.Body.String())
	}
}
