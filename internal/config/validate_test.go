package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidateAndCanonicalizeRejectsDuplicateSourceIDsAcrossProviders(t *testing.T) {
	bundle := &Bundle{
		GitLabSources: []GitLabSource{{
			ID:       "shared",
			Token:    "token",
			Location: SourceLocation{Path: "/cfg/gitlab.yaml", Field: "sources"},
		}},
		GitHubSources: []GitHubSource{{
			ID:       "shared",
			Secret:   "secret",
			Location: SourceLocation{Path: "/cfg/github.yaml", Field: "sources"},
		}},
	}

	err := ValidateAndCanonicalize(bundle)
	if err == nil {
		t.Fatal("ValidateAndCanonicalize() error = nil, want duplicate source rejection")
	}
	for _, location := range []SourceLocation{bundle.GitLabSources[0].Location, bundle.GitHubSources[0].Location} {
		if !strings.Contains(err.Error(), location.String()) {
			t.Fatalf("ValidateAndCanonicalize() error = %v, want location %q", err, location.String())
		}
	}
}

func TestValidateRejectsDuplicateGitLabSourceTokens(t *testing.T) {
	bundle := &Bundle{GitLabSources: []GitLabSource{
		{
			ID: "primary", Token: "shared-secret", Location: SourceLocation{Path: "/cfg/primary.yaml", Field: "sources[0]"},
			Repositories: []Repository{{ID: "application", ExternalID: "7"}},
		},
		{
			ID: "secondary", Token: "shared-secret", Location: SourceLocation{Path: "/cfg/secondary.yaml", Field: "sources[0]"},
			Repositories: []Repository{{ID: "operations", ExternalID: "8"}},
		},
	}}

	err := ValidateAndCanonicalize(bundle)
	if err == nil || !strings.Contains(err.Error(), "duplicate GitLab source token") {
		t.Fatalf("ValidateAndCanonicalize() error = %v, want duplicate GitLab source token", err)
	}
	if strings.Contains(err.Error(), "shared-secret") {
		t.Fatalf("ValidateAndCanonicalize() exposed token in error: %v", err)
	}
	for _, source := range bundle.GitLabSources {
		if !strings.Contains(err.Error(), source.Location.String()) {
			t.Fatalf("ValidateAndCanonicalize() error = %v, want location %q", err, source.Location.String())
		}
	}
}

func TestValidateAndCanonicalizeRejectsDuplicateRepositoryExternalIDsWithinSource(t *testing.T) {
	for _, test := range []struct {
		name      string
		path      string
		resource  string
		locations []SourceLocation
	}{
		{
			name: "GitLab",
			path: "/cfg/gitlab.yaml",
			resource: `kind: GitLabSources
sources:
  - id: gitlab-primary
    token: test-token
    repositories:
      - id: application
        project_id: 7
      - id: operations
        project_id: 7
`,
			locations: []SourceLocation{
				{Path: "/cfg/gitlab.yaml", Field: "repositories[0]"},
				{Path: "/cfg/gitlab.yaml", Field: "repositories[1]"},
			},
		},
		{
			name: "GitHub",
			path: "/cfg/github.yaml",
			resource: `kind: GitHubSources
sources:
  - id: github-primary
    secret: test-secret
    repositories:
      - id: application
        repository_id: 101
      - id: operations
        repository_id: 101
`,
			locations: []SourceLocation{
				{Path: "/cfg/github.yaml", Field: "repositories[0]"},
				{Path: "/cfg/github.yaml", Field: "repositories[1]"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			bundle, err := Decode(Candidate{
				Global:    SourceFile{Path: "/cfg/hookfly.yaml", Content: []byte("kind: Hookfly\n")},
				Resources: []SourceFile{{Path: test.path, Content: []byte(test.resource)}},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = ValidateAndCanonicalize(bundle)
			if err == nil || !strings.Contains(err.Error(), "duplicate repository external ID") {
				t.Fatalf("ValidateAndCanonicalize() error = %v, want duplicate repository external ID", err)
			}
			for _, location := range test.locations {
				if !strings.Contains(err.Error(), location.String()) {
					t.Fatalf("ValidateAndCanonicalize() error = %v, want location %q", err, location.String())
				}
			}
		})
	}
}

func TestValidateAndCanonicalizeRejectsInvalidBundleReferencesAndValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Bundle)
		want   string
	}{
		{
			name: "duplicate repository ID within source",
			mutate: func(bundle *Bundle) {
				bundle.GitLabSources[0].Repositories = append(bundle.GitLabSources[0].Repositories, Repository{ID: "app", Name: "other", ExternalID: "2"})
			},
			want: "duplicate repository ID",
		},
		{
			name: "duplicate connection ID",
			mutate: func(bundle *Bundle) {
				bundle.DokployConnections = append(bundle.DokployConnections, DokployConnection{ID: "dokploy", BaseURL: "https://dokploy.example.invalid", APIKey: "second"})
			},
			want: "duplicate connection ID",
		},
		{
			name: "duplicate target ID",
			mutate: func(bundle *Bundle) {
				bundle.Targets = append(bundle.Targets, Target{ID: "production", Type: "dokploy", Connection: "dokploy", ResourceType: "compose", ResourceID: "second"})
			},
			want: "duplicate target ID",
		},
		{
			name: "duplicate route ID",
			mutate: func(bundle *Bundle) {
				bundle.Routes = append(bundle.Routes, Route{ID: "deploy", Priority: intPointer(20), Match: validRouteMatch(), Action: Action{Type: "record_only"}})
			},
			want: "duplicate route ID",
		},
		{
			name: "missing route source",
			mutate: func(bundle *Bundle) {
				bundle.Routes[0].Match.Source = "missing"
			},
			want: "unknown source",
		},
		{
			name: "missing route repository",
			mutate: func(bundle *Bundle) {
				bundle.Routes[0].Match.Repository = "missing"
			},
			want: "unknown repository",
		},
		{
			name: "missing target connection",
			mutate: func(bundle *Bundle) {
				bundle.Targets[0].Connection = "missing"
			},
			want: "unknown connection",
		},
		{
			name: "missing deploy target",
			mutate: func(bundle *Bundle) {
				bundle.Routes[0].Action.Targets = []string{"missing"}
			},
			want: "unknown target",
		},
		{
			name: "invalid identifier",
			mutate: func(bundle *Bundle) {
				bundle.Targets[0].ID = "not an identifier"
			},
			want: "invalid target ID",
		},
		{
			name: "nonpositive external ID",
			mutate: func(bundle *Bundle) {
				bundle.GitLabSources[0].Repositories[0].ExternalID = "0"
			},
			want: "external ID must be positive",
		},
		{
			name: "malformed Dokploy URL",
			mutate: func(bundle *Bundle) {
				bundle.DokployConnections[0].BaseURL = "://bad"
			},
			want: "invalid Dokploy URL",
		},
		{
			name: "unsupported target type",
			mutate: func(bundle *Bundle) {
				bundle.Targets[0].Type = "other"
			},
			want: "unsupported target type",
		},
		{
			name: "unsupported resource type",
			mutate: func(bundle *Bundle) {
				bundle.Targets[0].ResourceType = "service"
			},
			want: "unsupported resource type",
		},
		{
			name: "unsupported event",
			mutate: func(bundle *Bundle) {
				bundle.Routes[0].Match.Event = "unknown"
			},
			want: "unsupported event",
		},
		{
			name: "unsupported action",
			mutate: func(bundle *Bundle) {
				bundle.Routes[0].Action.Type = "other"
			},
			want: "unsupported action",
		},
		{
			name: "absent priority",
			mutate: func(bundle *Bundle) {
				bundle.Routes[0].Priority = nil
			},
			want: "priority is required",
		},
		{
			name: "duplicate action target",
			mutate: func(bundle *Bundle) {
				bundle.Routes[0].Action.Targets = []string{"production", "production"}
			},
			want: "duplicate target",
		},
		{
			name: "record only targets",
			mutate: func(bundle *Bundle) {
				bundle.Routes[0].Action = Action{Type: "record_only", Targets: []string{"production"}}
			},
			want: "record_only route",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bundle := validBundle()
			tt.mutate(bundle)
			err := ValidateAndCanonicalize(bundle)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateAndCanonicalize() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestValidateAndCanonicalizeRejectsUnsafeHTTPTargets(t *testing.T) {
	validHTTP := func() *Bundle {
		bundle := validBundle()
		bundle.DokployConnections = nil
		bundle.HTTPConnections = []HTTPConnection{{ID: "admin", BaseURL: "https://admin.example.invalid", Auth: HTTPAuthentication{Type: "api_key", Value: "secret", Header: "X-API-Token"}}}
		bundle.Targets = []Target{{
			ID: "production", Type: "http", Connection: "admin", Method: "POST", Path: "/api/deploy",
			Headers: map[string]string{"X-Deploy-Source": "hookfly"}, Body: &HTTPBody{Type: "json", Value: map[string]any{"ref": "{{ event.ref }}"}}, SuccessStatuses: []int{202},
		}}
		bundle.Routes[0].Action.Targets = []string{"production"}
		return bundle
	}
	for _, test := range []struct {
		name   string
		mutate func(*Bundle)
		want   string
	}{
		{name: "non-origin URL", mutate: func(bundle *Bundle) { bundle.HTTPConnections[0].BaseURL = "https://admin.example.invalid/base" }, want: "invalid HTTP URL"},
		{name: "unsafe path", mutate: func(bundle *Bundle) { bundle.Targets[0].Path = "https://other.example.invalid/deploy" }, want: "invalid HTTP path"},
		{name: "unsupported method", mutate: func(bundle *Bundle) { bundle.Targets[0].Method = "CONNECT" }, want: "unsupported HTTP method"},
		{name: "reserved header", mutate: func(bundle *Bundle) { bundle.Targets[0].Headers["Authorization"] = "bad" }, want: "reserved HTTP header"},
		{name: "unknown template", mutate: func(bundle *Bundle) {
			bundle.Targets[0].Body = &HTTPBody{Type: "json", Value: map[string]any{"ref": "{{ event.raw_payload }}"}}
		}, want: "unsupported HTTP template variable"},
		{name: "malformed template", mutate: func(bundle *Bundle) {
			bundle.Targets[0].Body = &HTTPBody{Type: "json", Value: map[string]any{"ref": "{{}}"}}
		}, want: "invalid HTTP template"},
		{name: "request environment reference", mutate: func(bundle *Bundle) { bundle.Targets[0].Headers["X-Deploy-Source"] = "${DEPLOY_SECRET}" }, want: "must not use environment references"},
		{name: "private network without opt in", mutate: func(bundle *Bundle) { bundle.HTTPConnections[0].BaseURL = "http://127.0.0.1:8080" }, want: "requires allow_private_network"},
		{name: "non-success status", mutate: func(bundle *Bundle) { bundle.Targets[0].SuccessStatuses = []int{500} }, want: "invalid HTTP success status"},
	} {
		t.Run(test.name, func(t *testing.T) {
			bundle := validHTTP()
			test.mutate(bundle)
			err := ValidateAndCanonicalize(bundle)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateAndCanonicalize() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateAndCanonicalizeSortsSemanticIDsAndRoutesByPrecedence(t *testing.T) {
	bundle := validBundle()
	bundle.GitLabSources = append(bundle.GitLabSources, GitLabSource{ID: "alpha", Token: "alpha-token", Repositories: []Repository{{ID: "z", Name: "z", ExternalID: "2"}, {ID: "a", Name: "a", ExternalID: "3"}}})
	bundle.GitHubSources = append(bundle.GitHubSources, GitHubSource{ID: "github", Secret: "secret", Repositories: []Repository{{ID: "repo", Name: "repo", ExternalID: "4"}}})
	bundle.DokployConnections = append(bundle.DokployConnections, DokployConnection{ID: "alpha", BaseURL: "https://alpha.example.invalid", APIKey: "key"})
	bundle.Targets = append(bundle.Targets, Target{ID: "alpha", Type: "dokploy", Connection: "alpha", ResourceType: "compose", ResourceID: "compose"})
	bundle.Routes = append(bundle.Routes,
		Route{ID: "observe", Priority: intPointer(20), Match: validRouteMatch(), Action: Action{Type: "record_only"}},
		Route{ID: "early", Priority: intPointer(5), Match: validRouteMatch(), Action: Action{Type: "record_only"}},
	)

	if err := ValidateAndCanonicalize(bundle); err != nil {
		t.Fatal(err)
	}
	if got, want := sourceIDs(bundle.GitLabSources), []string{"alpha", "gitlab"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("GitLab source order = %q, want %q", got, want)
	}
	if got, want := repositoryIDs(bundle.GitLabSources[0].Repositories), []string{"a", "z"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("repository order = %q, want %q", got, want)
	}
	if got, want := connectionIDs(bundle.DokployConnections), []string{"alpha", "dokploy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("connection order = %q, want %q", got, want)
	}
	if got, want := targetIDs(bundle.Targets), []string{"alpha", "production"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("target order = %q, want %q", got, want)
	}
	if got, want := routeIDs(bundle.Routes), []string{"early", "deploy", "observe"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("route order = %q, want %q", got, want)
	}
}

func TestValidateAndCanonicalizeRejectsEqualPriorityOverlaps(t *testing.T) {
	bundle := validBundle()
	bundle.Routes = append(bundle.Routes, Route{
		ID: "record", Priority: intPointer(10), Match: validRouteMatch(), Action: Action{Type: "record_only"},
	})

	err := ValidateAndCanonicalize(bundle)
	if err == nil || !strings.Contains(err.Error(), "equal-priority routes") {
		t.Fatalf("ValidateAndCanonicalize() error = %v, want equal-priority overlap rejection", err)
	}
}

func validBundle() *Bundle {
	return &Bundle{
		GitLabSources: []GitLabSource{{
			ID: "gitlab", Token: "token",
			Repositories: []Repository{{ID: "app", Name: "app", ExternalID: "1"}},
		}},
		DokployConnections: []DokployConnection{{ID: "dokploy", BaseURL: "https://dokploy.example.invalid", APIKey: "key"}},
		Targets:            []Target{{ID: "production", Type: "dokploy", Connection: "dokploy", ResourceType: "compose", ResourceID: "compose"}},
		Routes: []Route{{
			ID: "deploy", Priority: intPointer(10), Match: validRouteMatch(),
			Action: Action{Type: "deploy", Targets: []string{"production"}},
		}},
	}
}

func validRouteMatch() RouteMatch {
	return RouteMatch{Source: "gitlab", Repository: "app", Event: "pipeline"}
}

func intPointer(value int) *int { return &value }

func sourceIDs(sources []GitLabSource) []string {
	ids := make([]string, len(sources))
	for index := range sources {
		ids[index] = sources[index].ID
	}
	return ids
}

func repositoryIDs(repositories []Repository) []string {
	ids := make([]string, len(repositories))
	for index := range repositories {
		ids[index] = repositories[index].ID
	}
	return ids
}

func connectionIDs(connections []DokployConnection) []string {
	ids := make([]string, len(connections))
	for index := range connections {
		ids[index] = connections[index].ID
	}
	return ids
}

func targetIDs(targets []Target) []string {
	ids := make([]string, len(targets))
	for index := range targets {
		ids[index] = targets[index].ID
	}
	return ids
}

func routeIDs(routes []Route) []string {
	ids := make([]string, len(routes))
	for index := range routes {
		ids[index] = routes[index].ID
	}
	return ids
}
