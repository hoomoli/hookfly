package auth

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestSettingsDefaultToFailClosedOIDCConfiguration(t *testing.T) {
	// Break caught: an empty production environment silently falling back to unauthenticated access.
	_, err := LoadSettings(mapLookup(nil))
	if err == nil || !strings.Contains(err.Error(), "HOOKFLY_EXTERNAL_URL") {
		t.Fatalf("LoadSettings() error = %v, want missing OIDC configuration", err)
	}
}

func TestSettingsAllowOnlyExplicitDevelopmentModeWithoutOIDCSecrets(t *testing.T) {
	settings, err := LoadSettings(mapLookup(map[string]string{"HOOKFLY_AUTH_MODE": "none"}))
	if err != nil {
		t.Fatal(err)
	}
	if settings.Mode != ModeNone {
		t.Fatalf("mode = %q, want %q", settings.Mode, ModeNone)
	}
}

func TestSettingsLoadSecureOIDCDefaults(t *testing.T) {
	settings, err := LoadSettings(mapLookup(validOIDCEnvironment()))
	if err != nil {
		t.Fatal(err)
	}
	if settings.Mode != ModeOIDC || settings.ExternalOrigin != "https://webhook.chuandashow.com" {
		t.Fatalf("mode/origin = %q/%q", settings.Mode, settings.ExternalOrigin)
	}
	if settings.Issuer != "https://authentik.chuandashow.com/application/o/hookfly/" {
		t.Fatalf("issuer = %q", settings.Issuer)
	}
	if strings.Join(settings.Scopes, ",") != "openid,profile" {
		t.Fatalf("scopes = %#v", settings.Scopes)
	}
	if settings.DisplayClaim != DisplayClaimPreferredUsername {
		t.Fatalf("display claim = %q, want %q", settings.DisplayClaim, DisplayClaimPreferredUsername)
	}
	if strings.Join(settings.AllowedGroups, ",") != "hookfly-users" {
		t.Fatalf("groups = %#v", settings.AllowedGroups)
	}
	if len(settings.SessionSecret) != 32 {
		t.Fatalf("session secret bytes = %d", len(settings.SessionSecret))
	}
}

func TestSettingsAcceptSupportedDisplayClaims(t *testing.T) {
	for _, claim := range []string{DisplayClaimPreferredUsername, DisplayClaimEmail} {
		t.Run(claim, func(t *testing.T) {
			environment := validOIDCEnvironment()
			environment["HOOKFLY_AUTH_DISPLAY_CLAIM"] = claim
			if claim == DisplayClaimEmail {
				environment["AUTHENTIK_OAUTH_SCOPES"] = "openid,profile,email"
			}
			settings, err := LoadSettings(mapLookup(environment))
			if err != nil {
				t.Fatal(err)
			}
			if settings.DisplayClaim != claim {
				t.Fatalf("display claim = %q, want %q", settings.DisplayClaim, claim)
			}
		})
	}
}

func TestSettingsRejectUnsupportedDisplayClaims(t *testing.T) {
	for _, claim := range []string{"", "name", "Email", " preferred_username "} {
		t.Run(claim, func(t *testing.T) {
			environment := validOIDCEnvironment()
			environment["HOOKFLY_AUTH_DISPLAY_CLAIM"] = claim
			if _, err := LoadSettings(mapLookup(environment)); err == nil || !strings.Contains(err.Error(), "HOOKFLY_AUTH_DISPLAY_CLAIM") {
				t.Fatalf("LoadSettings() error = %v, want display claim rejection", err)
			}
		})
	}
}

func TestSettingsRequireEmailScopeForEmailDisplayClaim(t *testing.T) {
	environment := validOIDCEnvironment()
	environment["HOOKFLY_AUTH_DISPLAY_CLAIM"] = DisplayClaimEmail
	environment["AUTHENTIK_OAUTH_SCOPES"] = "openid,profile"
	if _, err := LoadSettings(mapLookup(environment)); err == nil || !strings.Contains(err.Error(), "AUTHENTIK_OAUTH_SCOPES") {
		t.Fatalf("LoadSettings() error = %v, want missing email scope rejection", err)
	}
}

func TestSettingsCanonicalizeBrowserEquivalentExternalOrigins(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{value: "https://WEBHOOK.Example.Invalid:443", want: "https://webhook.example.invalid"},
		{value: "https://WEBHOOK.Example.Invalid:8443", want: "https://webhook.example.invalid:8443"},
		{value: "https://[2001:DB8::1]:443", want: "https://[2001:db8::1]"},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			environment := validOIDCEnvironment()
			environment["HOOKFLY_EXTERNAL_URL"] = test.value
			settings, err := LoadSettings(mapLookup(environment))
			if err != nil {
				t.Fatal(err)
			}
			if settings.ExternalOrigin != test.want {
				t.Fatalf("ExternalOrigin = %q, want %q", settings.ExternalOrigin, test.want)
			}
		})
	}
}

func TestSettingsRejectUnsafeExternalURLs(t *testing.T) {
	for _, value := range []string{
		"http://webhook.example.com",
		"https://user@webhook.example.com",
		"https://webhook.example.com/admin",
		"https://webhook.example.com/?query=x",
		"https://webhook.example.com/#fragment",
		"https://webhook.example.com:bad",
	} {
		t.Run(value, func(t *testing.T) {
			environment := validOIDCEnvironment()
			environment["HOOKFLY_EXTERNAL_URL"] = value
			if _, err := LoadSettings(mapLookup(environment)); err == nil || !strings.Contains(err.Error(), "HOOKFLY_EXTERNAL_URL") {
				t.Fatalf("LoadSettings(%q) error = %v", value, err)
			}
		})
	}
}

func TestSettingsRejectInvalidIssuerAndRequiredValues(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "http issuer", key: "AUTHENTIK_ISSUER", value: "http://authentik.example.com/application/o/hookfly/"},
		{name: "issuer query", key: "AUTHENTIK_ISSUER", value: "https://authentik.example.com/application/o/hookfly/?x=1"},
		{name: "empty client id", key: "AUTHENTIK_CLIENT_ID", value: ""},
		{name: "empty client secret", key: "AUTHENTIK_CLIENT_SECRET", value: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := validOIDCEnvironment()
			environment[test.key] = test.value
			if _, err := LoadSettings(mapLookup(environment)); err == nil || !strings.Contains(err.Error(), test.key) {
				t.Fatalf("LoadSettings() error = %v, want %s", err, test.key)
			}
		})
	}
}

func TestSettingsNormalizeScopesAndGroups(t *testing.T) {
	environment := validOIDCEnvironment()
	environment["AUTHENTIK_OAUTH_SCOPES"] = " profile;openid,profile "
	environment["HOOKFLY_AUTH_ALLOWED_GROUPS"] = "hookfly-users, operators;hookfly-users"
	settings, err := LoadSettings(mapLookup(environment))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(settings.Scopes, ",") != "profile,openid" {
		t.Fatalf("scopes = %#v", settings.Scopes)
	}
	if strings.Join(settings.AllowedGroups, ",") != "hookfly-users,operators" {
		t.Fatalf("groups = %#v", settings.AllowedGroups)
	}
}

func TestSettingsRejectScopesWithoutOpenIDAndExplicitEmptyGroups(t *testing.T) {
	for _, test := range []struct {
		key   string
		value string
	}{
		{key: "AUTHENTIK_OAUTH_SCOPES", value: "profile"},
		{key: "HOOKFLY_AUTH_ALLOWED_GROUPS", value: " , ; "},
	} {
		environment := validOIDCEnvironment()
		environment[test.key] = test.value
		if _, err := LoadSettings(mapLookup(environment)); err == nil || !strings.Contains(err.Error(), test.key) {
			t.Fatalf("LoadSettings(%s) error = %v", test.key, err)
		}
	}
}

func TestSettingsRequireExactly32DecodedSessionSecretBytes(t *testing.T) {
	for _, size := range []int{0, 16, 31, 33, 64} {
		environment := validOIDCEnvironment()
		environment["HOOKFLY_AUTH_SESSION_SECRET"] = base64.StdEncoding.EncodeToString(make([]byte, size))
		if _, err := LoadSettings(mapLookup(environment)); err == nil || !strings.Contains(err.Error(), "HOOKFLY_AUTH_SESSION_SECRET") {
			t.Fatalf("LoadSettings(%d bytes) error = %v", size, err)
		}
	}
	environment := validOIDCEnvironment()
	environment["HOOKFLY_AUTH_SESSION_SECRET"] = "not-base64"
	if _, err := LoadSettings(mapLookup(environment)); err == nil || !strings.Contains(err.Error(), "HOOKFLY_AUTH_SESSION_SECRET") {
		t.Fatalf("LoadSettings(invalid base64) error = %v", err)
	}
}

func TestSettingsRejectUnknownAuthenticationMode(t *testing.T) {
	_, err := LoadSettings(mapLookup(map[string]string{"HOOKFLY_AUTH_MODE": "disabled"}))
	if err == nil || !strings.Contains(err.Error(), "HOOKFLY_AUTH_MODE") {
		t.Fatalf("LoadSettings() error = %v", err)
	}
}

func validOIDCEnvironment() map[string]string {
	return map[string]string{
		"HOOKFLY_EXTERNAL_URL":        "https://webhook.chuandashow.com/",
		"AUTHENTIK_ISSUER":            "https://authentik.chuandashow.com/application/o/hookfly/",
		"AUTHENTIK_CLIENT_ID":         "hookfly-client",
		"AUTHENTIK_CLIENT_SECRET":     "test-client-secret",
		"HOOKFLY_AUTH_SESSION_SECRET": base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")),
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, found := values[key]
		return value, found
	}
}
