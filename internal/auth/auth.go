// Package auth provides the replaceable management authentication boundary.
package auth

import (
	"context"
	"encoding/json"
	"net/http"
)

const (
	PermissionEventsRead       = "events:read"
	PermissionEventsDelete     = "events:delete"
	PermissionConnectionsRead  = "connections:read"
	PermissionDeploymentsRetry = "deployments:retry"
	PermissionConfigReload     = "config:reload"
)

// Principal is the authenticated management actor and its granted permissions.
type Principal struct {
	ID          string
	DisplayName string
	Permissions map[string]bool
}

// Provider authenticates one management request.
type Provider interface {
	Authenticate(*http.Request) (Principal, error)
}

// NoAuthProvider is the initial WireGuard-only identity provider.
type NoAuthProvider struct{}

func (NoAuthProvider) Authenticate(*http.Request) (Principal, error) {
	return Principal{
		ID:          "wireguard-anonymous",
		DisplayName: "WireGuard anonymous",
		Permissions: map[string]bool{
			PermissionEventsRead:       true,
			PermissionEventsDelete:     true,
			PermissionConnectionsRead:  true,
			PermissionDeploymentsRetry: true,
			PermissionConfigReload:     true,
		},
	}, nil
}

type principalContextKey struct{}

// Authenticate asks the provider for a Principal and places it in request context.
func Authenticate(provider Provider, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if provider == nil {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		principal, err := provider.Authenticate(r)
		if err != nil || principal.ID == "" {
			if failureHandler, ok := provider.(interface{ AuthenticationFailed(http.ResponseWriter) }); ok {
				failureHandler.AuthenticationFailed(w)
			}
			writeAuthError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		ctx := context.WithValue(r.Context(), principalContextKey{}, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Require allows only a context Principal with permission.
func Require(permission string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
			return
		}
		if !principal.Permissions[permission] {
			writeAuthError(w, http.StatusForbidden, "forbidden", "permission denied")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// PrincipalFromContext returns the authenticated management actor.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok && principal.ID != ""
}

func writeAuthError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message}})
}
