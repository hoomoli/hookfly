package gitlab

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/hoomoli/hookfly/internal/provider"
)

func TestAdapterAuthenticateRequiresMatchingNonemptyToken(t *testing.T) {
	adapter := New("token")
	for _, tt := range []struct {
		name  string
		token string
		want  bool
	}{
		{name: "matching token", token: "token", want: true},
		{name: "different token", token: "other", want: false},
		{name: "missing token", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			headers.Set("X-Gitlab-Token", tt.token)
			if got := adapter.Authenticate(headers, []byte(`{"ignored":true}`)); got != tt.want {
				t.Fatalf("Authenticate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAdapterNormalizeSupportedGitLabEvents(t *testing.T) {
	adapter := New("token")
	tests := []struct {
		name   string
		header string
		body   string
		want   provider.IncomingEvent
	}{
		{
			name: "pipeline", header: "Pipeline Hook",
			body: `{"project":{"id":12345},"object_attributes":{"ref":"main","status":"SUCCESS","sha":"abc123","id":88,"source":"PUSH"},"commit":{"message":"fix checkout timeout"}}`,
			want: provider.IncomingEvent{RepositoryExternalID: "12345", Event: "pipeline", Ref: "main", Status: "success", Revision: "abc123", CommitMessage: "fix checkout timeout", ExternalID: "88", Trigger: "push", Supported: true},
		},
		{
			name: "job", header: "Job Hook",
			body: `{"project_id":12345,"ref":"main","build_status":"FAILED","pipeline_id":88,"sha":"abc123"}`,
			want: provider.IncomingEvent{RepositoryExternalID: "12345", Event: "job", Ref: "main", Status: "failed", Revision: "abc123", ExternalID: "88", Supported: true},
		},
		{
			name: "push", header: "Push Hook",
			body: `{"project_id":12345,"ref":"refs/heads/main","after":"abc123","commits":[{"message":"first"},{"message":"release 2.4.1"}]}`,
			want: provider.IncomingEvent{RepositoryExternalID: "12345", Event: "push", Ref: "main", Revision: "abc123", CommitMessage: "release 2.4.1", Supported: true},
		},
		{
			name: "tag push", header: "Tag Push Hook",
			body: `{"project_id":12345,"ref":"refs/tags/v1.0.0","after":"abc123"}`,
			want: provider.IncomingEvent{RepositoryExternalID: "12345", Event: "tag_push", Ref: "v1.0.0", Revision: "abc123", Supported: true},
		},
		{
			name: "merge request", header: "Merge Request Hook",
			body: `{"project":{"id":12345},"object_attributes":{"source_branch":"feature","state":"OPENED","last_commit":{"id":"abc123","message":"add feature"},"id":88,"action":"UPDATE"}}`,
			want: provider.IncomingEvent{RepositoryExternalID: "12345", Event: "merge_request", Ref: "feature", Status: "opened", Revision: "abc123", CommitMessage: "add feature", ExternalID: "88", Trigger: "update", Supported: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := gitLabHeaders(tt.header)
			got, err := adapter.Normalize(headers, []byte(tt.body))
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			tt.want.DeliveryID = "event-uuid"
			tt.want.SafeHeaders = map[string]string{
				"X-Gitlab-Event": tt.header, "X-Gitlab-Event-UUID": "event-uuid", "X-Gitlab-Webhook-UUID": "webhook-uuid",
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Normalize() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestAdapterNormalizeRejectsInvalidGitLabPayloads(t *testing.T) {
	adapter := New("token")
	for _, tt := range []struct {
		name   string
		header string
		body   string
	}{
		{name: "missing event header", body: `{}`},
		{name: "malformed JSON", header: "Pipeline Hook", body: `{`},
		{name: "multiple JSON values", header: "Pipeline Hook", body: `{} {}`},
		{name: "missing repository ID", header: "Pipeline Hook", body: `{"project":{}}`},
		{name: "nonnumeric repository ID", header: "Pipeline Hook", body: `{"project":{"id":"12345"}}`},
		{name: "nonpositive repository ID", header: "Pipeline Hook", body: `{"project":{"id":0}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := adapter.Normalize(gitLabHeaders(tt.header), []byte(tt.body)); err == nil {
				t.Fatal("Normalize() error = nil, want malformed payload error")
			}
		})
	}
}

func TestAdapterNormalizeKeepsUnknownGitLabEventUnsupportedAndRedactsToken(t *testing.T) {
	adapter := New("token")
	headers := gitLabHeaders("Custom Hook")
	headers.Set("X-Gitlab-Token", "token")

	got, err := adapter.Normalize(headers, []byte(`{"project":{"id":12345}}`))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	want := provider.IncomingEvent{
		RepositoryExternalID: "12345", Event: "custom_hook", DeliveryID: "event-uuid", Supported: false,
		SafeHeaders: map[string]string{"X-Gitlab-Event": "Custom Hook", "X-Gitlab-Event-UUID": "event-uuid", "X-Gitlab-Webhook-UUID": "webhook-uuid"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Normalize() = %#v, want %#v", got, want)
	}
	if _, present := got.SafeHeaders["X-Gitlab-Token"]; present {
		t.Fatal("Normalize() retained X-Gitlab-Token in safe headers")
	}
}

func gitLabHeaders(event string) http.Header {
	headers := make(http.Header)
	headers.Set("X-Gitlab-Event", event)
	headers.Set("X-Gitlab-Event-UUID", "event-uuid")
	headers.Set("X-Gitlab-Webhook-UUID", "webhook-uuid")
	return headers
}
