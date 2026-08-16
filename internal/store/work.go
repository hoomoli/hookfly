package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var (
	ErrAttemptNotSending = errors.New("attempt is not the current sending attempt")
	ErrPollNotClaimed    = errors.New("attempt poll is not currently claimed")
)

const maxPollBatch = 32

// PendingAttempt identifies durable current work without exposing persisted target snapshots.
type PendingAttempt struct {
	EventID        string
	DeliveryID     string
	AttemptID      string
	TargetID       string
	TargetSnapshot []byte
	ReceivedAt     time.Time
}

// PollWork is one atomically claimed current attempt that is due for reconciliation.
type PollWork struct {
	EventID            string
	DeliveryID         string
	AttemptID          string
	TargetID           string
	TargetSnapshot     []byte
	TransportStatus    string
	DeploymentStatus   string
	DeploymentID       string
	DeploymentCursor   *string
	MonitoringDeadline time.Time
	ClaimedAt          time.Time
}

// PollUpdate is the complete persisted outcome of one deployment-list query.
type PollUpdate struct {
	TransportStatus  string
	DeploymentStatus string
	DeploymentID     string
	NextPollAt       *time.Time
}

// PollSchedule supplies restart defaults when an interrupted attempt has no due time or deadline.
type PollSchedule struct {
	Interval time.Duration
	Timeout  time.Duration
}

// StatusChange describes one committed status transition for structured logging.
type StatusChange struct {
	AttemptID        string
	TargetID         string
	BeforeTransport  string
	BeforeDeployment string
	AfterTransport   string
	AfterDeployment  string
}

// ClaimPendingAttempt conditionally moves the oldest claimable current attempt to sending.
func (s *Store) ClaimPendingAttempt(ctx context.Context, now time.Time) (PendingAttempt, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PendingAttempt{}, false, fmt.Errorf("begin attempt claim: %w", err)
	}
	defer tx.Rollback()

	var work PendingAttempt
	var receivedAt int64
	err = tx.QueryRowContext(ctx, `
		SELECT e.id, d.id, a.id, d.target_id, d.target_snapshot, e.received_at
		FROM deliveries d
		JOIN events e ON e.id = d.event_id
		JOIN delivery_attempts a ON a.id = d.current_attempt_id AND a.delivery_id = d.id
		WHERE a.kind IN ('initial', 'retry', 'redeploy')
		  AND a.transport_status = 'pending'
		  AND d.transport_status = 'pending'
		  AND NOT EXISTS (
		      SELECT 1
		      FROM deliveries active_delivery
		      JOIN delivery_attempts active_attempt
		        ON active_attempt.id = active_delivery.current_attempt_id
		       AND active_attempt.delivery_id = active_delivery.id
		      WHERE (
		            (active_delivery.dispatch_key IS NOT NULL AND d.dispatch_key IS NOT NULL
		             AND active_delivery.dispatch_key = d.dispatch_key) OR
		            ((active_delivery.dispatch_key IS NULL OR d.dispatch_key IS NULL)
		             AND active_delivery.target_id = d.target_id)
		        )
		        AND active_delivery.id <> d.id
		        AND active_attempt.deployment_id IS NULL
		        AND (
		            active_attempt.transport_status = 'sending' OR
		            active_attempt.deployment_status IN ('locating', 'running', 'unrecognized')
		        )
		  )
		ORDER BY e.received_at, d.created_at, d.id
		LIMIT 1`).Scan(&work.EventID, &work.DeliveryID, &work.AttemptID, &work.TargetID, &work.TargetSnapshot, &receivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PendingAttempt{}, false, nil
	}
	if err != nil {
		return PendingAttempt{}, false, fmt.Errorf("select pending attempt: %w", err)
	}
	work.ReceivedAt = time.UnixMilli(receivedAt).UTC()

	result, err := tx.ExecContext(ctx, `
		UPDATE delivery_attempts
		SET transport_status = 'sending'
		WHERE id = ? AND delivery_id = ? AND kind IN ('initial', 'retry', 'redeploy') AND transport_status = 'pending'
		  AND EXISTS (
		      SELECT 1 FROM deliveries
		      WHERE id = ? AND current_attempt_id = delivery_attempts.id AND transport_status = 'pending'
		  )`, work.AttemptID, work.DeliveryID, work.DeliveryID)
	if err != nil {
		return PendingAttempt{}, false, fmt.Errorf("claim attempt: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return PendingAttempt{}, false, fmt.Errorf("inspect attempt claim: %w", err)
	}
	if changed != 1 {
		return PendingAttempt{}, false, nil
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE deliveries
		SET transport_status = 'sending', updated_at = ?
		WHERE id = ? AND current_attempt_id = ? AND transport_status = 'pending'`,
		now.UTC().UnixMilli(), work.DeliveryID, work.AttemptID)
	if err != nil {
		return PendingAttempt{}, false, fmt.Errorf("claim delivery: %w", err)
	}
	if err := requireOneRow(result, "claim delivery"); err != nil {
		return PendingAttempt{}, false, err
	}
	if err := insertAudit(ctx, tx, work, "transport_sending", "pending", "not_started", "sending", "not_started", now); err != nil {
		return PendingAttempt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return PendingAttempt{}, false, fmt.Errorf("commit attempt claim: %w", err)
	}
	return work, true, nil
}

func (s *Store) MarkEnqueued(ctx context.Context, attemptID string, requestJSON, responseJSON []byte, at, pollDue, deadline time.Time) error {
	return s.transitionAttempt(ctx, attemptID, requestJSON, responseJSON, at, "enqueued", "locating", "transport_enqueued", &at, &pollDue, &deadline)
}

func (s *Store) MarkTransportFailed(ctx context.Context, attemptID string, requestJSON, responseJSON []byte, at time.Time) error {
	return s.transitionAttempt(ctx, attemptID, requestJSON, responseJSON, at, "failed", "not_started", "transport_failed", nil, nil, nil)
}

func (s *Store) MarkTransportUnknown(ctx context.Context, attemptID string, requestJSON, responseJSON []byte, at, pollDue, deadline time.Time) error {
	return s.transitionAttempt(ctx, attemptID, requestJSON, responseJSON, at, "unknown", "locating", "transport_unknown", nil, &pollDue, &deadline)
}

// RecordDeploymentRequest durably captures the request immediately before the remote POST.
func (s *Store) RecordDeploymentRequest(ctx context.Context, attemptID string, requestJSON []byte, at time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE delivery_attempts
		SET request_json = ?, request_at = ?
		WHERE id = ? AND transport_status = 'sending' AND request_json IS NULL AND request_at IS NULL
		  AND EXISTS (
		      SELECT 1 FROM deliveries
		      WHERE current_attempt_id = delivery_attempts.id
		  )`, requestJSON, at.UTC().UnixMilli(), attemptID)
	if err != nil {
		return fmt.Errorf("record deployment request: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect recorded deployment request: %w", err)
	}
	if changed != 1 {
		return ErrAttemptNotSending
	}
	return nil
}

// SetDeploymentCursor persists the exclusive preflight boundary before the remote POST.
func (s *Store) SetDeploymentCursor(ctx context.Context, attemptID, cursor string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE delivery_attempts
		SET deployment_cursor = ?
		WHERE id = ? AND transport_status = 'sending' AND deployment_cursor IS NULL
		  AND EXISTS (
		      SELECT 1 FROM deliveries
		      WHERE current_attempt_id = delivery_attempts.id
		  )`, cursor, attemptID)
	if err != nil {
		return fmt.Errorf("set deployment cursor: %w", err)
	}
	if err := requireOneRow(result, "set deployment cursor"); err != nil {
		return err
	}
	return nil
}

// ListDuePolls atomically claims a bounded batch of current active deployment attempts.
func (s *Store) ListDuePolls(ctx context.Context, now time.Time) ([]PollWork, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin poll claim: %w", err)
	}
	defer tx.Rollback()

	nowMillis := now.UTC().UnixMilli()
	rows, err := tx.QueryContext(ctx, `
		SELECT e.id, d.id, a.id, d.target_id, d.target_snapshot, a.transport_status, a.deployment_status,
		       a.deployment_id, a.deployment_cursor, a.monitoring_deadline_at
		FROM delivery_attempts a
		JOIN deliveries d ON d.id = a.delivery_id AND d.current_attempt_id = a.id
		JOIN events e ON e.id = d.event_id
		WHERE a.deployment_status IN ('locating', 'running', 'unrecognized')
		  AND a.poll_due_at IS NOT NULL
		  AND a.monitoring_deadline_at IS NOT NULL
		  AND (a.poll_due_at <= ? OR a.monitoring_deadline_at <= ?)
		  AND (
		      a.last_polled_at IS NULL OR
		      a.last_polled_at < CASE
		          WHEN a.monitoring_deadline_at < a.poll_due_at THEN a.monitoring_deadline_at
		          ELSE a.poll_due_at
		      END
		  )
		ORDER BY CASE
		             WHEN a.monitoring_deadline_at < a.poll_due_at THEN a.monitoring_deadline_at
		             ELSE a.poll_due_at
		         END,
		         e.received_at, d.created_at, d.id
		LIMIT ?`, nowMillis, nowMillis, maxPollBatch)
	if err != nil {
		return nil, fmt.Errorf("select due polls: %w", err)
	}
	var candidates []PollWork
	for rows.Next() {
		var work PollWork
		var deploymentID sql.NullString
		var deploymentCursor sql.NullString
		var deadline int64
		if err := rows.Scan(
			&work.EventID, &work.DeliveryID, &work.AttemptID, &work.TargetID, &work.TargetSnapshot,
			&work.TransportStatus, &work.DeploymentStatus, &deploymentID, &deploymentCursor, &deadline,
		); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan due poll: %w", err)
		}
		if deploymentID.Valid {
			work.DeploymentID = deploymentID.String
		}
		if deploymentCursor.Valid {
			cursor := deploymentCursor.String
			work.DeploymentCursor = &cursor
		}
		work.MonitoringDeadline = time.UnixMilli(deadline).UTC()
		work.ClaimedAt = now.UTC()
		candidates = append(candidates, work)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate due polls: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close due polls: %w", err)
	}

	claimed := make([]PollWork, 0, len(candidates))
	for _, work := range candidates {
		result, err := tx.ExecContext(ctx, `
			UPDATE delivery_attempts
			SET last_polled_at = ?
			WHERE id = ? AND delivery_id = ?
			  AND deployment_status IN ('locating', 'running', 'unrecognized')
			  AND poll_due_at IS NOT NULL
			  AND monitoring_deadline_at IS NOT NULL
			  AND (poll_due_at <= ? OR monitoring_deadline_at <= ?)
			  AND (
			      last_polled_at IS NULL OR
			      last_polled_at < CASE
			          WHEN monitoring_deadline_at < poll_due_at THEN monitoring_deadline_at
			          ELSE poll_due_at
			      END
			  )
			  AND EXISTS (
			      SELECT 1 FROM deliveries
			      WHERE id = ? AND current_attempt_id = delivery_attempts.id
			  )`, nowMillis, work.AttemptID, work.DeliveryID, nowMillis, nowMillis, work.DeliveryID)
		if err != nil {
			return nil, fmt.Errorf("claim due poll: %w", err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("inspect due poll claim: %w", err)
		}
		if rowsAffected == 1 {
			claimed = append(claimed, work)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit poll claims: %w", err)
	}
	return claimed, nil
}

// CompletePoll persists scheduling/evidence metadata and appends audit only for a real status transition.
func (s *Store) CompletePoll(ctx context.Context, work PollWork, update PollUpdate, at time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin poll completion: %w", err)
	}
	defer tx.Rollback()

	var current PendingAttempt
	var beforeTransport, beforeDeployment string
	var existingDeploymentID sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT d.event_id, d.id, a.id, d.target_id, a.transport_status, a.deployment_status, a.deployment_id
		FROM delivery_attempts a
		JOIN deliveries d ON d.id = a.delivery_id AND d.current_attempt_id = a.id
		WHERE a.id = ? AND a.delivery_id = ? AND a.last_polled_at = ?`,
		work.AttemptID, work.DeliveryID, work.ClaimedAt.UTC().UnixMilli()).Scan(
		&current.EventID, &current.DeliveryID, &current.AttemptID, &current.TargetID,
		&beforeTransport, &beforeDeployment, &existingDeploymentID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrPollNotClaimed
	}
	if err != nil {
		return false, fmt.Errorf("read claimed poll: %w", err)
	}

	deploymentID := existingDeploymentID
	if update.DeploymentID != "" {
		deploymentID = sql.NullString{String: update.DeploymentID, Valid: true}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE delivery_attempts
		SET transport_status = ?, deployment_status = ?, deployment_id = ?, poll_due_at = ?
		WHERE id = ? AND delivery_id = ? AND last_polled_at = ?`,
		update.TransportStatus, update.DeploymentStatus, nullableSQLString(deploymentID), timeMillis(update.NextPollAt),
		work.AttemptID, work.DeliveryID, work.ClaimedAt.UTC().UnixMilli())
	if err != nil {
		return false, fmt.Errorf("update claimed poll: %w", err)
	}
	if err := requirePollRow(result, "update claimed poll"); err != nil {
		return false, err
	}

	changed := beforeTransport != update.TransportStatus || beforeDeployment != update.DeploymentStatus
	if changed {
		result, err = tx.ExecContext(ctx, `
			UPDATE deliveries
			SET transport_status = ?, deployment_status = ?, updated_at = ?
			WHERE id = ? AND current_attempt_id = ?`,
			update.TransportStatus, update.DeploymentStatus, at.UTC().UnixMilli(), work.DeliveryID, work.AttemptID)
		if err != nil {
			return false, fmt.Errorf("update delivery poll status: %w", err)
		}
		if err := requirePollRow(result, "update delivery poll status"); err != nil {
			return false, err
		}
		if err := insertAudit(ctx, tx, current, "deployment_"+update.DeploymentStatus,
			beforeTransport, beforeDeployment, update.TransportStatus, update.DeploymentStatus, at); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit poll completion: %w", err)
	}
	return changed, nil
}

// RecoverInterrupted converts current sending attempts to reconciliation and releases active poll claims.
func (s *Store) RecoverInterrupted(
	ctx context.Context,
	now time.Time,
	defaultSchedule PollSchedule,
	targetSchedules map[string]PollSchedule,
) ([]StatusChange, error) {
	defaultSchedule = normalizedPollSchedule(defaultSchedule)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin interrupted recovery: %w", err)
	}
	defer tx.Rollback()

	type interrupted struct {
		work                        PendingAttempt
		transport, deployment       string
		pollDue, monitoringDeadline sql.NullInt64
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT d.event_id, d.id, a.id, d.target_id, a.transport_status, a.deployment_status,
		       a.poll_due_at, a.monitoring_deadline_at
		FROM delivery_attempts a
		JOIN deliveries d ON d.id = a.delivery_id AND d.current_attempt_id = a.id
		WHERE a.transport_status = 'sending' OR a.deployment_status IN ('locating', 'running', 'unrecognized')
		ORDER BY d.created_at, d.id`)
	if err != nil {
		return nil, fmt.Errorf("select interrupted attempts: %w", err)
	}
	var attempts []interrupted
	for rows.Next() {
		var attempt interrupted
		if err := rows.Scan(
			&attempt.work.EventID, &attempt.work.DeliveryID, &attempt.work.AttemptID, &attempt.work.TargetID,
			&attempt.transport, &attempt.deployment, &attempt.pollDue, &attempt.monitoringDeadline,
		); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan interrupted attempt: %w", err)
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate interrupted attempts: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close interrupted attempts: %w", err)
	}

	changes := make([]StatusChange, 0)
	for _, attempt := range attempts {
		schedule := defaultSchedule
		if configured, found := targetSchedules[attempt.work.TargetID]; found {
			schedule = normalizedPollSchedule(configured)
		}
		pollDue := attempt.pollDue
		if !pollDue.Valid {
			pollDue = sql.NullInt64{Int64: now.Add(schedule.Interval).UTC().UnixMilli(), Valid: true}
		}
		deadline := attempt.monitoringDeadline
		if !deadline.Valid {
			deadline = sql.NullInt64{Int64: now.Add(schedule.Timeout).UTC().UnixMilli(), Valid: true}
		}
		afterTransport, afterDeployment := attempt.transport, attempt.deployment
		if attempt.transport == "sending" {
			afterTransport, afterDeployment = "unknown", "locating"
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE delivery_attempts
			SET transport_status = ?, deployment_status = ?, poll_due_at = ?, monitoring_deadline_at = ?, last_polled_at = NULL
			WHERE id = ? AND delivery_id = ?`,
			afterTransport, afterDeployment, pollDue.Int64, deadline.Int64,
			attempt.work.AttemptID, attempt.work.DeliveryID)
		if err != nil {
			return nil, fmt.Errorf("recover interrupted attempt: %w", err)
		}
		if err := requirePollRow(result, "recover interrupted attempt"); err != nil {
			return nil, err
		}
		if attempt.transport != afterTransport || attempt.deployment != afterDeployment {
			result, err = tx.ExecContext(ctx, `
				UPDATE deliveries
				SET transport_status = ?, deployment_status = ?, updated_at = ?
				WHERE id = ? AND current_attempt_id = ?`,
				afterTransport, afterDeployment, now.UTC().UnixMilli(), attempt.work.DeliveryID, attempt.work.AttemptID)
			if err != nil {
				return nil, fmt.Errorf("recover interrupted delivery: %w", err)
			}
			if err := requirePollRow(result, "recover interrupted delivery"); err != nil {
				return nil, err
			}
			if err := insertAudit(ctx, tx, attempt.work, "transport_recovered",
				attempt.transport, attempt.deployment, afterTransport, afterDeployment, now); err != nil {
				return nil, err
			}
			changes = append(changes, StatusChange{
				AttemptID: attempt.work.AttemptID, TargetID: attempt.work.TargetID,
				BeforeTransport: attempt.transport, BeforeDeployment: attempt.deployment,
				AfterTransport: afterTransport, AfterDeployment: afterDeployment,
			})
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit interrupted recovery: %w", err)
	}
	return changes, nil
}

func (s *Store) transitionAttempt(
	ctx context.Context,
	attemptID string,
	requestJSON, responseJSON []byte,
	at time.Time,
	transportStatus, deploymentStatus, action string,
	enqueuedAt, pollDue, deadline *time.Time,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin attempt transition: %w", err)
	}
	defer tx.Rollback()

	var work PendingAttempt
	var beforeTransport, beforeDeployment string
	err = tx.QueryRowContext(ctx, `
		SELECT d.event_id, d.id, a.id, d.target_id, a.transport_status, a.deployment_status
		FROM delivery_attempts a
		JOIN deliveries d ON d.id = a.delivery_id AND d.current_attempt_id = a.id
		WHERE a.id = ?`, attemptID).Scan(
		&work.EventID, &work.DeliveryID, &work.AttemptID, &work.TargetID, &beforeTransport, &beforeDeployment,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAttemptNotSending
	}
	if err != nil {
		return fmt.Errorf("read current attempt: %w", err)
	}
	if beforeTransport != "sending" {
		return ErrAttemptNotSending
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE delivery_attempts
		SET transport_status = ?, deployment_status = ?, request_json = ?, response_json = ?,
		    response_at = ?, enqueued_at = ?, poll_due_at = ?, monitoring_deadline_at = ?
		WHERE id = ? AND delivery_id = ? AND transport_status = 'sending'`,
		transportStatus, deploymentStatus, requestJSON, responseJSON, at.UTC().UnixMilli(),
		timeMillis(enqueuedAt), timeMillis(pollDue), timeMillis(deadline), work.AttemptID, work.DeliveryID)
	if err != nil {
		return fmt.Errorf("update attempt outcome: %w", err)
	}
	if err := requireOneRow(result, "update attempt outcome"); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE deliveries
		SET transport_status = ?, deployment_status = ?, updated_at = ?
		WHERE id = ? AND current_attempt_id = ? AND transport_status = 'sending'`,
		transportStatus, deploymentStatus, at.UTC().UnixMilli(), work.DeliveryID, work.AttemptID)
	if err != nil {
		return fmt.Errorf("update delivery outcome: %w", err)
	}
	if err := requireOneRow(result, "update delivery outcome"); err != nil {
		return err
	}
	if err := insertAudit(ctx, tx, work, action, beforeTransport, beforeDeployment, transportStatus, deploymentStatus, at); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit attempt transition: %w", err)
	}
	return nil
}

func requireOneRow(result sql.Result, action string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect %s: %w", action, err)
	}
	if rows != 1 {
		return ErrAttemptNotSending
	}
	return nil
}

func requirePollRow(result sql.Result, action string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect %s: %w", action, err)
	}
	if rows != 1 {
		return ErrPollNotClaimed
	}
	return nil
}

func nullableSQLString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func normalizedPollSchedule(schedule PollSchedule) PollSchedule {
	if schedule.Interval <= 0 {
		schedule.Interval = time.Second
	}
	if schedule.Timeout <= 0 {
		schedule.Timeout = 30 * time.Minute
	}
	return schedule
}

func insertAudit(
	ctx context.Context,
	tx *sql.Tx,
	work PendingAttempt,
	action, beforeTransport, beforeDeployment, afterTransport, afterDeployment string,
	at time.Time,
) error {
	beforeJSON, err := json.Marshal(statusSnapshot{TransportStatus: beforeTransport, DeploymentStatus: beforeDeployment})
	if err != nil {
		return fmt.Errorf("encode audit before state: %w", err)
	}
	afterJSON, err := json.Marshal(statusSnapshot{TransportStatus: afterTransport, DeploymentStatus: afterDeployment})
	if err != nil {
		return fmt.Errorf("encode audit after state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_logs (
			id, event_id, delivery_id, attempt_id, actor, action, before_json, after_json, created_at
		) VALUES (?, ?, ?, ?, 'system', ?, ?, ?, ?)`,
		uuid.NewString(), work.EventID, work.DeliveryID, work.AttemptID, action, beforeJSON, afterJSON, at.UTC().UnixMilli()); err != nil {
		return fmt.Errorf("insert attempt audit: %w", err)
	}
	return nil
}

func timeMillis(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().UnixMilli()
}

type statusSnapshot struct {
	TransportStatus  string `json:"transport_status"`
	DeploymentStatus string `json:"deployment_status"`
}
