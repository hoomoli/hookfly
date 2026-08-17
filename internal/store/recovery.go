package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hoomoli/hookfly/internal/domain"
)

// ActiveBinding is the durable current-attempt identity checked before startup recovery.
type ActiveBinding struct {
	EventID          string
	DeliveryID       string
	AttemptID        string
	TargetID         string
	TargetSnapshot   []byte
	TransportStatus  domain.TransportStatus
	DeploymentStatus domain.DeploymentStatus
}

// ListActiveBindings returns only current attempts satisfying the authoritative active predicate.
func (s *Store) ListActiveBindings(ctx context.Context) ([]ActiveBinding, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.event_id, d.id, a.id, d.target_id, d.target_snapshot,
		       a.transport_status, a.deployment_status
		FROM deliveries d
		JOIN delivery_attempts a ON a.id = d.current_attempt_id AND a.delivery_id = d.id
		WHERE a.transport_status IN ('pending', 'sending')
		   OR a.deployment_status IN ('locating', 'running', 'unrecognized')
		ORDER BY d.created_at, d.id`)
	if err != nil {
		return nil, fmt.Errorf("list active bindings: %w", err)
	}
	defer rows.Close()
	bindings := make([]ActiveBinding, 0)
	for rows.Next() {
		var binding ActiveBinding
		if err := rows.Scan(
			&binding.EventID, &binding.DeliveryID, &binding.AttemptID, &binding.TargetID,
			&binding.TargetSnapshot, &binding.TransportStatus, &binding.DeploymentStatus,
		); err != nil {
			return nil, fmt.Errorf("scan active binding: %w", err)
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active bindings: %w", err)
	}
	return bindings, nil
}

// TerminateIncompatibleActive safely ends one still-current active attempt without external I/O.
func (s *Store) TerminateIncompatibleActive(ctx context.Context, binding ActiveBinding, reasonCode string, at time.Time) error {
	if reasonCode != "target_changed" && reasonCode != "target_unavailable" {
		reasonCode = "target_unavailable"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin incompatible active recovery: %w", err)
	}
	defer tx.Rollback()

	var current ActiveBinding
	err = tx.QueryRowContext(ctx, `
		SELECT d.event_id, d.id, a.id, d.target_id, d.target_snapshot,
		       a.transport_status, a.deployment_status
		FROM deliveries d
		JOIN delivery_attempts a ON a.id = d.current_attempt_id AND a.delivery_id = d.id
		WHERE d.id = ?`, binding.DeliveryID).Scan(
		&current.EventID, &current.DeliveryID, &current.AttemptID, &current.TargetID,
		&current.TargetSnapshot, &current.TransportStatus, &current.DeploymentStatus,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read incompatible active attempt: %w", err)
	}
	if current.AttemptID != binding.AttemptID || current.TransportStatus != binding.TransportStatus || current.DeploymentStatus != binding.DeploymentStatus {
		return nil
	}
	if !domain.Active(current.TransportStatus, current.DeploymentStatus) {
		return nil
	}
	afterTransport := domain.TransportUnknown
	afterDeployment := domain.DeploymentUnknown
	if current.TransportStatus == domain.TransportPending && current.DeploymentStatus == domain.DeploymentNotStarted {
		afterTransport = domain.TransportFailed
		afterDeployment = domain.DeploymentNotStarted
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE delivery_attempts
		SET transport_status = ?, deployment_status = ?, poll_due_at = NULL,
		    monitoring_deadline_at = NULL, last_polled_at = NULL
		WHERE id = ? AND delivery_id = ?`, afterTransport, afterDeployment, current.AttemptID, current.DeliveryID)
	if err != nil {
		return fmt.Errorf("terminate incompatible active attempt: %w", err)
	}
	if err := requireOneRow(result, "terminate incompatible active attempt"); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE deliveries
		SET transport_status = ?, deployment_status = ?, updated_at = ?
		WHERE id = ? AND current_attempt_id = ?`,
		afterTransport, afterDeployment, at.UTC().UnixMilli(), current.DeliveryID, current.AttemptID)
	if err != nil {
		return fmt.Errorf("terminate incompatible active delivery: %w", err)
	}
	if err := requireOneRow(result, "terminate incompatible active delivery"); err != nil {
		return err
	}
	if err := insertAudit(ctx, tx, PendingAttempt{
		EventID: current.EventID, DeliveryID: current.DeliveryID, AttemptID: current.AttemptID, TargetID: current.TargetID,
	}, "active_binding_"+reasonCode, string(current.TransportStatus), string(current.DeploymentStatus), string(afterTransport), string(afterDeployment), at); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit incompatible active recovery: %w", err)
	}
	s.notifyChanged()
	return nil
}
