package auth

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
)

const (
	ModeOIDC                      = "oidc"
	ModeNone                      = "none"
	DisplayClaimPreferredUsername = "preferred_username"
	DisplayClaimEmail             = "email"
)

// Settings contains validated management-authentication configuration.
type Settings struct {
	Mode           string
	ExternalOrigin string
	Issuer         string
	ClientID       string
	ClientSecret   string
	Scopes         []string
	DisplayClaim   string
	AllowedGroups  []string
	SessionSecret  []byte
}

// LoadSettings parses authentication configuration without reading process
// environment directly, which keeps startup behavior deterministic in tests.
func LoadSettings(lookup func(string) (string, bool)) (Settings, error) {
	mode := ModeOIDC
	if value, found := lookup("HOOKFLY_AUTH_MODE"); found && strings.TrimSpace(value) != "" {
		mode = strings.ToLower(strings.TrimSpace(value))
	}
	if mode != ModeOIDC && mode != ModeNone {
		return Settings{}, fmt.Errorf("HOOKFLY_AUTH_MODE must be oidc or none")
	}
	displayClaim := DisplayClaimPreferredUsername
	if value, found := lookup("HOOKFLY_AUTH_DISPLAY_CLAIM"); found {
		displayClaim = value
	}
	if displayClaim != DisplayClaimPreferredUsername && displayClaim != DisplayClaimEmail {
		return Settings{}, fmt.Errorf("HOOKFLY_AUTH_DISPLAY_CLAIM must be preferred_username or email")
	}
	settings := Settings{Mode: mode, DisplayClaim: displayClaim}
	if mode == ModeNone {
		return settings, nil
	}

	externalURL, err := required(lookup, "HOOKFLY_EXTERNAL_URL")
	if err != nil {
		return Settings{}, err
	}
	settings.ExternalOrigin, err = secureOrigin(externalURL)
	if err != nil {
		return Settings{}, fmt.Errorf("HOOKFLY_EXTERNAL_URL must be an HTTPS origin")
	}

	settings.Issuer, err = required(lookup, "AUTHENTIK_ISSUER")
	if err != nil {
		return Settings{}, err
	}
	if !secureIssuer(settings.Issuer) {
		return Settings{}, fmt.Errorf("AUTHENTIK_ISSUER must be an absolute HTTPS URL without credentials, query, or fragment")
	}
	if settings.ClientID, err = required(lookup, "AUTHENTIK_CLIENT_ID"); err != nil {
		return Settings{}, err
	}
	if settings.ClientSecret, err = required(lookup, "AUTHENTIK_CLIENT_SECRET"); err != nil {
		return Settings{}, err
	}

	scopeValue, scopesConfigured := lookup("AUTHENTIK_OAUTH_SCOPES")
	if !scopesConfigured {
		scopeValue = "openid,profile"
	}
	settings.Scopes = splitUnique(scopeValue)
	if !slices.Contains(settings.Scopes, "openid") {
		return Settings{}, fmt.Errorf("AUTHENTIK_OAUTH_SCOPES must include openid")
	}
	if settings.DisplayClaim == DisplayClaimEmail && !slices.Contains(settings.Scopes, "email") {
		return Settings{}, fmt.Errorf("AUTHENTIK_OAUTH_SCOPES must include email when HOOKFLY_AUTH_DISPLAY_CLAIM is email")
	}

	groupValue, groupsConfigured := lookup("HOOKFLY_AUTH_ALLOWED_GROUPS")
	if !groupsConfigured {
		groupValue = "hookfly-users"
	}
	settings.AllowedGroups = splitUnique(groupValue)
	if len(settings.AllowedGroups) == 0 {
		return Settings{}, fmt.Errorf("HOOKFLY_AUTH_ALLOWED_GROUPS must not be empty")
	}

	encodedSecret, err := required(lookup, "HOOKFLY_AUTH_SESSION_SECRET")
	if err != nil {
		return Settings{}, err
	}
	settings.SessionSecret, err = base64.StdEncoding.DecodeString(encodedSecret)
	if err != nil || len(settings.SessionSecret) != 32 {
		return Settings{}, fmt.Errorf("HOOKFLY_AUTH_SESSION_SECRET must be Base64 encoding of exactly 32 bytes")
	}
	return settings, nil
}

func required(lookup func(string) (string, bool), name string) (string, error) {
	value, found := lookup(name)
	value = strings.TrimSpace(value)
	if !found || value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func secureOrigin(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", fmt.Errorf("invalid origin")
	}
	if _, err := url.ParseRequestURI(parsed.RequestURI()); err != nil {
		return "", fmt.Errorf("invalid origin")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" {
		return "", fmt.Errorf("invalid origin")
	}
	port := parsed.Port()
	if port == "443" {
		port = ""
	}
	host := hostname
	if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	}
	return "https://" + host, nil
}

func secureIssuer(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil &&
		parsed.RawQuery == "" && parsed.Fragment == "" && parsed.Opaque == ""
}

func splitUnique(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, exists := seen[part]; exists {
			continue
		}
		seen[part] = struct{}{}
		result = append(result, part)
	}
	return result
}
