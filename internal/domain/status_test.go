package domain_test

import (
	"reflect"
	"testing"

	"github.com/hoomoli/hookfly/internal/domain"
)

func TestAllowedOperations(t *testing.T) {
	tests := []struct {
		name       string
		transport  domain.TransportStatus
		deployment domain.DeploymentStatus
		want       []domain.Operation
	}{
		{"definitive transport failure", domain.TransportFailed, domain.DeploymentNotStarted, []domain.Operation{domain.OperationRetry}},
		{"unconfirmed transport", domain.TransportUnknown, domain.DeploymentUnknown, []domain.Operation{domain.OperationRetry}},
		{"failed deployment", domain.TransportEnqueued, domain.DeploymentError, []domain.Operation{domain.OperationRedeploy}},
		{"successful deployment", domain.TransportEnqueued, domain.DeploymentDone, nil},
		{"active deployment", domain.TransportEnqueued, domain.DeploymentRunning, nil},
		{"unrecognized active deployment", domain.TransportEnqueued, domain.DeploymentUnrecognized, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.AllowedOperations(tt.transport, tt.deployment); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestActive(t *testing.T) {
	tests := []struct {
		name       string
		transport  domain.TransportStatus
		deployment domain.DeploymentStatus
		want       bool
	}{
		{"pending transport", domain.TransportPending, domain.DeploymentNotStarted, true},
		{"sending transport", domain.TransportSending, domain.DeploymentNotStarted, true},
		{"locating deployment", domain.TransportEnqueued, domain.DeploymentLocating, true},
		{"running deployment", domain.TransportEnqueued, domain.DeploymentRunning, true},
		{"unrecognized deployment", domain.TransportEnqueued, domain.DeploymentUnrecognized, true},
		{"terminal statuses", domain.TransportEnqueued, domain.DeploymentDone, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.Active(tt.transport, tt.deployment); got != tt.want {
				t.Fatalf("Active() = %v, want %v", got, tt.want)
			}
		})
	}
}
