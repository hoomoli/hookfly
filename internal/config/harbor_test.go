package config

import (
	"strings"
	"testing"
)

func TestDecodeAndValidateHarborSourcesAndArtifactPushRoute(t *testing.T) {
	priority := 10
	candidate := Candidate{
		Global: SourceFile{Path: "/cfg/hookfly.yaml", Content: []byte("kind: Hookfly\n")},
		Resources: []SourceFile{
			{Path: "/cfg/conf.d/harbor.yaml", Content: []byte(`kind: HarborSources
sources:
  - id: harbor-primary
    authorization: ${HARBOR_AUTHORIZATION}
    repositories:
      - id: application-image
        name: Application Image
        repository: team/application
`)},
			{Path: "/cfg/conf.d/routes.yaml", Content: []byte(`kind: Routes
routes:
  - id: deploy-application-image
    priority: 10
    match:
      source: harbor-primary
      repository: application-image
      event: artifact_push
      ref: latest
    action:
      type: record_only
`)},
		},
	}
	bundle, err := Decode(candidate, func(name string) (string, bool) {
		return "Bearer harbor-secret", name == "HARBOR_AUTHORIZATION"
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAndCanonicalize(bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.HarborSources) != 1 {
		t.Fatalf("HarborSources = %#v", bundle.HarborSources)
	}
	source := bundle.HarborSources[0]
	if source.ID != "harbor-primary" || source.Authorization != "Bearer harbor-secret" || len(source.Repositories) != 1 {
		t.Fatalf("Harbor source = %#v", source)
	}
	if got := source.Repositories[0]; got.ID != "application-image" || got.Name != "Application Image" || got.ExternalID != "team/application" {
		t.Fatalf("Harbor repository = %#v", got)
	}
	if bundle.Routes[0].Priority == nil || *bundle.Routes[0].Priority != priority || bundle.Routes[0].Match.Event != "artifact_push" {
		t.Fatalf("Harbor route = %#v", bundle.Routes[0])
	}
}

func TestDecodeHarborAuthorizationRequiresEnvironmentValue(t *testing.T) {
	candidate := Candidate{
		Global: SourceFile{Path: "/cfg/hookfly.yaml", Content: []byte("kind: Hookfly\n")},
		Resources: []SourceFile{{Path: "/cfg/harbor.yaml", Content: []byte(`kind: HarborSources
sources:
  - id: harbor-primary
    authorization: ${HARBOR_AUTHORIZATION}
`)}},
	}
	if _, err := Decode(candidate, nil); err == nil || !strings.Contains(err.Error(), "HARBOR_AUTHORIZATION") {
		t.Fatalf("Decode() error = %v, want missing Harbor authorization", err)
	}
}

func TestValidateHarborRepositoryRequiresFullName(t *testing.T) {
	for _, repository := range []string{"", "application", "/application", "team/", "team//application"} {
		t.Run(repository, func(t *testing.T) {
			bundle := &Bundle{HarborSources: []HarborSource{{
				ID: "harbor-primary", Authorization: "Bearer harbor-secret",
				Repositories: []Repository{{ID: "application", ExternalID: repository}},
			}}}
			if err := ValidateAndCanonicalize(bundle); err == nil || !strings.Contains(err.Error(), "Harbor repository must be a namespace/name path") {
				t.Fatalf("ValidateAndCanonicalize() error = %v", err)
			}
		})
	}
}

func TestValidateRejectsDuplicateSourceIDBetweenHarborAndGitProviders(t *testing.T) {
	bundle := &Bundle{
		GitHubSources: []GitHubSource{{ID: "shared", Secret: "github-secret"}},
		HarborSources: []HarborSource{{ID: "shared", Authorization: "Bearer harbor-secret"}},
	}
	if err := ValidateAndCanonicalize(bundle); err == nil || !strings.Contains(err.Error(), "duplicate source ID") {
		t.Fatalf("ValidateAndCanonicalize() error = %v, want duplicate source ID", err)
	}
}
