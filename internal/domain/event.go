package domain

import "time"

// CanonicalEvent is the provider-independent event representation used by routes.
type CanonicalEvent struct {
	Provider      string
	Source        string
	Repository    string
	Event         string
	Ref           string
	Status        string
	Revision      string
	CommitMessage string
	ExternalID    string
	Trigger       string
}

// RoutingResult records how an event was handled by the configured rules.
type RoutingResult string

const (
	RoutingUnmatched   RoutingResult = "unmatched"
	RoutingRecordOnly  RoutingResult = "record_only"
	RoutingDeploy      RoutingResult = "deploy"
	RoutingDeferred    RoutingResult = "deferred"
	RoutingUnsupported RoutingResult = "unsupported"
)

// NewDelivery is a delivery created for a matched deployment target.
type NewDelivery struct {
	TargetID       string
	TargetSnapshot []byte
	DispatchKey    string
}

// RouteDecision is a generation-owned routing result ready for durable persistence.
type RouteDecision struct {
	RoutingResult RoutingResult
	RuleID        string
	RuleSnapshot  []byte
	Deliveries    []NewDelivery
}

// IngestCommand contains an authenticated, normalized webhook ready to persist.
type IngestCommand struct {
	DeliveryID     string
	CanonicalEvent CanonicalEvent
	ReceivedAt     time.Time
	RoutingResult  RoutingResult
	RuleID         string
	RuleSnapshot   []byte
	ConfigDigest   string
	HeadersJSON    []byte
	PayloadJSON    []byte
	Deliveries     []NewDelivery
}

// IngestResult identifies the durable event and whether it was already stored.
type IngestResult struct {
	EventID   string
	Duplicate bool
}
