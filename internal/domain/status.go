package domain

type TransportStatus string

const (
	TransportPending  TransportStatus = "pending"
	TransportSending  TransportStatus = "sending"
	TransportEnqueued TransportStatus = "enqueued"
	TransportFailed   TransportStatus = "failed"
	TransportUnknown  TransportStatus = "unknown"
)

type DeploymentStatus string

const (
	DeploymentNotStarted   DeploymentStatus = "not_started"
	DeploymentLocating     DeploymentStatus = "locating"
	DeploymentRunning      DeploymentStatus = "running"
	DeploymentUnrecognized DeploymentStatus = "unrecognized"
	DeploymentDone         DeploymentStatus = "done"
	DeploymentError        DeploymentStatus = "error"
	DeploymentCancelled    DeploymentStatus = "cancelled"
	DeploymentTimeout      DeploymentStatus = "timeout"
	DeploymentUnknown      DeploymentStatus = "unknown"
)

type Operation string

const (
	OperationRetry    Operation = "retry"
	OperationRedeploy Operation = "redeploy"
)

// Active reports whether sending or deployment monitoring is in progress.
func Active(transport TransportStatus, deployment DeploymentStatus) bool {
	return transport == TransportPending || transport == TransportSending ||
		deployment == DeploymentLocating || deployment == DeploymentRunning || deployment == DeploymentUnrecognized
}

// AllowedOperations reports the safe manual operation for the current state.
func AllowedOperations(transport TransportStatus, deployment DeploymentStatus) []Operation {
	if transport == TransportFailed || transport == TransportUnknown {
		return []Operation{OperationRetry}
	}
	if deployment == DeploymentError || deployment == DeploymentCancelled ||
		deployment == DeploymentTimeout || deployment == DeploymentUnknown {
		return []Operation{OperationRedeploy}
	}
	return nil
}
