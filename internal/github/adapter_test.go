package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"reflect"
	"testing"

	"github.com/hoomoli/hookfly/internal/provider"
)

func TestAdapterAuthenticateRequiresExactGitHubHMAC(t *testing.T) {
	adapter := New("secret")
	body := []byte(`{"repository":{"id":12345}}`)
	for _, tt := range []struct {
		name      string
		signature string
		payload   []byte
		want      bool
	}{
		{name: "matching signature", signature: sign("secret", body), payload: body, want: true},
		{name: "missing signature", payload: body, want: false},
		{name: "malformed signature", signature: "sha256=ABCDEF", payload: body, want: false},
		{name: "incorrect algorithm", signature: "sha1=" + hex.EncodeToString(make([]byte, sha256.Size)), payload: body, want: false},
		{name: "changed body", signature: sign("secret", body), payload: []byte(`{"repository":{"id":12346}}`), want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			headers := make(http.Header)
			headers.Set("X-Hub-Signature-256", tt.signature)
			if got := adapter.Authenticate(headers, tt.payload); got != tt.want {
				t.Fatalf("Authenticate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAdapterNormalizeSupportedGitHubEvents(t *testing.T) {
	adapter := New("secret")
	tests := []struct {
		name  string
		event string
		body  string
		want  provider.IncomingEvent
	}{
		{
			name: "workflow run", event: "workflow_run",
			body: `{"repository":{"id":12345},"workflow_run":{"head_branch":"main","conclusion":"SUCCESS","status":"completed","head_sha":"abc123","head_commit":{"message":"fix checkout timeout"},"id":88,"event":"push"}}`,
			want: provider.IncomingEvent{RepositoryExternalID: "12345", Event: "pipeline", Ref: "main", Status: "success", Revision: "abc123", CommitMessage: "fix checkout timeout", ExternalID: "88", Trigger: "push", Supported: true},
		},
		{
			name: "workflow job", event: "workflow_job",
			body: `{"repository":{"id":12345},"workflow_job":{"head_branch":"main","conclusion":"","status":"IN_PROGRESS","head_sha":"abc123","id":88}}`,
			want: provider.IncomingEvent{RepositoryExternalID: "12345", Event: "job", Ref: "main", Status: "in_progress", Revision: "abc123", ExternalID: "88", Supported: true},
		},
		{
			name: "push", event: "push",
			body: `{"repository":{"id":12345},"ref":"refs/heads/main","after":"abc123","head_commit":{"message":"release 2.4.1"}}`,
			want: provider.IncomingEvent{RepositoryExternalID: "12345", Event: "push", Ref: "main", Revision: "abc123", CommitMessage: "release 2.4.1", Supported: true},
		},
		{
			name: "tag push", event: "push",
			body: `{"repository":{"id":12345},"ref":"refs/tags/v1.2.3","after":"abc123"}`,
			want: provider.IncomingEvent{RepositoryExternalID: "12345", Event: "tag_push", Ref: "v1.2.3", Revision: "abc123", Supported: true},
		},
		{
			name: "pull request", event: "pull_request",
			body: `{"repository":{"id":12345},"number":88,"action":"OPENED","pull_request":{"head":{"ref":"feature","sha":"abc123"},"state":"OPEN"}}`,
			want: provider.IncomingEvent{RepositoryExternalID: "12345", Event: "merge_request", Ref: "feature", Status: "open", Revision: "abc123", ExternalID: "88", Trigger: "opened", Supported: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := adapter.Normalize(gitHubHeaders(tt.event), []byte(tt.body))
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			tt.want.DeliveryID = "delivery-uuid"
			tt.want.SafeHeaders = map[string]string{"X-GitHub-Event": tt.event, "X-GitHub-Delivery": "delivery-uuid"}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Normalize() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestAdapterNormalizeRejectsInvalidGitHubPayloads(t *testing.T) {
	adapter := New("secret")
	for _, tt := range []struct {
		name  string
		event string
		body  string
	}{
		{name: "missing event header", body: `{}`},
		{name: "malformed JSON", event: "push", body: `{`},
		{name: "multiple JSON values", event: "push", body: `{} {}`},
		{name: "missing repository ID", event: "push", body: `{"repository":{}}`},
		{name: "nonnumeric repository ID", event: "push", body: `{"repository":{"id":"12345"}}`},
		{name: "nonpositive repository ID", event: "push", body: `{"repository":{"id":0}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := adapter.Normalize(gitHubHeaders(tt.event), []byte(tt.body)); err == nil {
				t.Fatal("Normalize() error = nil, want malformed payload error")
			}
		})
	}
}

func TestAdapterNormalizeRedactsGitHubSignature(t *testing.T) {
	adapter := New("secret")
	headers := gitHubHeaders("custom_event")
	headers.Set("X-Hub-Signature-256", sign("secret", []byte(`{"repository":{"id":12345}}`)))

	got, err := adapter.Normalize(headers, []byte(`{"repository":{"id":12345}}`))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if got.Supported {
		t.Fatal("Normalize() supported unknown event")
	}
	if _, present := got.SafeHeaders["X-Hub-Signature-256"]; present {
		t.Fatal("Normalize() retained X-Hub-Signature-256 in safe headers")
	}
}

func gitHubHeaders(event string) http.Header {
	headers := make(http.Header)
	headers.Set("X-GitHub-Event", event)
	headers.Set("X-GitHub-Delivery", "delivery-uuid")
	return headers
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
