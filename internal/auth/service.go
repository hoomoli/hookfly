package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/gorilla/securecookie"
)

const (
	sessionCookieName = "hookfly_session"
	loginCookieName   = "hookfly_login"
	sessionLifetime   = 8 * time.Hour
	loginLifetime     = 10 * time.Minute
)

// Service authenticates management requests and owns the browser login routes.
type Service interface {
	Provider
	Login(http.ResponseWriter, *http.Request)
	Callback(http.ResponseWriter, *http.Request)
	Session(http.ResponseWriter, *http.Request)
	Logout(http.ResponseWriter, *http.Request)
	ExpectedOrigin() string
}

type authorizationRequest struct {
	State         string
	Nonce         string
	CodeChallenge string
}

type exchangeRequest struct {
	Code     string
	Verifier string
}

type oidcIdentity struct {
	TokenSubject    string
	UserInfoSubject string
	Nonce           string
	Name            string
	Username        string
	Groups          []string
}

type oidcBackend interface {
	AuthorizationURL(authorizationRequest) string
	Exchange(context.Context, exchangeRequest) (oidcIdentity, error)
}

type sessionData struct {
	Version     int    `json:"v"`
	ActorID     string `json:"a"`
	DisplayName string `json:"d"`
	Username    string `json:"u,omitempty"`
	Provider    string `json:"p"`
	IssuedAt    int64  `json:"iat"`
	ExpiresAt   int64  `json:"exp"`
}

type loginTransaction struct {
	Version   int    `json:"v"`
	State     string `json:"s"`
	Nonce     string `json:"n"`
	Verifier  string `json:"p"`
	ReturnTo  string `json:"r"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

// OIDCService implements Authentik OIDC login and encrypted local sessions.
type OIDCService struct {
	settings     Settings
	backend      oidcBackend
	sessionCodec *securecookie.SecureCookie
	loginCodec   *securecookie.SecureCookie
	now          func() time.Time
	logger       *slog.Logger
}

func newOIDCService(settings Settings, backend oidcBackend, now func() time.Time, logger *slog.Logger) *OIDCService {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	sessionCodec := securecookie.New(deriveKey(settings.SessionSecret, "session-auth"), deriveKey(settings.SessionSecret, "session-encrypt"))
	sessionCodec.MaxAge(int(sessionLifetime.Seconds()))
	loginCodec := securecookie.New(deriveKey(settings.SessionSecret, "login-auth"), deriveKey(settings.SessionSecret, "login-encrypt"))
	loginCodec.MaxAge(int(loginLifetime.Seconds()))
	return &OIDCService{settings: settings, backend: backend, sessionCodec: sessionCodec, loginCodec: loginCodec, now: now, logger: logger}
}

func (s *OIDCService) ExpectedOrigin() string { return s.settings.ExternalOrigin }

func (s *OIDCService) Authenticate(request *http.Request) (Principal, error) {
	session, err := s.sessionFromRequest(request)
	if err != nil {
		return Principal{}, err
	}
	return principalFor(session.ActorID, session.DisplayName), nil
}

func (s *OIDCService) AuthenticationFailed(writer http.ResponseWriter) {
	s.clearCookie(writer, sessionCookieName)
}

func (s *OIDCService) Login(writer http.ResponseWriter, request *http.Request) {
	state, err := randomValue(32)
	if err != nil {
		s.writeAuthenticationFailure(writer, http.StatusInternalServerError)
		return
	}
	nonce, err := randomValue(32)
	if err != nil {
		s.writeAuthenticationFailure(writer, http.StatusInternalServerError)
		return
	}
	verifier, err := randomValue(32)
	if err != nil {
		s.writeAuthenticationFailure(writer, http.StatusInternalServerError)
		return
	}
	now := s.now()
	transaction := loginTransaction{
		Version: 1, State: state, Nonce: nonce, Verifier: verifier,
		ReturnTo: safeReturnTo(request.URL.Query().Get("return_to")),
		IssuedAt: now.Unix(), ExpiresAt: now.Add(loginLifetime).Unix(),
	}
	encoded, err := s.encodeLoginCookie(transaction)
	if err != nil {
		s.writeAuthenticationFailure(writer, http.StatusInternalServerError)
		return
	}
	s.setCookie(writer, loginCookieName, encoded, loginLifetime)
	digest := sha256.Sum256([]byte(verifier))
	location := s.backend.AuthorizationURL(authorizationRequest{
		State: state, Nonce: nonce, CodeChallenge: base64.RawURLEncoding.EncodeToString(digest[:]),
	})
	writer.Header().Set("Cache-Control", "no-store")
	http.Redirect(writer, request, location, http.StatusFound)
}

func (s *OIDCService) Callback(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	s.clearCookie(writer, loginCookieName)
	cookie, err := request.Cookie(loginCookieName)
	if err != nil {
		s.writeAuthenticationFailure(writer, http.StatusUnauthorized)
		return
	}
	transaction, err := s.decodeLoginCookie(cookie.Value)
	if err != nil || transaction.Version != 1 || transaction.ExpiresAt <= s.now().Unix() ||
		subtle.ConstantTimeCompare([]byte(transaction.State), []byte(request.URL.Query().Get("state"))) != 1 {
		s.writeAuthenticationFailure(writer, http.StatusUnauthorized)
		return
	}
	code := request.URL.Query().Get("code")
	if code == "" || request.URL.Query().Get("error") != "" {
		s.writeAuthenticationFailure(writer, http.StatusUnauthorized)
		return
	}
	identity, err := s.backend.Exchange(request.Context(), exchangeRequest{Code: code, Verifier: transaction.Verifier})
	if isAuthorizationDenied(err) {
		writeAuthorizationDeniedPage(writer)
		return
	}
	if err != nil || identity.TokenSubject == "" || identity.UserInfoSubject == "" ||
		identity.TokenSubject != identity.UserInfoSubject || identity.Nonce != transaction.Nonce {
		s.writeAuthenticationFailure(writer, http.StatusUnauthorized)
		return
	}
	if !containsAllowedGroup(identity.Groups, s.settings.AllowedGroups) {
		writeAuthorizationDeniedPage(writer)
		return
	}
	actorDigest := sha256.Sum256([]byte(s.settings.Issuer + "\x00" + identity.TokenSubject))
	if err := s.issueSession(writer, sessionData{
		ActorID: "oidc:" + hex.EncodeToString(actorDigest[:]), DisplayName: displayName(identity),
		Username: identity.Username, Provider: "authentik",
	}); err != nil {
		s.writeAuthenticationFailure(writer, http.StatusInternalServerError)
		return
	}
	http.Redirect(writer, request, safeReturnTo(transaction.ReturnTo), http.StatusFound)
}

func (s *OIDCService) Session(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	session, err := s.sessionFromRequest(request)
	if err != nil {
		s.clearCookie(writer, sessionCookieName)
		writeAuthError(writer, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(struct {
		User struct {
			DisplayName string `json:"display_name"`
			Username    string `json:"username,omitempty"`
			Provider    string `json:"provider"`
		} `json:"user"`
		ExpiresAt string `json:"expires_at"`
	}{User: struct {
		DisplayName string `json:"display_name"`
		Username    string `json:"username,omitempty"`
		Provider    string `json:"provider"`
	}{DisplayName: session.DisplayName, Username: session.Username, Provider: session.Provider}, ExpiresAt: time.Unix(session.ExpiresAt, 0).UTC().Format(time.RFC3339)})
}

func (s *OIDCService) Logout(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	s.clearCookie(writer, sessionCookieName)
	writer.WriteHeader(http.StatusNoContent)
}

func (s *OIDCService) issueSession(writer http.ResponseWriter, session sessionData) error {
	now := s.now()
	session.Version = 1
	session.IssuedAt = now.Unix()
	session.ExpiresAt = now.Add(sessionLifetime).Unix()
	encoded, err := s.sessionCodec.Encode(sessionCookieName, session)
	if err != nil {
		return err
	}
	s.setCookie(writer, sessionCookieName, encoded, sessionLifetime)
	return nil
}

func (s *OIDCService) sessionFromRequest(request *http.Request) (sessionData, error) {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		return sessionData{}, err
	}
	var session sessionData
	if err := s.sessionCodec.Decode(sessionCookieName, cookie.Value, &session); err != nil {
		return sessionData{}, err
	}
	if session.Version != 1 || session.ActorID == "" || session.DisplayName == "" || session.Provider == "" || session.ExpiresAt <= s.now().Unix() {
		return sessionData{}, errors.New("session is invalid or expired")
	}
	return session, nil
}

func (s *OIDCService) encodeLoginCookie(transaction loginTransaction) (string, error) {
	return s.loginCodec.Encode(loginCookieName, transaction)
}

func (s *OIDCService) decodeLoginCookie(value string) (loginTransaction, error) {
	var transaction loginTransaction
	err := s.loginCodec.Decode(loginCookieName, value, &transaction)
	return transaction, err
}

func (s *OIDCService) setCookie(writer http.ResponseWriter, name, value string, lifetime time.Duration) {
	http.SetCookie(writer, &http.Cookie{
		Name: name, Value: value, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteLaxMode, MaxAge: int(lifetime.Seconds()), Expires: s.now().Add(lifetime),
	})
}

func (s *OIDCService) clearCookie(writer http.ResponseWriter, name string) {
	http.SetCookie(writer, &http.Cookie{
		Name: name, Value: "", Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0),
	})
}

func (s *OIDCService) writeAuthenticationFailure(writer http.ResponseWriter, status int) {
	writeAuthError(writer, status, "unauthorized", "authentication failed")
}

func writeAuthorizationDeniedPage(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(http.StatusForbidden)
	_, _ = writer.Write([]byte("<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>Access denied</title></head><body><h1>Access denied</h1><p>Your account is not allowed to use Hookfly.</p></body></html>\n"))
}

type developmentService struct{ NoAuthProvider }

// NewDevelopmentService creates the explicit local-only authentication mode.
func NewDevelopmentService() Service { return developmentService{} }

func (developmentService) ExpectedOrigin() string { return "" }
func (developmentService) Login(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNotFound)
}
func (developmentService) Callback(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNotFound)
}
func (developmentService) Logout(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNotFound)
}
func (developmentService) Session(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte(`{"user":{"display_name":"Local development","provider":"local"}}` + "\n"))
}

func deriveKey(secret []byte, purpose string) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("hookfly-auth:" + purpose))
	return mac.Sum(nil)
}

func randomValue(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func safeReturnTo(value string) string {
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || unsafeRedirectText(value) {
		return "/"
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" {
		return "/"
	}
	decodedPath, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil || strings.HasPrefix(decodedPath, "//") || unsafeRedirectText(decodedPath) {
		return "/"
	}
	return value
}

func unsafeRedirectText(value string) bool {
	for _, character := range value {
		if character == '\\' || character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func containsAllowedGroup(groups, allowed []string) bool {
	for _, group := range groups {
		if slices.Contains(allowed, group) {
			return true
		}
	}
	return false
}

func displayName(identity oidcIdentity) string {
	if value := strings.TrimSpace(identity.Username); value != "" {
		return value
	}
	if value := strings.TrimSpace(identity.Name); value != "" {
		return value
	}
	return "Authenticated user"
}

func allPermissions() []string {
	return []string{PermissionEventsRead, PermissionEventsDelete, PermissionConnectionsRead, PermissionDeploymentsRetry, PermissionConfigReload}
}

func principalFor(id, displayName string) Principal {
	permissions := make(map[string]bool, len(allPermissions()))
	for _, permission := range allPermissions() {
		permissions[permission] = true
	}
	return Principal{ID: id, DisplayName: displayName, Permissions: permissions}
}
