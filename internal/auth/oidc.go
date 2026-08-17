package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type discoveredOIDCBackend struct {
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
}

// NewOIDCService discovers and validates the configured provider before the
// application opens any listener.
func NewOIDCService(ctx context.Context, settings Settings, logger *slog.Logger) (*OIDCService, error) {
	provider, err := oidc.NewProvider(ctx, settings.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discover Authentik OIDC provider: %w", err)
	}
	var discovery struct {
		Issuer                string   `json:"issuer"`
		AuthorizationEndpoint string   `json:"authorization_endpoint"`
		TokenEndpoint         string   `json:"token_endpoint"`
		UserInfoEndpoint      string   `json:"userinfo_endpoint"`
		JWKSURI               string   `json:"jwks_uri"`
		ResponseTypes         []string `json:"response_types_supported"`
		CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	}
	if err := provider.Claims(&discovery); err != nil {
		return nil, fmt.Errorf("decode Authentik OIDC discovery: %w", err)
	}
	if discovery.Issuer != settings.Issuer || !secureEndpoint(discovery.AuthorizationEndpoint) ||
		!secureEndpoint(discovery.TokenEndpoint) || !secureEndpoint(discovery.UserInfoEndpoint) || !secureEndpoint(discovery.JWKSURI) {
		return nil, fmt.Errorf("Authentik OIDC discovery does not match configured issuer or endpoints")
	}
	if !slicesContain(discovery.ResponseTypes, "code") || !slicesContain(discovery.CodeChallengeMethods, "S256") {
		return nil, fmt.Errorf("Authentik OIDC provider must support authorization code and PKCE S256")
	}
	backend := &discoveredOIDCBackend{
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: settings.ClientID}),
		oauth: oauth2.Config{
			ClientID: settings.ClientID, ClientSecret: settings.ClientSecret,
			Endpoint: provider.Endpoint(), RedirectURL: settings.ExternalOrigin + "/api/v1/auth/callback",
			Scopes: append([]string(nil), settings.Scopes...),
		},
	}
	return newOIDCService(settings, backend, nil, logger), nil
}

func (b *discoveredOIDCBackend) AuthorizationURL(request authorizationRequest) string {
	return b.oauth.AuthCodeURL(request.State,
		oidc.Nonce(request.Nonce),
		oauth2.SetAuthURLParam("code_challenge", request.CodeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
}

func (b *discoveredOIDCBackend) Exchange(ctx context.Context, request exchangeRequest) (oidcIdentity, error) {
	token, err := b.oauth.Exchange(ctx, request.Code, oauth2.VerifierOption(request.Verifier))
	if err != nil {
		return oidcIdentity{}, fmt.Errorf("exchange authorization code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return oidcIdentity{}, fmt.Errorf("authorization response omitted ID token")
	}
	idToken, err := b.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return oidcIdentity{}, fmt.Errorf("verify ID token: %w", err)
	}
	var tokenClaims struct {
		Subject string `json:"sub"`
		Nonce   string `json:"nonce"`
	}
	if err := idToken.Claims(&tokenClaims); err != nil || tokenClaims.Subject == "" || tokenClaims.Nonce == "" {
		return oidcIdentity{}, fmt.Errorf("ID token omitted required claims")
	}
	userInfo, err := b.provider.UserInfo(ctx, oauth2.StaticTokenSource(token))
	if err != nil {
		return oidcIdentity{}, fmt.Errorf("retrieve OIDC UserInfo: %w", err)
	}
	var rawClaims json.RawMessage
	if err := userInfo.Claims(&rawClaims); err != nil {
		return oidcIdentity{}, fmt.Errorf("decode OIDC UserInfo: %w", err)
	}
	claims, err := parseUserInfoClaims(rawClaims)
	if err != nil {
		return oidcIdentity{}, err
	}
	return oidcIdentity{
		TokenSubject: tokenClaims.Subject, UserInfoSubject: claims.Subject, Nonce: tokenClaims.Nonce,
		Name: claims.Name, Username: claims.Username, Email: claims.Email, Groups: claims.Groups,
	}, nil
}

type userInfoClaims struct {
	Subject  string
	Name     string
	Username string
	Email    string
	Groups   []string
}

func parseUserInfoClaims(raw json.RawMessage) (userInfoClaims, error) {
	var claims struct {
		Subject  string          `json:"sub"`
		Name     string          `json:"name"`
		Username string          `json:"preferred_username"`
		Email    json.RawMessage `json:"email"`
		Groups   json.RawMessage `json:"groups"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil || claims.Subject == "" {
		return userInfoClaims{}, fmt.Errorf("OIDC UserInfo omitted required subject")
	}
	var groups []string
	if len(claims.Groups) == 0 || string(claims.Groups) == "null" || json.Unmarshal(claims.Groups, &groups) != nil {
		return userInfoClaims{}, authorizationDeniedError{}
	}
	var email string
	_ = json.Unmarshal(claims.Email, &email)
	return userInfoClaims{Subject: claims.Subject, Name: claims.Name, Username: claims.Username, Email: email, Groups: groups}, nil
}

type authorizationDeniedError struct{}

func (authorizationDeniedError) Error() string        { return "OIDC authorization denied" }
func (authorizationDeniedError) authorizationDenied() {}

func isAuthorizationDenied(err error) bool {
	var denied interface{ authorizationDenied() }
	return errors.As(err, &denied)
}

func secureEndpoint(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil &&
		parsed.RawQuery == "" && parsed.Fragment == "" && parsed.Opaque == ""
}

func slicesContain(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
