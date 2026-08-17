package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hoomoli/hookfly/internal/domain"
)

// DeferredEvent is the durable queue head to be matched by the current generation.
type DeferredEvent struct {
	Seq            int64
	EventID        string
	ReceivedAt     time.Time
	DeliveryID     string
	CanonicalEvent domain.CanonicalEvent
	HeadersJSON    []byte
	PayloadJSON    []byte
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// IngestDeferred persists a normalized event and its FIFO queue row atomically.
func (s *Store) IngestDeferred(ctx context.Context, command domain.IngestCommand) (domain.IngestResult, error) {
	if command.RoutingResult != domain.RoutingDeferred {
		return domain.IngestResult{}, fmt.Errorf("deferred ingest requires deferred routing result")
	}
	if err := validateDeliveries(command.RoutingResult, command.Deliveries); err != nil {
		return domain.IngestResult{}, err
	}
	persisted := persistedEventFor(command)
	if err := validatePersistedEvent(persisted); err != nil {
		return domain.IngestResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.IngestResult{}, fmt.Errorf("begin deferred ingest: %w", err)
	}
	defer tx.Rollback()

	eventID := uuid.NewString()
	result, err := tx.ExecContext(ctx, eventInsertSQL(command.RoutingResult), eventInsertValues(eventID, command, persisted)...)
	if err != nil {
		return domain.IngestResult{}, fmt.Errorf("insert deferred event: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return domain.IngestResult{}, fmt.Errorf("inspect deferred event insert: %w", err)
	}
	if inserted == 0 {
		if err := tx.QueryRowContext(ctx, "SELECT id FROM events WHERE provider = ? AND source_id = ? AND delivery_id = ?", persisted.Provider, persisted.Source, persisted.DeliveryID).Scan(&eventID); err != nil {
			return domain.IngestResult{}, fmt.Errorf("read duplicate deferred event: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return domain.IngestResult{}, fmt.Errorf("commit duplicate deferred ingest: %w", err)
		}
		return domain.IngestResult{EventID: eventID, Duplicate: true}, nil
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO routing_queue (event_id) VALUES (?)", eventID); err != nil {
		return domain.IngestResult{}, fmt.Errorf("enqueue deferred event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return domain.IngestResult{}, fmt.Errorf("commit deferred ingest: %w", err)
	}
	s.notifyChanged()
	return domain.IngestResult{EventID: eventID}, nil
}

// CountActive returns the authoritative number of current active deliveries.
func (s *Store) CountActive(ctx context.Context) (int, error) {
	return countActive(ctx, s.db)
}

func countActive(ctx context.Context, query rowQuerier) (int, error) {
	var count int
	err := query.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM deliveries d
		JOIN delivery_attempts a ON a.id = d.current_attempt_id AND a.delivery_id = d.id
		WHERE a.transport_status IN ('pending', 'sending')
		   OR a.deployment_status IN ('locating', 'running', 'unrecognized')`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active deliveries: %w", err)
	}
	return count, nil
}

// CountQueued returns the durable deferred routing backlog.
func (s *Store) CountQueued(ctx context.Context) (int, error) {
	return countQueued(ctx, s.db)
}

func countQueued(ctx context.Context, query rowQuerier) (int, error) {
	var count int
	if err := query.QueryRowContext(ctx, "SELECT COUNT(*) FROM routing_queue").Scan(&count); err != nil {
		return 0, fmt.Errorf("count queued events: %w", err)
	}
	return count, nil
}

// PeekDeferred returns the smallest durable acceptance sequence without changing it.
func (s *Store) PeekDeferred(ctx context.Context) (DeferredEvent, bool, error) {
	var deferred DeferredEvent
	var receivedAt int64
	var ref, status, revision, commitMessage, externalID, trigger sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT q.seq, e.id, e.received_at, e.provider, e.source_id, e.repository_id,
		       e.delivery_id, e.event, e.ref, e.status, e.revision, e.commit_message, e.external_id, e.trigger,
		       e.raw_headers_json, e.raw_payload_json
		FROM routing_queue q
		JOIN events e ON e.id = q.event_id
		WHERE e.routing_result = 'deferred'
		ORDER BY q.seq
		LIMIT 1`).Scan(
		&deferred.Seq, &deferred.EventID, &receivedAt, &deferred.CanonicalEvent.Provider, &deferred.CanonicalEvent.Source, &deferred.CanonicalEvent.Repository,
		&deferred.DeliveryID, &deferred.CanonicalEvent.Event, &ref, &status,
		&revision, &commitMessage, &externalID, &trigger, &deferred.HeadersJSON, &deferred.PayloadJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return DeferredEvent{}, false, nil
	}
	if err != nil {
		return DeferredEvent{}, false, fmt.Errorf("peek deferred event: %w", err)
	}
	deferred.ReceivedAt = time.UnixMilli(receivedAt).UTC()
	deferred.CanonicalEvent.Ref = ref.String
	deferred.CanonicalEvent.Status = status.String
	deferred.CanonicalEvent.Revision = revision.String
	deferred.CanonicalEvent.CommitMessage = commitMessage.String
	deferred.CanonicalEvent.ExternalID = externalID.String
	deferred.CanonicalEvent.Trigger = trigger.String
	return deferred, true, nil
}

// FinalizeDeferred applies one current-generation route to the durable queue head atomically.
func (s *Store) FinalizeDeferred(ctx context.Context, deferred DeferredEvent, decision domain.RouteDecision, configDigest string) (bool, error) {
	if decision.RoutingResult == "" || decision.RoutingResult == domain.RoutingDeferred {
		return false, fmt.Errorf("final routing result is required")
	}
	if err := validateDeliveries(decision.RoutingResult, decision.Deliveries); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin deferred finalization: %w", err)
	}
	defer tx.Rollback()

	var headSeq int64
	var eventID string
	var receivedAt int64
	err = tx.QueryRowContext(ctx, `
		SELECT q.seq, q.event_id, e.received_at
		FROM routing_queue q
		JOIN events e ON e.id = q.event_id
		WHERE e.routing_result = 'deferred'
		ORDER BY q.seq LIMIT 1`).Scan(&headSeq, &eventID, &receivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read deferred queue head: %w", err)
	}
	if headSeq != deferred.Seq || eventID != deferred.EventID {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE events
		SET routing_result = ?, rule_id = ?, rule_snapshot = ?, config_digest = ?
		WHERE id = ? AND routing_result = 'deferred'`,
		decision.RoutingResult, nullableRouteString(decision.RuleID), decision.RuleSnapshot, configDigest, eventID)
	if err != nil {
		return false, fmt.Errorf("finalize deferred event: %w", err)
	}
	if err := requireOneRow(result, "finalize deferred event"); err != nil {
		return false, err
	}
	for _, delivery := range decision.Deliveries {
		deliveryID := uuid.NewString()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO deliveries (id, event_id, target_id, target_snapshot, dispatch_key, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			deliveryID, eventID, delivery.TargetID, delivery.TargetSnapshot, nullableRouteString(delivery.DispatchKey), receivedAt, receivedAt); err != nil {
			return false, fmt.Errorf("insert deferred delivery: %w", err)
		}
		attemptID := uuid.NewString()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO delivery_attempts (id, delivery_id, kind, transport_status, deployment_status, created_at)
			VALUES (?, ?, 'initial', 'pending', 'not_started', ?)`, attemptID, deliveryID, receivedAt); err != nil {
			return false, fmt.Errorf("insert deferred initial attempt: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET current_attempt_id = ? WHERE id = ?", attemptID, deliveryID); err != nil {
			return false, fmt.Errorf("set deferred current attempt: %w", err)
		}
	}
	result, err = tx.ExecContext(ctx, "DELETE FROM routing_queue WHERE seq = ? AND event_id = ?", headSeq, eventID)
	if err != nil {
		return false, fmt.Errorf("remove finalized queue row: %w", err)
	}
	if err := requireOneRow(result, "remove finalized queue row"); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit deferred finalization: %w", err)
	}
	s.notifyChanged()
	return true, nil
}

func nullableRouteString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
