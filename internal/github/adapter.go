// Package github normalizes GitHub webhook requests.
package github

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/hoomoli/hookfly/internal/provider"
)

type adapter struct {
	secret string
}

// New returns a GitHub webhook adapter configured with secret.
func New(secret string) provider.Adapter {
	return adapter{secret: secret}
}

func (a adapter) Authenticate(headers http.Header, body []byte) bool {
	if a.secret == "" {
		return false
	}
	signature := headers.Get("X-Hub-Signature-256")
	if !validSignature(signature) {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(a.secret))
	_, _ = mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}

func (a adapter) Normalize(headers http.Header, body []byte) (provider.IncomingEvent, error) {
	headerEvent := strings.ToLower(strings.TrimSpace(headers.Get("X-GitHub-Event")))
	if headerEvent == "" {
		return provider.IncomingEvent{}, fmt.Errorf("GitHub event header is required")
	}
	payload, err := decodeObject(body)
	if err != nil {
		return provider.IncomingEvent{}, err
	}
	eventName, supported := normalizedEventName(headerEvent)
	event := provider.IncomingEvent{
		Event:       eventName,
		DeliveryID:  headers.Get("X-GitHub-Delivery"),
		Supported:   supported,
		SafeHeaders: gitHubSafeHeaders(headers),
	}
	if !supported {
		event.RepositoryExternalID = optionalRepositoryExternalID(payload)
		return event, nil
	}
	repositoryID, err := requiredRepositoryExternalID(payload)
	if err != nil {
		return provider.IncomingEvent{}, err
	}
	event.RepositoryExternalID = repositoryID
	switch headerEvent {
	case "workflow_run":
		event.Ref = stringAt(payload, "workflow_run", "head_branch")
		event.Status = completedStatus(payload, "workflow_run")
		event.Revision = stringAt(payload, "workflow_run", "head_sha")
		event.CommitMessage = stringAt(payload, "workflow_run", "head_commit", "message")
		event.ExternalID = numberStringAt(payload, "workflow_run", "id")
		event.Trigger = stringAt(payload, "workflow_run", "event")
	case "workflow_job":
		event.Ref = stringAt(payload, "workflow_job", "head_branch")
		event.Status = completedStatus(payload, "workflow_job")
		event.Revision = stringAt(payload, "workflow_job", "head_sha")
		event.ExternalID = numberStringAt(payload, "workflow_job", "id")
	case "push":
		ref := stringAt(payload, "ref")
		if strings.HasPrefix(ref, "refs/tags/") {
			event.Event = "tag_push"
			event.Ref = strings.TrimPrefix(ref, "refs/tags/")
		} else {
			event.Ref = strings.TrimPrefix(ref, "refs/heads/")
		}
		event.Revision = stringAt(payload, "after")
		event.CommitMessage = stringAt(payload, "head_commit", "message")
	case "pull_request":
		event.Ref = stringAt(payload, "pull_request", "head", "ref")
		event.Status = lowercase(stringAt(payload, "pull_request", "state"))
		event.Revision = stringAt(payload, "pull_request", "head", "sha")
		event.ExternalID = numberStringAt(payload, "number")
		event.Trigger = lowercase(stringAt(payload, "action"))
	}
	return event, nil
}

func validSignature(signature string) bool {
	if !strings.HasPrefix(signature, "sha256=") || len(signature) != len("sha256=")+sha256.Size*2 {
		return false
	}
	for _, character := range signature[len("sha256="):] {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func decodeObject(body []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode GitHub payload: %w", err)
	}
	if payload == nil {
		return nil, fmt.Errorf("decode GitHub payload: expected JSON object")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode GitHub payload: multiple JSON values are not supported")
		}
		return nil, fmt.Errorf("decode GitHub payload: %w", err)
	}
	return payload, nil
}

func normalizedEventName(event string) (string, bool) {
	switch event {
	case "workflow_run":
		return "pipeline", true
	case "workflow_job":
		return "job", true
	case "push":
		return "push", true
	case "pull_request":
		return "merge_request", true
	}
	return event, false
}

func requiredRepositoryExternalID(payload map[string]any) (string, error) {
	value, ok := valueAt(payload, "repository", "id")
	if !ok {
		return "", fmt.Errorf("GitHub payload has no repository ID")
	}
	number, ok := value.(json.Number)
	if !ok {
		return "", fmt.Errorf("GitHub repository ID must be a positive integer")
	}
	id, err := number.Int64()
	if err != nil || id <= 0 {
		return "", fmt.Errorf("GitHub repository ID must be a positive integer")
	}
	return strconv.FormatInt(id, 10), nil
}

func optionalRepositoryExternalID(payload map[string]any) string {
	value, ok := valueAt(payload, "repository", "id")
	if !ok {
		return ""
	}
	number, ok := value.(json.Number)
	if !ok {
		return ""
	}
	id, err := number.Int64()
	if err != nil || id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func completedStatus(payload map[string]any, path string) string {
	if conclusion := stringAt(payload, path, "conclusion"); conclusion != "" {
		return lowercase(conclusion)
	}
	return lowercase(stringAt(payload, path, "status"))
}

func stringAt(payload map[string]any, path ...string) string {
	value, ok := valueAt(payload, path...)
	if !ok {
		return ""
	}
	stringValue, _ := value.(string)
	return stringValue
}

func numberStringAt(payload map[string]any, path ...string) string {
	value, ok := valueAt(payload, path...)
	if !ok {
		return ""
	}
	number, ok := value.(json.Number)
	if !ok {
		return ""
	}
	return number.String()
}

func valueAt(payload map[string]any, path ...string) (any, bool) {
	var current any = payload
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func lowercase(value string) string {
	return strings.ToLower(value)
}

func gitHubSafeHeaders(headers http.Header) map[string]string {
	safe := map[string]string{"X-GitHub-Event": headers.Get("X-GitHub-Event")}
	if deliveryID := headers.Get("X-GitHub-Delivery"); deliveryID != "" {
		safe["X-GitHub-Delivery"] = deliveryID
	}
	return safe
}
