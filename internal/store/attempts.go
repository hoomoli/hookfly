package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hoomoli/hookfly/internal/domain"
)

var ErrCurrentAttemptChanged = errors.New("current attempt changed")

// CurrentAttempt is the persisted state used to make one safe manual decision.
type CurrentAttempt struct {
	EventID            string
	DeliveryID         string
	AttemptID          string
	TargetID           string
	TargetSnapshot     []byte
	TransportStatus    domain.TransportStatus
	DeploymentStatus   domain.DeploymentStatus
	DeploymentID       string
	MonitoringDeadline time.Time
}

// ManualUpdate applies an externally reconciled correction and optionally appends a pending manual attempt.
type ManualUpdate struct {
	Expected           CurrentAttempt
	TransportStatus    domain.TransportStatus
	DeploymentStatus   domain.DeploymentStatus
	DeploymentID       string
	PollDue            *time.Time
	MonitoringDeadline *time.Time
	Correct            bool
	Create             bool
	Operation          domain.Operation
	ActorID            string
	Reason             string
	At                 time.Time
}

// CurrentAttempt returns only the delivery's current attempt, never historical attempts.
func (s *Store) CurrentAttempt(ctx context.Context, deliveryID string) (CurrentAttempt, error) {
	var current CurrentAttempt
	var deploymentID sql.NullString
	var deadline sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT d.event_id, d.id, a.id, d.target_id, d.target_snapshot, a.transport_status, a.deployment_status,
		       a.deployment_id, a.monitoring_deadline_at
		FROM deliveries d
		JOIN delivery_attempts a ON a.id = d.current_attempt_id AND a.delivery_id = d.id
		WHERE d.id = ?`, deliveryID).Scan(
		&current.EventID, &current.DeliveryID, &current.AttemptID, &current.TargetID, &current.TargetSnapshot,
		&current.TransportStatus, &current.DeploymentStatus, &deploymentID, &deadline,
	)
	if err != nil {
		return CurrentAttempt{}, fmt.Errorf("read current attempt: %w", err)
	}
	if deploymentID.Valid {
		current.DeploymentID = deploymentID.String
	}
	if deadline.Valid {
		current.MonitoringDeadline = time.UnixMilli(deadline.Int64).UTC()
	}
	return current, nil
}

// ApplyManualUpdate makes a reconciled correction and manual attempt replacement in one CAS transaction.
func (s *Store) ApplyManualUpdate(ctx context.Context, update ManualUpdate) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin manual update: %w", err)
	}
	defer tx.Rollback()

	current, err := currentAttemptTx(ctx, tx, update.Expected.DeliveryID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrCurrentAttemptChanged
	}
	if err != nil {
		return "", fmt.Errorf("read manual current attempt: %w", err)
	}
	if current.AttemptID != update.Expected.AttemptID || current.TransportStatus != update.Expected.TransportStatus || current.DeploymentStatus != update.Expected.DeploymentStatus {
		return "", ErrCurrentAttemptChanged
	}

	beforeTransport, beforeDeployment := current.TransportStatus, current.DeploymentStatus
	kind := ""
	if update.Create {
		var kindErr error
		kind, kindErr = operationAttemptKind(update.Operation)
		if kindErr != nil {
			return "", kindErr
		}
	}
	if update.Correct {
		if err := updateManualCorrection(ctx, tx, current, update); err != nil {
			return "", err
		}
		if err := insertManualAudit(ctx, tx, current, update.ActorID, "manual_reconciled", beforeTransport, beforeDeployment,
			update.TransportStatus, update.DeploymentStatus, update.Reason, update.At); err != nil {
			return "", err
		}
		current.TransportStatus, current.DeploymentStatus = update.TransportStatus, update.DeploymentStatus
	}
	if !update.Create {
		if err := tx.Commit(); err != nil {
			return "", fmt.Errorf("commit manual correction: %w", err)
		}
		s.notifyChanged()
		return current.AttemptID, nil
	}

	manualID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO delivery_attempts (id, delivery_id, kind, actor, transport_status, deployment_status, created_at)
		VALUES (?, ?, ?, ?, 'pending', 'not_started', ?)`,
		manualID, current.DeliveryID, kind, update.ActorID, update.At.UTC().UnixMilli()); err != nil {
		return "", fmt.Errorf("insert manual attempt: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE deliveries
		SET current_attempt_id = ?, transport_status = 'pending', deployment_status = 'not_started', updated_at = ?
		WHERE id = ? AND current_attempt_id = ?`,
		manualID, update.At.UTC().UnixMilli(), current.DeliveryID, current.AttemptID)
	if err != nil {
		return "", fmt.Errorf("replace current attempt: %w", err)
	}
	if err := requireCurrentAttemptRow(result, "replace current attempt"); err != nil {
		return "", err
	}
	manual := current
	manual.AttemptID = manualID
	if err := insertManualAudit(ctx, tx, manual, update.ActorID, "manual_"+string(update.Operation),
		current.TransportStatus, current.DeploymentStatus, domain.TransportPending, domain.DeploymentNotStarted, update.Reason, update.At); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit manual attempt: %w", err)
	}
	s.notifyChanged()
	return manualID, nil
}

func operationAttemptKind(operation domain.Operation) (string, error) {
	switch operation {
	case domain.OperationRetry:
		return "retry", nil
	case domain.OperationRedeploy:
		return "redeploy", nil
	default:
		return "", fmt.Errorf("unsupported manual operation %q", operation)
	}
}

func currentAttemptTx(ctx context.Context, tx *sql.Tx, deliveryID string) (CurrentAttempt, error) {
	var current CurrentAttempt
	err := tx.QueryRowContext(ctx, `
		SELECT d.event_id, d.id, a.id, d.target_id, d.target_snapshot, a.transport_status, a.deployment_status
		FROM deliveries d
		JOIN delivery_attempts a ON a.id = d.current_attempt_id AND a.delivery_id = d.id
		WHERE d.id = ?`, deliveryID).Scan(
		&current.EventID, &current.DeliveryID, &current.AttemptID, &current.TargetID, &current.TargetSnapshot,
		&current.TransportStatus, &current.DeploymentStatus,
	)
	return current, err
}

func updateManualCorrection(ctx context.Context, tx *sql.Tx, current CurrentAttempt, update ManualUpdate) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE delivery_attempts
		SET transport_status = ?, deployment_status = ?, deployment_id = ?, poll_due_at = ?, monitoring_deadline_at = ?, last_polled_at = NULL
		WHERE id = ? AND delivery_id = ?
		  AND EXISTS (
		      SELECT 1 FROM deliveries
		      WHERE id = ? AND current_attempt_id = delivery_attempts.id
		  )`,
		update.TransportStatus, update.DeploymentStatus, nullableManualString(update.DeploymentID), timeMillis(update.PollDue), timeMillis(update.MonitoringDeadline),
		current.AttemptID, current.DeliveryID, current.DeliveryID)
	if err != nil {
		return fmt.Errorf("correct manual attempt: %w", err)
	}
	if err := requireCurrentAttemptRow(result, "correct manual attempt"); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE deliveries SET transport_status = ?, deployment_status = ?, updated_at = ?
		WHERE id = ? AND current_attempt_id = ?`,
		update.TransportStatus, update.DeploymentStatus, update.At.UTC().UnixMilli(), current.DeliveryID, current.AttemptID)
	if err != nil {
		return fmt.Errorf("correct manual delivery: %w", err)
	}
	return requireCurrentAttemptRow(result, "correct manual delivery")
}

func insertManualAudit(ctx context.Context, tx *sql.Tx, current CurrentAttempt, actor, action string,
	beforeTransport domain.TransportStatus, beforeDeployment domain.DeploymentStatus,
	afterTransport domain.TransportStatus, afterDeployment domain.DeploymentStatus, reason string, at time.Time) error {
	beforeJSON, err := json.Marshal(statusSnapshot{TransportStatus: string(beforeTransport), DeploymentStatus: string(beforeDeployment)})
	if err != nil {
		return fmt.Errorf("encode manual audit before state: %w", err)
	}
	afterJSON, err := json.Marshal(struct {
		TransportStatus  string `json:"transport_status"`
		DeploymentStatus string `json:"deployment_status"`
		Reason           string `json:"reason,omitempty"`
	}{string(afterTransport), string(afterDeployment), reason})
	if err != nil {
		return fmt.Errorf("encode manual audit after state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_logs (id, event_id, delivery_id, attempt_id, actor, action, before_json, after_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), current.EventID, current.DeliveryID, current.AttemptID, actor, action, beforeJSON, afterJSON, at.UTC().UnixMilli()); err != nil {
		return fmt.Errorf("insert manual audit: %w", err)
	}
	return nil
}

func nullableManualString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func requireCurrentAttemptRow(result sql.Result, action string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect %s: %w", action, err)
	}
	if rows != 1 {
		return ErrCurrentAttemptChanged
	}
	return nil
}
