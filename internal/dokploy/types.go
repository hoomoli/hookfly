package dokploy

import (
	"fmt"
	"time"
)

// DeployRequest is the version-tolerant Compose enqueue payload.
type DeployRequest struct {
	ComposeID   string `json:"composeId"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// DeployResponse contains only bounded diagnostic response data.
type DeployResponse struct {
	StatusCode int
	Body       []byte
	Truncated  bool
}

// Deployment is the stable subset returned by deployment.allByCompose.
type Deployment struct {
	DeploymentID string    `json:"deploymentId"`
	ComposeID    string    `json:"composeId"`
	Description  *string   `json:"description"`
	Status       *string   `json:"status"`
	CreatedAt    time.Time `json:"createdAt"`
}

// DeploymentLookup describes cursor-based correlation without guessing.
type DeploymentLookup string

const (
	DeploymentNotFound      DeploymentLookup = "not_found"
	DeploymentFound         DeploymentLookup = "found"
	DeploymentAmbiguous     DeploymentLookup = "ambiguous"
	DeploymentCursorMissing DeploymentLookup = "cursor_missing"
)

// ComposeResource is the safe inventory subset discovered through project.all.
type ComposeResource struct {
	ProjectName     string
	EnvironmentName string
	Name            string
	AppName         string
	ResourceID      string
	Status          string
}

// ComposeContainer is the safe live-runtime subset for one Compose container.
type ComposeContainer struct {
	Name         string
	State        string
	Health       string
	RestartCount int
}

type TransportErrorKind string

const (
	TransportDefinitive TransportErrorKind = "definitive"
	TransportUncertain  TransportErrorKind = "uncertain"
)

// TransportError distinguishes safe-to-fail sends from sends that may have reached Dokploy.
type TransportError struct {
	Kind TransportErrorKind
	Err  error
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("Dokploy enqueue %s: %v", e.Kind, e.Err)
}

func (e *TransportError) Unwrap() error {
	return e.Err
}
