package harbor

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/hoomoli/hookfly/internal/provider"
)

func TestAdapterAuthenticateRequiresExactNonemptyAuthorization(t *testing.T) {
	adapter := New("Bearer harbor-secret")
	for _, tt := range []struct {
		name          string
		authorization string
		want          bool
	}{
		{name: "matching authorization", authorization: "Bearer harbor-secret", want: true},
		{name: "different authorization", authorization: "Bearer other", want: false},
		{name: "missing authorization", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			headers.Set("Authorization", tt.authorization)
			if got := adapter.Authenticate(headers, []byte(`{"ignored":true}`)); got != tt.want {
				t.Fatalf("Authenticate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAdapterNormalizeArtifactPushFormats(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		want        provider.IncomingEvent
	}{
		{
			name: "default", contentType: "application/json",
			body: `{"type":"PUSH_ARTIFACT","occur_at":1680501893,"operator":"harbor-jobservice","event_data":{"resources":[{"digest":"sha256:abc123","tag":"latest","resource_url":"registry.example.invalid/team/application:latest"}],"repository":{"name":"application","namespace":"team","repo_full_name":"team/application","repo_type":"private"}}}`,
			want: provider.IncomingEvent{RepositoryExternalID: "team/application", Event: "artifact_push", Ref: "latest", Revision: "sha256:abc123", Supported: true},
		},
		{
			name: "CloudEvents", contentType: "application/cloudevents+json",
			body: `{"specversion":"1.0","id":"66e18103-09c1-41f6-982f-37df223f3eeb","requestid":"51c0b694-0168-4f3c-b0db-282565455d7b","source":"/projects/2/webhook/policies/15","type":"harbor.artifact.pushed","datacontenttype":"application/json","time":"2023-04-03T06:04:46Z","data":{"resources":[{"digest":"sha256:abc123","tag":"stable","resource_url":"registry.example.invalid/team/application:stable"}],"repository":{"name":"application","namespace":"team","repo_full_name":"team/application","repo_type":"private"}},"operator":"harbor-jobservice"}`,
			want: provider.IncomingEvent{RepositoryExternalID: "team/application", Event: "artifact_push", Ref: "stable", Revision: "sha256:abc123", ExternalID: "66e18103-09c1-41f6-982f-37df223f3eeb", DeliveryID: "66e18103-09c1-41f6-982f-37df223f3eeb", Supported: true},
		},
	}

	adapter := New("Bearer harbor-secret")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			headers.Set("Content-Type", tt.contentType)
			headers.Set("Authorization", "Bearer harbor-secret")
			got, err := adapter.Normalize(headers, []byte(tt.body))
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			tt.want.SafeHeaders = map[string]string{"Content-Type": tt.contentType}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Normalize() = %#v, want %#v", got, tt.want)
			}
			if _, present := got.SafeHeaders["Authorization"]; present {
				t.Fatal("Normalize() retained Authorization in safe headers")
			}
		})
	}
}

func TestAdapterNormalizeKeepsNonPushEventsUnsupported(t *testing.T) {
	adapter := New("Bearer harbor-secret")
	body := []byte(`{"type":"PULL_ARTIFACT","event_data":{"resources":[{"digest":"sha256:abc123","tag":"latest"}],"repository":{"repo_full_name":"team/application"}}}`)
	got, err := adapter.Normalize(http.Header{"Content-Type": []string{"application/json"}}, body)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	want := provider.IncomingEvent{
		RepositoryExternalID: "team/application", Event: "artifact_pull", Supported: false,
		SafeHeaders: map[string]string{"Content-Type": "application/json"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Normalize() = %#v, want %#v", got, want)
	}
}

func TestAdapterNormalizeRejectsInvalidArtifactPushPayloads(t *testing.T) {
	adapter := New("Bearer harbor-secret")
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "malformed JSON", body: `{`},
		{name: "multiple JSON values", body: `{} {}`},
		{name: "missing event type", body: `{}`},
		{name: "missing repository", body: `{"type":"PUSH_ARTIFACT","event_data":{"resources":[{"digest":"sha256:abc123"}]}}`},
		{name: "missing resource", body: `{"type":"PUSH_ARTIFACT","event_data":{"repository":{"repo_full_name":"team/application"}}}`},
		{name: "missing digest", body: `{"type":"PUSH_ARTIFACT","event_data":{"resources":[{"tag":"latest"}],"repository":{"repo_full_name":"team/application"}}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := adapter.Normalize(nil, []byte(tt.body)); err == nil {
				t.Fatal("Normalize() error = nil, want malformed payload error")
			}
		})
	}
}
