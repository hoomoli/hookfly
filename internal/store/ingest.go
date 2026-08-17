package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/hoomoli/hookfly/internal/domain"
)

// Ingest persists an event and its initial pending delivery attempts atomically.
func (s *Store) Ingest(ctx context.Context, command domain.IngestCommand) (domain.IngestResult, error) {
	if command.RoutingResult == domain.RoutingDeferred {
		return domain.IngestResult{}, fmt.Errorf("direct ingest cannot persist deferred routing result")
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
		return domain.IngestResult{}, fmt.Errorf("begin ingest: %w", err)
	}
	defer tx.Rollback()

	eventID := uuid.NewString()
	result, err := tx.ExecContext(ctx, eventInsertSQL(command.RoutingResult), eventInsertValues(eventID, command, persisted)...)
	if err != nil {
		return domain.IngestResult{}, fmt.Errorf("insert event: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return domain.IngestResult{}, fmt.Errorf("inspect event insert: %w", err)
	}
	if inserted == 0 {
		if err := tx.QueryRowContext(ctx, "SELECT id FROM events WHERE provider = ? AND source_id = ? AND delivery_id = ?", persisted.Provider, persisted.Source, persisted.DeliveryID).Scan(&eventID); err != nil {
			return domain.IngestResult{}, fmt.Errorf("read duplicate event: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return domain.IngestResult{}, fmt.Errorf("commit duplicate ingest: %w", err)
		}
		return domain.IngestResult{EventID: eventID, Duplicate: true}, nil
	}

	for _, delivery := range command.Deliveries {
		deliveryID := uuid.NewString()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO deliveries (id, event_id, target_id, target_snapshot, dispatch_key, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			deliveryID, eventID, delivery.TargetID, delivery.TargetSnapshot, nullableRouteString(delivery.DispatchKey), command.ReceivedAt.UTC().UnixMilli(), command.ReceivedAt.UTC().UnixMilli(),
		); err != nil {
			return domain.IngestResult{}, fmt.Errorf("insert delivery: %w", err)
		}
		attemptID := uuid.NewString()
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO delivery_attempts (id, delivery_id, kind, transport_status, deployment_status, created_at)
			VALUES (?, ?, 'initial', 'pending', 'not_started', ?)`, attemptID, deliveryID, command.ReceivedAt.UTC().UnixMilli()); err != nil {
			return domain.IngestResult{}, fmt.Errorf("insert initial attempt: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET current_attempt_id = ? WHERE id = ?", attemptID, deliveryID); err != nil {
			return domain.IngestResult{}, fmt.Errorf("set current attempt: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.IngestResult{}, fmt.Errorf("commit ingest: %w", err)
	}
	s.notifyChanged()
	return domain.IngestResult{EventID: eventID}, nil
}

func validateDeliveries(routingResult domain.RoutingResult, deliveries []domain.NewDelivery) error {
	if len(deliveries) != 0 && routingResult != domain.RoutingDeploy {
		return fmt.Errorf("deliveries require deploy routing result")
	}
	return nil
}

type persistedEvent struct {
	domain.CanonicalEvent
	DeliveryID string
}

func persistedEventFor(command domain.IngestCommand) persistedEvent {
	return persistedEvent{CanonicalEvent: command.CanonicalEvent, DeliveryID: command.DeliveryID}
}

func validatePersistedEvent(event persistedEvent) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"provider", event.Provider},
		{"source", event.Source},
		{"event", event.Event},
		{"delivery ID", event.DeliveryID},
	} {
		if field.value == "" {
			return fmt.Errorf("canonical event %s is required", field.name)
		}
	}
	return nil
}

func eventInsertSQL(routingResult domain.RoutingResult) string {
	if routingResult == domain.RoutingDeferred {
		return `INSERT INTO events (
			id, provider, source_id, repository_id, delivery_id, received_at,
			event, ref, status, revision, commit_message, external_id, trigger, routing_result,
			rule_id, rule_snapshot, config_digest, raw_headers_json, raw_payload_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'deferred', NULL, NULL, ?, ?, ?)
		ON CONFLICT(provider, source_id, delivery_id) DO NOTHING`
	}
	return `INSERT INTO events (
		id, provider, source_id, repository_id, delivery_id, received_at,
		event, ref, status, revision, commit_message, external_id, trigger, routing_result,
		rule_id, rule_snapshot, config_digest, raw_headers_json, raw_payload_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(provider, source_id, delivery_id) DO NOTHING`
}

func eventInsertValues(eventID string, command domain.IngestCommand, persisted persistedEvent) []any {
	values := []any{
		eventID, persisted.Provider, persisted.Source, persisted.Repository, persisted.DeliveryID, command.ReceivedAt.UTC().UnixMilli(),
		persisted.Event, persisted.Ref, persisted.Status, persisted.Revision, persisted.CommitMessage, persisted.ExternalID, persisted.Trigger,
	}
	if command.RoutingResult != domain.RoutingDeferred {
		values = append(values, command.RoutingResult, nullableRouteString(command.RuleID), command.RuleSnapshot)
	}
	return append(values,
		command.ConfigDigest, safeHeadersJSON(command.HeadersJSON), command.PayloadJSON,
	)
}

func safeHeadersJSON(headers []byte) []byte {
	if len(headers) == 0 {
		return nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(headers, &values); err != nil {
		return nil
	}
	for name := range values {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "token") || strings.Contains(lower, "signature") || strings.Contains(lower, "authorization") {
			delete(values, name)
		}
	}
	safe, err := json.Marshal(values)
	if err != nil {
		return nil
	}
	return safe
}
