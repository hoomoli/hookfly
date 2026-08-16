package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type providerFunc func(*http.Request) (Principal, error)

func (f providerFunc) Authenticate(r *http.Request) (Principal, error) { return f(r) }

func TestNoAuthProviderInjectsStablePrincipalAndRequirePassesActor(t *testing.T) {
	var actor string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			t.Fatal("principal missing from context")
		}
		actor = principal.ID
		w.WriteHeader(http.StatusNoContent)
	})
	handler := Authenticate(NoAuthProvider{}, Require(PermissionDeploymentsRetry, next))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusNoContent || actor != "wireguard-anonymous" {
		t.Fatalf("response/actor = %d/%q", rr.Code, actor)
	}
}

func TestNoAuthProviderGrantsConfigReloadForFutureProviderSeparation(t *testing.T) {
	principal, err := (NoAuthProvider{}).Authenticate(httptest.NewRequest(http.MethodPost, "/api/v1/config/reload", nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, permission := range []string{PermissionEventsRead, PermissionEventsDelete, PermissionConnectionsRead, PermissionDeploymentsRetry, PermissionConfigReload} {
		if !principal.Permissions[permission] {
			t.Fatalf("permission %q is missing", permission)
		}
	}
}

func TestAuthenticationAndAuthorizationUseStableEnvelopes(t *testing.T) {
	tests := []struct {
		name     string
		provider Provider
		wantCode int
		wantBody string
	}{
		{
			name: "missing principal",
			provider: providerFunc(func(*http.Request) (Principal, error) {
				return Principal{}, errors.New("credentials are absent")
			}),
			wantCode: http.StatusUnauthorized,
			wantBody: `{"error":{"code":"unauthorized","message":"authentication required"}}` + "\n",
		},
		{
			name: "permission denied",
			provider: providerFunc(func(*http.Request) (Principal, error) {
				return Principal{ID: "reader", Permissions: map[string]bool{PermissionEventsRead: true}}, nil
			}),
			wantCode: http.StatusForbidden,
			wantBody: `{"error":{"code":"forbidden","message":"permission denied"}}` + "\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := Authenticate(test.provider, Require(PermissionDeploymentsRetry, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("protected handler called")
			})))
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
			if rr.Code != test.wantCode || rr.Body.String() != test.wantBody {
				t.Fatalf("response = %d %q", rr.Code, rr.Body.String())
			}
			if rr.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", rr.Header().Get("Cache-Control"))
			}
		})
	}
}
