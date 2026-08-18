package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestDecodeDispatchesMultipleFilesOfOneKind(t *testing.T) {
	candidate := Candidate{
		Global: source("/cfg/hookfly.yaml", globalYAML("conf.d")),
		Resources: []SourceFile{
			source("/cfg/conf.d/a.yaml", gitLabSourceYAML("gitlab-a", 10)),
			source("/cfg/conf.d/b.yaml", gitLabSourceYAML("gitlab-b", 20)),
		},
	}

	bundle, err := Decode(candidate, nil)
	if err != nil || len(bundle.GitLabSources) != 2 {
		t.Fatalf("Decode() sources/error = %d/%v", len(bundle.GitLabSources), err)
	}
	if got, want := bundle.GitLabSources[0].Repositories[0].ExternalID, "10"; got != want {
		t.Fatalf("GitLab ExternalID = %q, want %q", got, want)
	}
}

func TestDecodeHTTPConnectionsAndTargets(t *testing.T) {
	candidate := Candidate{
		Global: source("/cfg/hookfly.yaml", globalYAML("conf.d")),
		Resources: []SourceFile{
			source("/cfg/conf.d/http-connections.yaml", `kind: HTTPConnections
connections:
  - id: application-admin
    base_url: https://admin.example.invalid
    auth:
      type: api_key
      value: ${ADMIN_TOKEN}
      header: X-API-Token
`),
			source("/cfg/conf.d/http-targets.yaml", `kind: HTTPTargets
targets:
  - id: application-deploy
    type: http
    connection: application-admin
    method: POST
    path: /api/deploy
    headers:
      X-Deploy-Source: hookfly
    body:
      type: json
      value:
        ref: "{{ event.ref }}"
        attempt_id: "{{ attempt.id }}"
    success_statuses: [202]
`),
		},
	}

	bundle, err := Decode(candidate, func(name string) (string, bool) { return "http-secret", name == "ADMIN_TOKEN" })
	if err != nil {
		t.Fatal(err)
	}
	if got := bundle.HTTPConnections; len(got) != 1 || got[0].Auth.Value != "http-secret" || got[0].Auth.Header != "X-API-Token" {
		t.Fatalf("HTTPConnections = %#v", got)
	}
	if got := bundle.Targets; len(got) != 1 || got[0].Method != "POST" || got[0].Path != "/api/deploy" || got[0].Headers["X-Deploy-Source"] != "hookfly" {
		t.Fatalf("Targets = %#v", got)
	}
}

func TestDecodeForwardTargetDefaultsToTargetHost(t *testing.T) {
	// Break caught: requiring an HTTP connection/auth contract or losing the explicit forwarding destination.
	candidate := Candidate{
		Global: source("/cfg/hookfly.yaml", globalYAML("conf.d")),
		Resources: []SourceFile{source("/cfg/conf.d/forward-targets.yaml", `kind: ForwardTargets
targets:
  - id: downstream-gitlab
    type: forward
    url: https://receiver.example.invalid/webhooks/gitlab
`)},
	}
	bundle, err := Decode(candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAndCanonicalize(bundle); err != nil {
		t.Fatal(err)
	}
	if got := bundle.Targets; len(got) != 1 || got[0].Type != "forward" || got[0].URL != "https://receiver.example.invalid/webhooks/gitlab" || got[0].Host != "target" {
		t.Fatalf("Targets = %#v", got)
	}
}

func TestDecodeHTTPFormTargetWithQueryAndBearerAuthentication(t *testing.T) {
	candidate := Candidate{
		Global: source("/cfg/hookfly.yaml", globalYAML("conf.d")),
		Resources: []SourceFile{
			source("/cfg/conf.d/http-connections.yaml", `kind: HTTPConnections
connections:
  - id: application-admin
    base_url: https://admin.example.invalid
    auth:
      type: bearer
      value: ${ADMIN_TOKEN}
`),
			source("/cfg/conf.d/http-targets.yaml", `kind: HTTPTargets
targets:
  - id: application-deploy
    type: http
    connection: application-admin
    method: POST
    path: /api/deploy
    query:
      page: "2"
      tag: [blue, green]
    body:
      type: form
      value:
        ref: "{{event.ref}}"
        mode: release
`),
		},
	}
	bundle, err := Decode(candidate, func(name string) (string, bool) { return "bearer-token", name == "ADMIN_TOKEN" })
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAndCanonicalize(bundle); err != nil {
		t.Fatal(err)
	}
	if got := bundle.HTTPConnections[0].Auth; got.Type != "bearer" || got.Value != "bearer-token" || got.Header != "" {
		t.Fatalf("authentication = %#v", got)
	}
	if got, want := bundle.Targets[0].Query["tag"], (HTTPStringList{"blue", "green"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("query tag = %#v, want %#v", got, want)
	}
	body, ok := bundle.Targets[0].Body.Value.(map[string]HTTPStringList)
	if !ok || !reflect.DeepEqual(body["ref"], HTTPStringList{"{{event.ref}}"}) {
		t.Fatalf("form body = %#v", bundle.Targets[0].Body)
	}
}

func TestDecodeAllowsEmptyResourceList(t *testing.T) {
	bundle, err := Decode(Candidate{Global: source("/cfg/hookfly.yaml", globalYAML("conf.d"))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.GitLabSources) != 0 || len(bundle.GitHubSources) != 0 || len(bundle.DokployConnections) != 0 || len(bundle.Targets) != 0 || len(bundle.Routes) != 0 {
		t.Fatalf("Decode() bundle = %#v, want no resources", bundle)
	}
}

func TestDecodeAcceptsCurrentConfigurationWithoutAPIVersion(t *testing.T) {
	candidate := Candidate{
		Global: source("/cfg/hookfly.yaml", "kind: Hookfly\nconfig_dir: conf.d\nhistory: {}\npolling: {}\n"),
		Resources: []SourceFile{
			source("/cfg/conf.d/routes.yaml", "kind: Routes\nroutes: []\n"),
		},
	}

	if _, err := Decode(candidate, nil); err != nil {
		t.Fatalf("Decode() error = %v, want current configuration accepted without api_version", err)
	}
}

func TestDecodeRejectsObsoleteAPIVersionField(t *testing.T) {
	t.Run("global", func(t *testing.T) {
		content := "api_version: hookfly.io/v2\n" + globalYAML("conf.d")
		_, err := Decode(Candidate{Global: source("/cfg/hookfly.yaml", content)}, nil)
		if err == nil || !strings.Contains(err.Error(), "/cfg/hookfly.yaml") {
			t.Fatalf("Decode() error = %v, want obsolete global api_version rejected", err)
		}
	})

	t.Run("resource", func(t *testing.T) {
		candidate := Candidate{
			Global: source("/cfg/hookfly.yaml", "kind: Hookfly\nconfig_dir: conf.d\nhistory: {}\npolling: {}\n"),
			Resources: []SourceFile{
				source("/cfg/conf.d/routes.yaml", "api_version: hookfly.io/v2\nkind: Routes\nroutes: []\n"),
			},
		}
		_, err := Decode(candidate, nil)
		if err == nil || !strings.Contains(err.Error(), "/cfg/conf.d/routes.yaml") {
			t.Fatalf("Decode() error = %v, want obsolete resource api_version rejected", err)
		}
	})
}

func TestDecodeResolvesOnlyExactEnvironmentScalars(t *testing.T) {
	candidate := Candidate{
		Global: source("/cfg/hookfly.yaml", globalYAML("conf.d")),
		Resources: []SourceFile{source("/cfg/conf.d/dokploy.yaml", `kind: DokployTargets
connections:
  - id: prefix-${DOKPLOY_KEY}
    base_url: https://dokploy.example.invalid
    api_key: ${DOKPLOY_KEY}
targets: []
`)},
	}

	bundle, err := Decode(candidate, func(name string) (string, bool) {
		return "secret:value # with comment characters", name == "DOKPLOY_KEY"
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := bundle.DokployConnections[0].APIKey, "secret:value # with comment characters"; got != want {
		t.Fatalf("APIKey = %q, want %q", got, want)
	}
	if got, want := bundle.DokployConnections[0].ID, "prefix-${DOKPLOY_KEY}"; got != want {
		t.Fatalf("ID = %q, want %q", got, want)
	}
}

func TestDecodeRejectsMissingOrEmptyEnvironmentVariable(t *testing.T) {
	for _, lookup := range []EnvLookup{
		nil,
		func(string) (string, bool) { return "", true },
	} {
		candidate := Candidate{
			Global: source("/cfg/hookfly.yaml", globalYAML("conf.d")),
			Resources: []SourceFile{source("/cfg/conf.d/dokploy.yaml", `kind: DokployTargets
connections:
  - id: dokploy
    base_url: https://dokploy.example.invalid
    api_key: ${MISSING_KEY}
targets: []
`)},
		}
		_, err := Decode(candidate, lookup)
		if err == nil || !strings.Contains(err.Error(), "MISSING_KEY") {
			t.Fatalf("Decode() error = %v, want missing environment variable", err)
		}
	}
}

func TestDecodeRejectsMultipleYAMLDocuments(t *testing.T) {
	candidate := Candidate{Global: source("/cfg/hookfly.yaml", globalYAML("conf.d")+"---\nkind: Hookfly\n")}
	_, err := Decode(candidate, nil)
	if err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("Decode() error = %v, want multiple-document error", err)
	}
}

func TestDecodeRejectsUnknownFieldAndDuplicateMappingKey(t *testing.T) {
	for _, content := range []string{
		globalYAML("conf.d") + "unexpected: true\n",
		"kind: Hookfly\nkind: Hookfly\nconfig_dir: conf.d\nhistory: {}\npolling: {}\n",
	} {
		_, err := Decode(Candidate{Global: source("/cfg/hookfly.yaml", content)}, nil)
		if err == nil {
			t.Fatal("Decode() error = nil, want strict decoder error")
		}
	}
}

func TestDecodeRejectsWrongGlobalKind(t *testing.T) {
	content := strings.Replace(globalYAML("conf.d"), "kind: Hookfly", "kind: GitLabSources", 1)
	_, err := Decode(Candidate{Global: source("/cfg/hookfly.yaml", content)}, nil)
	if err == nil || !strings.Contains(err.Error(), "/cfg/hookfly.yaml") {
		t.Fatalf("Decode() error = %v, want safe global metadata error", err)
	}
}

func TestDecodeUsesCurrentGlobalHistoryFields(t *testing.T) {
	for _, test := range []struct {
		name    string
		history string
		wantErr bool
	}{
		{name: "accepts default repository limit", history: "  global_limit: 10\n  default_repository_limit: 5\n"},
		{name: "rejects legacy default project limit", history: "  default_project_limit: 5\n", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := Candidate{Global: source("/cfg/hookfly.yaml", "kind: Hookfly\nhistory:\n"+test.history+"polling: {}\n")}
			bundle, err := Decode(candidate, nil)
			if test.wantErr {
				if err == nil {
					t.Fatal("Decode() error = nil, want legacy v1 field rejection")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, want := bundle.Global.History.DefaultRepositoryLimit, 5; got != want {
				t.Fatalf("Global.History.DefaultRepositoryLimit = %d, want %d", got, want)
			}
		})
	}
}

func TestDecodeRejectsUnknownResourceKind(t *testing.T) {
	candidate := Candidate{
		Global:    source("/cfg/hookfly.yaml", globalYAML("conf.d")),
		Resources: []SourceFile{source("/cfg/conf.d/resource.yaml", "kind: Unregistered\n")},
	}
	_, err := Decode(candidate, nil)
	if err == nil || !strings.Contains(err.Error(), "/cfg/conf.d/resource.yaml") {
		t.Fatalf("Decode() error = %v, want safe resource metadata error", err)
	}
}

func TestDecodeNeverIncludesResolvedSecretInErrors(t *testing.T) {
	candidate := Candidate{
		Global: source("/cfg/hookfly.yaml", globalYAML("conf.d")),
		Resources: []SourceFile{source("/cfg/conf.d/dokploy.yaml", `kind: DokployTargets
connections:
  - id: dokploy
    base_url: https://dokploy.example.invalid
    api_key: ${DOKPLOY_KEY}
    unexpected: true
targets: []
`)},
	}
	_, err := Decode(candidate, func(string) (string, bool) { return "never-print-this-secret", true })
	if err == nil {
		t.Fatal("Decode() error = nil, want strict decoder error")
	}
	if strings.Contains(err.Error(), "never-print-this-secret") {
		t.Fatalf("Decode() leaked resolved secret: %v", err)
	}
}

func TestDecodeNeverIncludesResolvedSecretInUnsupportedMetadata(t *testing.T) {
	candidate := Candidate{
		Global:    source("/cfg/hookfly.yaml", globalYAML("conf.d")),
		Resources: []SourceFile{source("/cfg/conf.d/unknown.yaml", "kind: ${KIND}\n")},
	}
	_, err := Decode(candidate, func(string) (string, bool) { return "never-print-this-secret", true })
	if err == nil {
		t.Fatal("Decode() error = nil, want unsupported-kind error")
	}
	if strings.Contains(err.Error(), "never-print-this-secret") {
		t.Fatalf("Decode() leaked resolved secret: %v", err)
	}
}

func TestDecodeAcceptsRoutePriority(t *testing.T) {
	candidate := Candidate{
		Global: source("/cfg/hookfly.yaml", globalYAML("conf.d")),
		Resources: []SourceFile{source("/cfg/conf.d/routes.yaml", `kind: Routes
routes:
  - id: deploy-main
    priority: 10
    match: {}
    action:
      type: record_only
`)},
	}
	bundle, err := Decode(candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := bundle.Routes[0].Priority; got == nil || *got != 10 {
		t.Fatalf("Route priority = %v, want 10", got)
	}
}

func TestDecodeRequiresExplicitRoutePriority(t *testing.T) {
	candidate := Candidate{
		Global: source("/cfg/hookfly.yaml", globalYAML("conf.d")),
		Resources: []SourceFile{source("/cfg/conf.d/routes.yaml", `kind: Routes
routes:
  - id: deploy-main
    match: {}
    action:
      type: record_only
`)},
	}
	if _, err := Decode(candidate, nil); err == nil {
		t.Fatal("Decode() error = nil, want missing priority error")
	}
}

func TestDecodeAcceptsExplicitZeroRoutePriority(t *testing.T) {
	candidate := Candidate{
		Global: source("/cfg/hookfly.yaml", globalYAML("conf.d")),
		Resources: []SourceFile{source("/cfg/conf.d/routes.yaml", `kind: Routes
routes:
  - id: deploy-main
    priority: 0
    match: {}
    action:
      type: record_only
`)},
	}
	bundle, err := Decode(candidate, nil)
	if err != nil {
		t.Fatalf("Decode() error = %v, want explicit zero priority accepted", err)
	}
	if got := bundle.Routes[0].Priority; got == nil || *got != 0 {
		t.Fatalf("Route priority = %v, want explicit zero", got)
	}
}

func TestDecodeDoesNotLeakResolvedSecretFromInvalidDuration(t *testing.T) {
	candidate := Candidate{Global: source("/cfg/hookfly.yaml", `kind: Hookfly
config_dir: conf.d
history: {}
polling:
  interval: ${POLLING_INTERVAL}
`)}
	_, err := Decode(candidate, func(string) (string, bool) { return "never-print-this-secret", true })
	if err == nil {
		t.Fatal("Decode() error = nil, want invalid duration error")
	}
	if strings.Contains(err.Error(), "never-print-this-secret") {
		t.Fatalf("Decode() leaked resolved secret: %v", err)
	}
}

func TestDecodeRejectsMissingOrEmptyProviderCredentials(t *testing.T) {
	for _, resource := range []string{
		`kind: GitLabSources
sources:
  - id: gitlab
    token: ""
    repositories:
      - id: repository
        project_id: 1
`,
		`kind: GitLabSources
sources:
  - id: gitlab
    repositories:
      - id: repository
        project_id: 1
`,
		`kind: GitHubSources
sources:
  - id: github
    secret: ""
    repositories:
      - id: repository
        repository_id: 1
`,
		`kind: GitHubSources
sources:
  - id: github
    repositories:
      - id: repository
        repository_id: 1
`,
		`kind: DokployTargets
connections:
  - id: dokploy
    base_url: https://dokploy.example.invalid
    api_key: ""
targets: []
`,
		`kind: DokployTargets
connections:
  - id: dokploy
    base_url: https://dokploy.example.invalid
targets: []
`,
	} {
		candidate := Candidate{
			Global:    source("/cfg/hookfly.yaml", globalYAML("conf.d")),
			Resources: []SourceFile{source("/cfg/conf.d/resource.yaml", resource)},
		}
		if _, err := Decode(candidate, nil); err == nil {
			t.Fatal("Decode() error = nil, want credential validation error")
		}
	}
}

func source(path, content string) SourceFile { return SourceFile{Path: path, Content: []byte(content)} }

func globalYAML(configDir string) string {
	return "kind: Hookfly\nconfig_dir: " + configDir + "\nhistory: {}\npolling: {}\n"
}

func gitLabSourceYAML(id string, projectID int) string {
	return "kind: GitLabSources\nsources:\n  - id: " + id + "\n    token: token\n    repositories:\n      - id: repository\n        project_id: " + string(rune('0'+projectID/10)) + string(rune('0'+projectID%10)) + "\n"
}
