package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
)

func TestOIDCDiscoveryRequiresSecureCompleteEndpoints(t *testing.T) {
	base := map[string]string{
		"authorization_endpoint": "https://idp.example.invalid/authorize",
		"token_endpoint":         "https://idp.example.invalid/token",
		"userinfo_endpoint":      "https://idp.example.invalid/userinfo",
		"jwks_uri":               "https://idp.example.invalid/jwks",
	}
	for _, test := range []struct {
		name  string
		field string
		value string
	}{
		{name: "missing authorization", field: "authorization_endpoint"},
		{name: "HTTP token", field: "token_endpoint", value: "http://idp.example.invalid/token"},
		{name: "missing UserInfo", field: "userinfo_endpoint"},
		{name: "relative UserInfo", field: "userinfo_endpoint", value: "/userinfo"},
		{name: "missing JWKS", field: "jwks_uri"},
		{name: "credentialed JWKS", field: "jwks_uri", value: "https://user@idp.example.invalid/jwks"},
	} {
		t.Run(test.name, func(t *testing.T) {
			endpoints := make(map[string]string, len(base))
			for key, value := range base {
				endpoints[key] = value
			}
			endpoints[test.field] = test.value
			issuer := "https://idp.example.invalid/application/o/hookfly/"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"issuer": issuer, "authorization_endpoint": endpoints["authorization_endpoint"],
					"token_endpoint": endpoints["token_endpoint"], "userinfo_endpoint": endpoints["userinfo_endpoint"],
					"jwks_uri": endpoints["jwks_uri"], "response_types_supported": []string{"code"},
					"code_challenge_methods_supported": []string{"S256"},
				})
			}))
			defer server.Close()

			settings := realAdapterSettings(t, issuer)
			ctx := oidc.ClientContext(context.Background(), rewriteHTTPSClient(t, server.URL))
			if _, err := NewOIDCService(ctx, settings, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
				t.Fatal("NewOIDCService() error = nil, want insecure or incomplete discovery rejection")
			}
		})
	}
}

func TestRealOIDCAdapterUsesAuthorizationCodePKCEAndVerifiedUserInfo(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const keyID = "hookfly-test-key"
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: privateKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader(jose.HeaderKey("kid"), keyID),
	)
	if err != nil {
		t.Fatal(err)
	}

	issuer := "https://idp.example.invalid/application/o/hookfly/"
	var fixtureMu sync.Mutex
	var nonce string
	var tokenForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/application/o/hookfly/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": issuer, "authorization_endpoint": "https://idp.example.invalid/authorize",
				"token_endpoint": "https://idp.example.invalid/token", "userinfo_endpoint": "https://idp.example.invalid/userinfo",
				"jwks_uri": "https://idp.example.invalid/jwks", "response_types_supported": []string{"code"},
				"code_challenge_methods_supported": []string{"S256"}, "id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			fixtureMu.Lock()
			tokenForm = r.Form
			currentNonce := nonce
			fixtureMu.Unlock()
			tokenIssuer := issuer
			tokenAudience := "hookfly-client"
			tokenExpiry := time.Now().Add(time.Hour)
			switch r.Form.Get("code") {
			case "wrong-issuer":
				tokenIssuer = "https://other-idp.example.invalid/"
			case "wrong-audience":
				tokenAudience = "other-client"
			case "expired":
				tokenExpiry = time.Now().Add(-time.Hour)
			}
			claims := fmt.Sprintf(`{"iss":%q,"sub":"subject-1","aud":%q,"exp":%d,"iat":%d,"nonce":%q}`,
				tokenIssuer, tokenAudience, tokenExpiry.Unix(), time.Now().Add(-time.Minute).Unix(), currentNonce)
			signed, signErr := signer.Sign([]byte(claims))
			if signErr != nil {
				t.Error(signErr)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			compact, compactErr := signed.CompactSerialize()
			if compactErr != nil {
				t.Error(compactErr)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-1", "token_type": "Bearer", "id_token": compact})
		case "/jwks":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &privateKey.PublicKey, KeyID: keyID, Algorithm: "RS256", Use: "sig"}}})
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer access-1" {
				t.Errorf("UserInfo Authorization = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sub": "subject-1", "name": "Display Name", "preferred_username": "dj",
				"email": "operator@example.com", "groups": []string{"hookfly-users"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := rewriteHTTPSClient(t, server.URL)
	settings := realAdapterSettings(t, issuer)
	settings.DisplayClaim = DisplayClaimEmail
	settings.Scopes = append(settings.Scopes, "email")
	service, err := NewOIDCService(oidc.ClientContext(context.Background(), client), settings, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	login := httptest.NewRecorder()
	service.Login(login, httptest.NewRequest(http.MethodGet, "/api/v1/auth/login?return_to=%2F%3Fview%3Dtargets", nil))
	transaction, err := service.decodeLoginCookie(responseCookie(t, login, loginCookieName).Value)
	if err != nil {
		t.Fatal(err)
	}
	fixtureMu.Lock()
	nonce = transaction.Nonce
	fixtureMu.Unlock()
	authorizationURL, err := url.Parse(login.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	query := authorizationURL.Query()
	digest := sha256.Sum256([]byte(transaction.Verifier))
	wantChallenge := base64.RawURLEncoding.EncodeToString(digest[:])
	if authorizationURL.Scheme != "https" || authorizationURL.Host != "idp.example.invalid" || authorizationURL.Path != "/authorize" ||
		query.Get("response_type") != "code" || query.Get("client_id") != "hookfly-client" ||
		query.Get("redirect_uri") != "https://webhook.example.invalid/api/v1/auth/callback" || query.Get("scope") != "openid profile email" ||
		query.Get("state") != transaction.State || query.Get("nonce") != transaction.Nonce || query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") != wantChallenge {
		t.Fatalf("authorization URL = %q", authorizationURL.String())
	}

	callbackRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/callback?code=code-1&state="+url.QueryEscape(transaction.State), nil)
	callbackRequest = callbackRequest.WithContext(oidc.ClientContext(callbackRequest.Context(), client))
	callbackRequest.AddCookie(responseCookie(t, login, loginCookieName))
	callback := httptest.NewRecorder()
	service.Callback(callback, callbackRequest)
	if callback.Code != http.StatusFound || callback.Header().Get("Location") != "/?view=targets" {
		t.Fatalf("callback = %d location=%q body=%q", callback.Code, callback.Header().Get("Location"), callback.Body.String())
	}
	fixtureMu.Lock()
	observedTokenForm := tokenForm
	fixtureMu.Unlock()
	if observedTokenForm.Get("grant_type") != "authorization_code" || observedTokenForm.Get("code") != "code-1" ||
		observedTokenForm.Get("redirect_uri") != "https://webhook.example.invalid/api/v1/auth/callback" || observedTokenForm.Get("code_verifier") != transaction.Verifier {
		t.Fatalf("token form = %v", observedTokenForm)
	}
	if findResponseCookie(callback, sessionCookieName) == nil {
		t.Fatal("verified callback did not create a session")
	}
	sessionRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	sessionRequest.AddCookie(responseCookie(t, callback, sessionCookieName))
	session := httptest.NewRecorder()
	service.Session(session, sessionRequest)
	if !strings.Contains(session.Body.String(), `"display_name":"operator@example.com"`) || strings.Contains(session.Body.String(), `"username"`) {
		t.Fatalf("email session projection = %s", session.Body.String())
	}
	for _, code := range []string{"wrong-issuer", "wrong-audience", "expired"} {
		if _, err := service.backend.Exchange(oidc.ClientContext(context.Background(), client), exchangeRequest{Code: code, Verifier: transaction.Verifier}); err == nil {
			t.Fatalf("Exchange(%q) error = nil, want ID token rejection", code)
		}
	}
}

func realAdapterSettings(t *testing.T, issuer string) Settings {
	t.Helper()
	environment := validOIDCEnvironment()
	environment["HOOKFLY_EXTERNAL_URL"] = "https://webhook.example.invalid"
	environment["AUTHENTIK_ISSUER"] = issuer
	settings, err := LoadSettings(mapLookup(environment))
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

func rewriteHTTPSClient(t *testing.T, target string) *http.Client {
	t.Helper()
	targetURL, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		clone := request.Clone(request.Context())
		clone.URL.Scheme = targetURL.Scheme
		clone.URL.Host = targetURL.Host
		clone.Host = targetURL.Host
		return http.DefaultTransport.RoundTrip(clone)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
