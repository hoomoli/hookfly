package gitlab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/hoomoli/hookfly/internal/provider"
)

type adapter struct {
	token string
}

// New returns a GitLab webhook adapter configured with token.
func New(token string) provider.Adapter {
	return adapter{token: token}
}

func (a adapter) Authenticate(headers http.Header, _ []byte) bool {
	return TokenMatches(headers.Get("X-Gitlab-Token"), a.token)
}

func (a adapter) Normalize(headers http.Header, body []byte) (provider.IncomingEvent, error) {
	eventName, supported := normalizedEventName(headers.Get("X-Gitlab-Event"))
	if eventName == "" {
		return provider.IncomingEvent{}, fmt.Errorf("GitLab event header is required")
	}
	payload, err := decodeAdapterObject(body)
	if err != nil {
		return provider.IncomingEvent{}, err
	}

	event := provider.IncomingEvent{
		Event:       eventName,
		DeliveryID:  gitLabDeliveryID(headers),
		Supported:   supported,
		SafeHeaders: gitLabSafeHeaders(headers),
	}
	if !supported {
		event.RepositoryExternalID = optionalGitLabRepositoryExternalID(payload)
		return event, nil
	}

	repositoryID, err := requiredGitLabRepositoryExternalID(payload, eventName)
	if err != nil {
		return provider.IncomingEvent{}, err
	}
	event.RepositoryExternalID = repositoryID
	switch eventName {
	case "pipeline":
		event.Ref = stringAt(payload, "object_attributes", "ref")
		event.Status = lowercase(stringAt(payload, "object_attributes", "status"))
		event.Revision = stringAt(payload, "object_attributes", "sha")
		event.CommitMessage = stringAt(payload, "commit", "message")
		event.ExternalID = numberStringAt(payload, "object_attributes", "id")
		event.Trigger = lowercase(stringAt(payload, "object_attributes", "source"))
	case "job":
		event.Ref = stringAt(payload, "ref")
		event.Status = lowercase(stringAt(payload, "build_status"))
		event.Revision = stringAt(payload, "sha")
		event.CommitMessage = stringAt(payload, "commit", "message")
		event.ExternalID = numberStringAt(payload, "pipeline_id")
	case "push":
		event.Ref = strings.TrimPrefix(stringAt(payload, "ref"), "refs/heads/")
		event.Revision = stringAt(payload, "after")
		event.CommitMessage = lastObjectStringAt(payload, "commits", "message")
	case "tag_push":
		event.Ref = strings.TrimPrefix(stringAt(payload, "ref"), "refs/tags/")
		event.Revision = stringAt(payload, "after")
		event.CommitMessage = lastObjectStringAt(payload, "commits", "message")
	case "merge_request":
		event.Ref = stringAt(payload, "object_attributes", "source_branch")
		event.Status = lowercase(stringAt(payload, "object_attributes", "state"))
		event.Revision = stringAt(payload, "object_attributes", "last_commit", "id")
		event.CommitMessage = stringAt(payload, "object_attributes", "last_commit", "message")
		event.ExternalID = numberStringAt(payload, "object_attributes", "id")
		event.Trigger = lowercase(stringAt(payload, "object_attributes", "action"))
	}
	return event, nil
}

func decodeAdapterObject(body []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode GitLab payload: %w", err)
	}
	if payload == nil {
		return nil, fmt.Errorf("decode GitLab payload: expected JSON object")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode GitLab payload: multiple JSON values are not supported")
		}
		return nil, fmt.Errorf("decode GitLab payload: %w", err)
	}
	return payload, nil
}

func requiredGitLabRepositoryExternalID(payload map[string]any, event string) (string, error) {
	path := []string{"project_id"}
	if event == "pipeline" || event == "merge_request" {
		path = []string{"project", "id"}
	}
	value, ok := valueAt(payload, path...)
	if !ok {
		return "", fmt.Errorf("GitLab payload has no repository ID")
	}
	number, ok := value.(json.Number)
	if !ok {
		return "", fmt.Errorf("GitLab repository ID must be a positive integer")
	}
	id, err := number.Int64()
	if err != nil || id <= 0 {
		return "", fmt.Errorf("GitLab repository ID must be a positive integer")
	}
	return strconv.FormatInt(id, 10), nil
}

func optionalGitLabRepositoryExternalID(payload map[string]any) string {
	for _, path := range [][]string{{"project", "id"}, {"project_id"}} {
		value, ok := valueAt(payload, path...)
		if !ok {
			continue
		}
		number, ok := value.(json.Number)
		if !ok {
			return ""
		}
		id, err := number.Int64()
		if err == nil && id > 0 {
			return strconv.FormatInt(id, 10)
		}
		return ""
	}
	return ""
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

func gitLabDeliveryID(headers http.Header) string {
	if eventUUID := headers.Get("X-Gitlab-Event-UUID"); eventUUID != "" {
		return eventUUID
	}
	return headers.Get("X-Gitlab-Webhook-UUID")
}

func gitLabSafeHeaders(headers http.Header) map[string]string {
	safe := map[string]string{"X-Gitlab-Event": headers.Get("X-Gitlab-Event")}
	if eventUUID := headers.Get("X-Gitlab-Event-UUID"); eventUUID != "" {
		safe["X-Gitlab-Event-UUID"] = eventUUID
	}
	if webhookUUID := headers.Get("X-Gitlab-Webhook-UUID"); webhookUUID != "" {
		safe["X-Gitlab-Webhook-UUID"] = webhookUUID
	}
	return safe
}
