// Package harbor normalizes Harbor webhook requests.
package harbor

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hoomoli/hookfly/internal/provider"
)

type adapter struct {
	authorization string
}

type envelope struct {
	SpecVersion string    `json:"specversion"`
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	EventData   eventData `json:"event_data"`
	Data        eventData `json:"data"`
}

type eventData struct {
	Resources  []resource `json:"resources"`
	Repository repository `json:"repository"`
}

type resource struct {
	Digest string `json:"digest"`
	Tag    string `json:"tag"`
}

type repository struct {
	FullName string `json:"repo_full_name"`
}

// New returns a Harbor webhook adapter configured with an exact Authorization value.
func New(authorization string) provider.Adapter {
	return adapter{authorization: authorization}
}

func (a adapter) Authenticate(headers http.Header, _ []byte) bool {
	provided := headers.Get("Authorization")
	return a.authorization != "" && provided != "" &&
		subtle.ConstantTimeCompare([]byte(provided), []byte(a.authorization)) == 1
}

func (a adapter) Normalize(headers http.Header, body []byte) (provider.IncomingEvent, error) {
	payload, err := decodeEnvelope(body)
	if err != nil {
		return provider.IncomingEvent{}, err
	}
	eventName, supported := normalizedEventName(payload.Type)
	if eventName == "" {
		return provider.IncomingEvent{}, fmt.Errorf("Harbor event type is required")
	}
	data := payload.EventData
	if payload.SpecVersion != "" {
		data = payload.Data
	}
	event := provider.IncomingEvent{
		RepositoryExternalID: strings.TrimSpace(data.Repository.FullName),
		Event:                eventName,
		Supported:            supported,
		SafeHeaders:          harborSafeHeaders(headers),
	}
	if payload.SpecVersion != "" {
		event.ExternalID = strings.TrimSpace(payload.ID)
		event.DeliveryID = event.ExternalID
	}
	if !supported {
		return event, nil
	}
	if event.RepositoryExternalID == "" {
		return provider.IncomingEvent{}, fmt.Errorf("Harbor payload has no repository full name")
	}
	if len(data.Resources) == 0 {
		return provider.IncomingEvent{}, fmt.Errorf("Harbor artifact push has no resources")
	}
	event.Ref = strings.TrimSpace(data.Resources[0].Tag)
	event.Revision = strings.TrimSpace(data.Resources[0].Digest)
	if event.Revision == "" {
		return provider.IncomingEvent{}, fmt.Errorf("Harbor artifact push has no digest")
	}
	return event, nil
}

func decodeEnvelope(body []byte) (envelope, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var payload envelope
	if err := decoder.Decode(&payload); err != nil {
		return envelope{}, fmt.Errorf("decode Harbor payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return envelope{}, fmt.Errorf("decode Harbor payload: multiple JSON values are not supported")
		}
		return envelope{}, fmt.Errorf("decode Harbor payload: %w", err)
	}
	return payload, nil
}

func normalizedEventName(event string) (string, bool) {
	switch strings.TrimSpace(event) {
	case "PUSH_ARTIFACT", "harbor.artifact.pushed":
		return "artifact_push", true
	case "PULL_ARTIFACT", "harbor.artifact.pulled":
		return "artifact_pull", false
	case "DELETE_ARTIFACT", "harbor.artifact.deleted":
		return "artifact_delete", false
	case "SCANNING_COMPLETED", "harbor.scan.completed":
		return "scan_completed", false
	case "SCANNING_STOPPED", "harbor.scan.stopped":
		return "scan_stopped", false
	case "SCANNING_FAILED", "harbor.scan.failed":
		return "scan_failed", false
	case "QUOTA_EXCEED", "harbor.quota.exceeded":
		return "quota_exceeded", false
	case "QUOTA_WARNING", "harbor.quota.warned":
		return "quota_warning", false
	case "REPLICATION", "harbor.replication.status.changed":
		return "replication", false
	case "TAG_RETENTION", "harbor.tag_retention.finished":
		return "tag_retention", false
	}
	return strings.ToLower(strings.TrimSpace(event)), false
}

func harborSafeHeaders(headers http.Header) map[string]string {
	safe := map[string]string{}
	if contentType := headers.Get("Content-Type"); contentType != "" {
		safe["Content-Type"] = contentType
	}
	return safe
}
