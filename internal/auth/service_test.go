package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSessionCookieRoundTripKeepsIdentityEncryptedAndGrantsAllPermissions(t *testing.T) {
	now := time.Date(2026, 8, 12, 8, 0, 0, 0, time.UTC)
	service := newTestOIDCService(t, &fakeOIDCBackend{}, func() time.Time { return now })
	actorID := "authentik:test-user"
	displayName := "Test User"
	username := "test-user"
	recorder := httptest.NewRecorder()
	service.issueSession(recorder, sessionData{
		ActorID: actorID, DisplayName: displayName, Username: username, Provider: "authentik",
	})
	cookie := responseCookie(t, recorder, sessionCookieName)
	for _, forbidden := range []string{actorID, displayName, username} {
		if strings.Contains(cookie.Value, forbidden) {
			t.Fatalf("encrypted cookie contains %q: %s", forbidden, cookie.Value)
		}
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("cookie attributes = %#v", cookie)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	request.AddCookie(cookie)
	principal, err := service.Authenticate(request)
	if err != nil {
		t.Fatal(err)
	}
	if principal.ID != actorID || principal.DisplayName != displayName {
		t.Fatalf("principal = %#v", principal)
	}
	for _, permission := range []string{PermissionEventsRead, PermissionEventsDelete, PermissionConnectionsRead, PermissionDeploymentsRetry, PermissionConfigReload} {
		if !principal.Permissions[permission] {
			t.Fatalf("principal missing %q: %#v", permission, principal)
		}
	}
}

func TestSessionEndpointReturnsOnlyApprovedUserProjection(t *testing.T) {
	now := time.Date(2026, 8, 12, 8, 0, 0, 0, time.UTC)
	service := newTestOIDCService(t, &fakeOIDCBackend{}, func() time.Time { return now })
	login := httptest.NewRecorder()
	service.issueSession(login, sessionData{
		ActorID: "authentik:sensitive-subject", DisplayName: "DJ", Username: "dj", Provider: "authentik",
	})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	request.AddCookie(responseCookie(t, login, sessionCookieName))
	recorder := httptest.NewRecorder()

	service.Session(recorder, request)

	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response = %d headers=%v", recorder.Code, recorder.Header())
	}
	want := `{"user":{"display_name":"DJ","username":"dj","provider":"authentik"},"expires_at":"2026-08-12T16:00:00Z"}` + "\n"
	if recorder.Body.String() != want {
		t.Fatalf("body = %q, want %q", recorder.Body.String(), want)
	}
	for _, forbidden := range []string{"sensitive-subject", "groups", "email", "token", "permission"} {
		if strings.Contains(strings.ToLower(recorder.Body.String()), forbidden) {
			t.Fatalf("session response contains %q: %s", forbidden, recorder.Body.String())
		}
	}
}

func TestInvalidAndExpiredSessionCookiesReturnUnauthorizedAndAreCleared(t *testing.T) {
	now := time.Date(2026, 8, 12, 8, 0, 0, 0, time.UTC)
	service := newTestOIDCService(t, &fakeOIDCBackend{}, func() time.Time { return now })
	login := httptest.NewRecorder()
	service.issueSession(login, sessionData{ActorID: "actor", DisplayName: "DJ", Provider: "authentik"})
	valid := responseCookie(t, login, sessionCookieName)

	tests := []struct {
		name   string
		cookie *http.Cookie
		move   time.Duration
	}{
		{name: "tampered", cookie: &http.Cookie{Name: valid.Name, Value: valid.Value + "tampered"}},
		{name: "expired", cookie: valid, move: 8*time.Hour + time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now = now.Add(test.move)
			request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
			request.AddCookie(test.cookie)
			recorder := httptest.NewRecorder()
			service.Session(recorder, request)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", recorder.Code)
			}
			cleared := responseCookie(t, recorder, sessionCookieName)
			if cleared.MaxAge >= 0 {
				t.Fatalf("cleared cookie = %#v", cleared)
			}
		})
	}
}

func TestDevelopmentServiceReturnsStableLocalIdentityWithoutLogout(t *testing.T) {
	service := NewDevelopmentService()
	recorder := httptest.NewRecorder()
	service.Session(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"display_name":"Local development"`) || !strings.Contains(recorder.Body.String(), `"provider":"local"`) {
		t.Fatalf("development session = %d %s", recorder.Code, recorder.Body.String())
	}
	logout := httptest.NewRecorder()
	service.Logout(logout, httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil))
	if logout.Code != http.StatusNotFound {
		t.Fatalf("development logout status = %d, want 404", logout.Code)
	}
}

func TestDevelopmentAuthenticationEndpointsAreNeverCached(t *testing.T) {
	// Break caught: a proxy caches a local-mode authentication response and reuses it after secure mode is restored.
	service := NewDevelopmentService()
	tests := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		method  string
		path    string
	}{
		{name: "login", handler: service.Login, method: http.MethodGet, path: "/api/v1/auth/login"},
		{name: "callback", handler: service.Callback, method: http.MethodGet, path: "/api/v1/auth/callback"},
		{name: "session", handler: service.Session, method: http.MethodGet, path: "/api/v1/auth/session"},
		{name: "logout", handler: service.Logout, method: http.MethodPost, path: "/api/v1/auth/logout"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			test.handler(recorder, httptest.NewRequest(test.method, test.path, nil))
			if recorder.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestBearerTokenWithoutSessionIsUnauthorized(t *testing.T) {
	// Break caught: accidentally treating an OAuth access token as management API authentication.
	service := newTestOIDCService(t, &fakeOIDCBackend{}, time.Now)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	request.Header.Set("Authorization", "Bearer access-token-must-not-authenticate")
	if principal, err := service.Authenticate(request); err == nil || principal.ID != "" {
		t.Fatalf("Bearer-only authentication = %#v/%v, want rejection", principal, err)
	}
}

func TestLoginCreatesEncryptedStateNonceAndPKCETransaction(t *testing.T) {
	now := time.Date(2026, 8, 12, 8, 0, 0, 0, time.UTC)
	backend := &fakeOIDCBackend{}
	service := newTestOIDCService(t, backend, func() time.Time { return now })
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/login?return_to=%2F%3Fview%3Dtargets", nil)

	service.Login(recorder, request)

	if recorder.Code != http.StatusFound || !strings.HasPrefix(recorder.Header().Get("Location"), "https://authentik.example.invalid/authorize?") {
		t.Fatalf("login response = %d location=%q", recorder.Code, recorder.Header().Get("Location"))
	}
	transactionCookie := responseCookie(t, recorder, loginCookieName)
	if !transactionCookie.HttpOnly || !transactionCookie.Secure || transactionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("transaction cookie = %#v", transactionCookie)
	}
	transaction, err := service.decodeLoginCookie(transactionCookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	if transaction.State == "" || transaction.Nonce == "" || transaction.Verifier == "" || transaction.ReturnTo != "/?view=targets" {
		t.Fatalf("transaction = %#v", transaction)
	}
	digest := sha256.Sum256([]byte(transaction.Verifier))
	wantChallenge := base64.RawURLEncoding.EncodeToString(digest[:])
	if backend.authorization.State != transaction.State || backend.authorization.Nonce != transaction.Nonce || backend.authorization.CodeChallenge != wantChallenge {
		t.Fatalf("authorization = %#v, transaction = %#v", backend.authorization, transaction)
	}
}

func TestLoginRejectsOpenRedirectTargets(t *testing.T) {
	for _, unsafe := range []string{"https://evil.example/", "//evil.example/", "/\\evil", "/%0aLocation:evil"} {
		t.Run(unsafe, func(t *testing.T) {
			service := newTestOIDCService(t, &fakeOIDCBackend{}, time.Now)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/login?return_to="+url.QueryEscape(unsafe), nil)
			service.Login(recorder, request)
			transaction, err := service.decodeLoginCookie(responseCookie(t, recorder, loginCookieName).Value)
			if err != nil {
				t.Fatal(err)
			}
			if transaction.ReturnTo != "/" {
				t.Fatalf("return_to = %q, want /", transaction.ReturnTo)
			}
		})
	}
}

func TestCallbackRequiresMatchingTransactionSubjectsNonceAndAllowedGroup(t *testing.T) {
	now := time.Date(2026, 8, 12, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		mutate     func(*loginTransaction, *fakeOIDCBackend)
		wantStatus int
	}{
		{name: "success", wantStatus: http.StatusFound},
		{name: "state mismatch", mutate: func(transaction *loginTransaction, _ *fakeOIDCBackend) { transaction.State = "different" }, wantStatus: http.StatusUnauthorized},
		{name: "nonce mismatch", mutate: func(_ *loginTransaction, backend *fakeOIDCBackend) { backend.identity.Nonce = "different" }, wantStatus: http.StatusUnauthorized},
		{name: "subject mismatch", mutate: func(_ *loginTransaction, backend *fakeOIDCBackend) { backend.identity.UserInfoSubject = "different" }, wantStatus: http.StatusUnauthorized},
		{name: "group denied", mutate: func(_ *loginTransaction, backend *fakeOIDCBackend) { backend.identity.Groups = []string{"other"} }, wantStatus: http.StatusForbidden},
		{name: "groups claim denied", mutate: func(_ *loginTransaction, backend *fakeOIDCBackend) {
			backend.exchangeErr = testAuthorizationDeniedError{}
		}, wantStatus: http.StatusForbidden},
		{name: "exchange failure", mutate: func(_ *loginTransaction, backend *fakeOIDCBackend) {
			backend.exchangeErr = errors.New("upstream token leaked")
		}, wantStatus: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := &fakeOIDCBackend{identity: oidcIdentity{
				TokenSubject: "subject-1", UserInfoSubject: "subject-1", Nonce: "nonce",
				Name: "Display Name", Username: "dj", Groups: []string{"hookfly-users"},
			}}
			service := newTestOIDCService(t, backend, func() time.Time { return now })
			transaction := loginTransaction{Version: 1, State: "state", Nonce: "nonce", Verifier: "verifier", ReturnTo: "/?view=targets", IssuedAt: now.Unix(), ExpiresAt: now.Add(10 * time.Minute).Unix()}
			requestState := transaction.State
			if test.mutate != nil {
				test.mutate(&transaction, backend)
			}
			cookieValue, err := service.encodeLoginCookie(transaction)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/callback?code=code&state="+url.QueryEscape(requestState), nil)
			request.AddCookie(&http.Cookie{Name: loginCookieName, Value: cookieValue})
			recorder := httptest.NewRecorder()

			service.Callback(recorder, request)

			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), test.wantStatus)
			}
			if strings.Contains(recorder.Body.String(), "upstream token leaked") {
				t.Fatalf("callback exposed provider error: %s", recorder.Body.String())
			}
			if test.wantStatus == http.StatusForbidden {
				if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Content-Type") != "text/html; charset=utf-8" {
					t.Fatalf("denial headers = %v", recorder.Header())
				}
				if recorder.Body.String() != "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>Access denied</title></head><body><h1>Access denied</h1><p>Your account is not allowed to use Hookfly.</p></body></html>\n" {
					t.Fatalf("denial body = %q", recorder.Body.String())
				}
			}
			if test.wantStatus == http.StatusFound {
				if recorder.Header().Get("Location") != "/?view=targets" {
					t.Fatalf("redirect = %q", recorder.Header().Get("Location"))
				}
				if backend.exchange.Code != "code" || backend.exchange.Verifier != "verifier" {
					t.Fatalf("exchange = %#v", backend.exchange)
				}
				responseCookie(t, recorder, sessionCookieName)
			} else if findResponseCookie(recorder, sessionCookieName) != nil {
				t.Fatal("failed callback created a session")
			}
		})
	}
}

func TestCallbackClearsLoginTransactionBeforeCommittingRedirect(t *testing.T) {
	// Break caught: clearing the one-time transaction after Redirect commits headers leaves a reusable login cookie in real clients.
	now := time.Date(2026, 8, 12, 8, 0, 0, 0, time.UTC)
	backend := &fakeOIDCBackend{identity: oidcIdentity{
		TokenSubject: "subject-1", UserInfoSubject: "subject-1", Nonce: "nonce",
		Username: "dj", Groups: []string{"hookfly-users"},
	}}
	service := newTestOIDCService(t, backend, func() time.Time { return now })
	transaction := loginTransaction{Version: 1, State: "state", Nonce: "nonce", Verifier: "verifier", ReturnTo: "/", IssuedAt: now.Unix(), ExpiresAt: now.Add(loginLifetime).Unix()}
	cookieValue, err := service.encodeLoginCookie(transaction)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/callback?code=code&state=state", nil)
	request.AddCookie(&http.Cookie{Name: loginCookieName, Value: cookieValue})
	writer := newTrackingResponseWriter()

	service.Callback(writer, request)

	if len(writer.statuses) != 1 || writer.statuses[0] != http.StatusFound {
		t.Fatalf("status writes = %v, want [302]", writer.statuses)
	}
	if !headerClearsCookie(writer.committedHeader, loginCookieName) {
		t.Fatalf("committed Set-Cookie headers do not clear %q: %v", loginCookieName, writer.committedHeader.Values("Set-Cookie"))
	}
}

func TestCallbackStopsAfterSessionCookieEncodingFailure(t *testing.T) {
	// Break caught: redirecting after a failed session write attempts to send two incompatible responses.
	now := time.Date(2026, 8, 12, 8, 0, 0, 0, time.UTC)
	backend := &fakeOIDCBackend{identity: oidcIdentity{
		TokenSubject: "subject-1", UserInfoSubject: "subject-1", Nonce: "nonce",
		Username: "dj", Groups: []string{"hookfly-users"},
	}}
	service := newTestOIDCService(t, backend, func() time.Time { return now })
	service.sessionCodec.MaxLength(1)
	transaction := loginTransaction{Version: 1, State: "state", Nonce: "nonce", Verifier: "verifier", ReturnTo: "/", IssuedAt: now.Unix(), ExpiresAt: now.Add(loginLifetime).Unix()}
	cookieValue, err := service.encodeLoginCookie(transaction)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/callback?code=code&state=state", nil)
	request.AddCookie(&http.Cookie{Name: loginCookieName, Value: cookieValue})
	writer := newTrackingResponseWriter()

	service.Callback(writer, request)

	if len(writer.statuses) != 1 || writer.statuses[0] != http.StatusInternalServerError {
		t.Fatalf("status writes = %v, want [500]", writer.statuses)
	}
	if writer.committedHeader.Get("Location") != "" {
		t.Fatalf("failed callback redirected to %q", writer.committedHeader.Get("Location"))
	}
}

func TestCallbackUsesPreferredUsernameThenNameThenFallback(t *testing.T) {
	for _, test := range []struct {
		name     string
		identity oidcIdentity
		want     string
	}{
		{name: "username", identity: oidcIdentity{Username: "dj", Name: "Display Name"}, want: "dj"},
		{name: "name", identity: oidcIdentity{Name: "Display Name"}, want: "Display Name"},
		{name: "fallback", identity: oidcIdentity{}, want: "Authenticated user"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := displayName(test.identity); got != test.want {
				t.Fatalf("displayName() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestUserInfoClaimsRequireStringGroups(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		ok   bool
	}{
		{name: "valid", raw: `{"sub":"s","groups":["hookfly-users"],"preferred_username":"dj"}`, ok: true},
		{name: "missing", raw: `{"sub":"s"}`},
		{name: "string", raw: `{"sub":"s","groups":"hookfly-users"}`},
		{name: "mixed", raw: `{"sub":"s","groups":["hookfly-users",7]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseUserInfoClaims(json.RawMessage(test.raw))
			if (err == nil) != test.ok {
				t.Fatalf("parseUserInfoClaims() error = %v, ok=%v", err, test.ok)
			}
			var denied interface{ authorizationDenied() }
			if !test.ok && !errors.As(err, &denied) {
				t.Fatalf("parseUserInfoClaims() error = %v, want authorization denial", err)
			}
		})
	}
}

type fakeOIDCBackend struct {
	authorization authorizationRequest
	exchange      exchangeRequest
	identity      oidcIdentity
	exchangeErr   error
}

type testAuthorizationDeniedError struct{}

func (testAuthorizationDeniedError) Error() string        { return "authorization denied" }
func (testAuthorizationDeniedError) authorizationDenied() {}

func (f *fakeOIDCBackend) AuthorizationURL(request authorizationRequest) string {
	f.authorization = request
	query := url.Values{"state": {request.State}, "nonce": {request.Nonce}, "code_challenge": {request.CodeChallenge}}
	return "https://authentik.example.invalid/authorize?" + query.Encode()
}

func (f *fakeOIDCBackend) Exchange(_ context.Context, request exchangeRequest) (oidcIdentity, error) {
	f.exchange = request
	return f.identity, f.exchangeErr
}

func newTestOIDCService(t *testing.T, backend oidcBackend, now func() time.Time) *OIDCService {
	t.Helper()
	settings, err := LoadSettings(mapLookup(validOIDCEnvironment()))
	if err != nil {
		t.Fatal(err)
	}
	return newOIDCService(settings, backend, now, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func responseCookie(t *testing.T, recorder *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	cookie := findResponseCookie(recorder, name)
	if cookie == nil {
		t.Fatalf("response cookie %q missing: %v", name, recorder.Header())
	}
	return cookie
}

func findResponseCookie(recorder *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

type trackingResponseWriter struct {
	header          http.Header
	committedHeader http.Header
	statuses        []int
	body            bytes.Buffer
}

func newTrackingResponseWriter() *trackingResponseWriter {
	return &trackingResponseWriter{header: make(http.Header)}
}

func (w *trackingResponseWriter) Header() http.Header { return w.header }

func (w *trackingResponseWriter) WriteHeader(status int) {
	w.statuses = append(w.statuses, status)
	if w.committedHeader == nil {
		w.committedHeader = w.header.Clone()
	}
}

func (w *trackingResponseWriter) Write(body []byte) (int, error) {
	if len(w.statuses) == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(body)
}

func headerClearsCookie(header http.Header, name string) bool {
	response := &http.Response{Header: header}
	for _, cookie := range response.Cookies() {
		if cookie.Name == name && cookie.MaxAge < 0 {
			return true
		}
	}
	return false
}
