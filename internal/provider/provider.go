// Package provider defines the common webhook-provider boundary.
package provider

import "net/http"

// Adapter authenticates and normalizes incoming webhook requests.
type Adapter interface {
	Authenticate(headers http.Header, body []byte) bool
	Normalize(headers http.Header, body []byte) (IncomingEvent, error)
}

// IncomingEvent is the provider-independent webhook representation used by routes.
type IncomingEvent struct {
	RepositoryExternalID string
	Event                string
	Ref                  string
	Status               string
	Revision             string
	CommitMessage        string
	ExternalID           string
	Trigger              string
	DeliveryID           string
	Supported            bool
	SafeHeaders          map[string]string
}
