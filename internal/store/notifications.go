package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrInvalidNotificationCursor = errors.New("invalid notification cursor")

type NotificationQuery struct {
	After string
	Limit int
}

type NotificationFact struct {
	ID         string
	Cursor     string
	Category   string
	Outcome    string
	EventID    string
	DeliveryID string
	TargetID   string
	Provider   string
	SourceID   string
	Repository string
	Summary    string
	OccurredAt time.Time
}

type NotificationPage struct {
	Items        []NotificationFact
	LatestCursor string
}

type notificationCursor struct {
	OccurredAt int64  `json:"occurred_at"`
	ID         string `json:"id"`
}

func (s *Store) ListNotifications(ctx context.Context, query NotificationQuery) (NotificationPage, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	after := notificationCursor{}
	if query.After != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(query.After)
		if err != nil || json.Unmarshal(decoded, &after) != nil || after.OccurredAt < 0 || after.ID == "" {
			return NotificationPage{}, ErrInvalidNotificationCursor
		}
	}

	rows, err := s.db.QueryContext(ctx, notificationFactsSQL, after.OccurredAt, after.OccurredAt, after.ID, limit)
	if err != nil {
		return NotificationPage{}, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()
	page := NotificationPage{Items: []NotificationFact{}}
	for rows.Next() {
		var fact NotificationFact
		var deliveryID, targetID sql.NullString
		var occurredAt int64
		if err := rows.Scan(&fact.ID, &fact.Category, &fact.Outcome, &fact.EventID, &deliveryID, &targetID, &fact.Provider, &fact.SourceID, &fact.Repository, &fact.Summary, &occurredAt); err != nil {
			return NotificationPage{}, fmt.Errorf("scan notification: %w", err)
		}
		fact.DeliveryID, fact.TargetID = deliveryID.String, targetID.String
		fact.OccurredAt = time.UnixMilli(occurredAt).UTC()
		fact.Cursor = encodeNotificationCursor(occurredAt, fact.ID)
		page.Items = append(page.Items, fact)
		page.LatestCursor = fact.Cursor
	}
	if err := rows.Err(); err != nil {
		return NotificationPage{}, fmt.Errorf("iterate notifications: %w", err)
	}
	if query.After == "" {
		var occurredAt int64
		var id string
		if err := s.db.QueryRowContext(ctx, `WITH facts AS (`+notificationFactsUnionSQL+`) SELECT occurred_at,id FROM facts ORDER BY occurred_at DESC,id DESC LIMIT 1`).Scan(&occurredAt, &id); err != nil && err != sql.ErrNoRows {
			return NotificationPage{}, fmt.Errorf("read latest notification cursor: %w", err)
		} else if err == nil {
			page.LatestCursor = encodeNotificationCursor(occurredAt, id)
		}
	}
	return page, nil
}

func encodeNotificationCursor(occurredAt int64, id string) string {
	encoded, _ := json.Marshal(notificationCursor{OccurredAt: occurredAt, ID: id})
	return base64.RawURLEncoding.EncodeToString(encoded)
}

const notificationFactsSQL = `WITH facts AS (` + notificationFactsUnionSQL + `)
SELECT id, category, outcome, event_id, delivery_id, target_id, provider, source_id, repository, summary, occurred_at
FROM facts
WHERE occurred_at > ? OR (occurred_at = ? AND id > ?)
ORDER BY occurred_at, id
LIMIT ?`

const notificationFactsUnionSQL = `
    SELECT '0:event:' || e.id AS id, 'pipeline' AS category,
           CASE
             WHEN e.status IN ('created', 'preparing', 'scheduled') THEN 'initializing'
             WHEN e.status IN ('pending', 'queued', 'waiting_for_resource', 'requested') THEN 'waiting'
             WHEN e.status IN ('running', 'in_progress') THEN 'running'
             WHEN e.status = 'success' THEN 'success'
             WHEN e.status IN ('failed', 'failure') THEN 'failure'
             WHEN e.status IN ('canceled', 'cancelled') THEN 'cancelled'
           END AS outcome,
           e.id AS event_id, NULL AS delivery_id, NULL AS target_id, e.provider, e.source_id, e.repository_id AS repository,
           COALESCE(NULLIF(TRIM(e.commit_message), ''), 'Pipeline ' || CASE
             WHEN e.status IN ('created', 'preparing', 'scheduled') THEN 'initializing'
             WHEN e.status IN ('pending', 'queued', 'waiting_for_resource', 'requested') THEN 'waiting'
             WHEN e.status IN ('running', 'in_progress') THEN 'running'
             WHEN e.status = 'success' THEN 'succeeded'
             WHEN e.status IN ('failed', 'failure') THEN 'failed'
             WHEN e.status IN ('canceled', 'cancelled') THEN 'cancelled'
           END) AS summary, e.received_at AS occurred_at
    FROM events e
    WHERE e.event = 'pipeline' AND e.repository_id <> '' AND e.routing_result <> 'unmatched'
      AND e.status IN ('created', 'preparing', 'scheduled', 'pending', 'queued', 'waiting_for_resource', 'requested', 'running', 'in_progress', 'success', 'failed', 'failure', 'canceled', 'cancelled')

    UNION ALL

    SELECT '0:push:' || e.id AS id, 'push' AS category, 'received' AS outcome,
           e.id AS event_id, NULL AS delivery_id, NULL AS target_id, e.provider, e.source_id, e.repository_id AS repository,
           COALESCE(NULLIF(TRIM(e.commit_message), ''), CASE WHEN e.event = 'artifact_push' THEN 'Artifact push received' ELSE 'Push received' END) AS summary, e.received_at AS occurred_at
    FROM events e
    WHERE e.event IN ('push', 'artifact_push') AND e.repository_id <> ''

    UNION ALL

    SELECT '1:delivery:' || d.target_id || ':' || d.id AS id, 'deployment' AS category, 'pending' AS outcome,
           e.id AS event_id, d.id AS delivery_id, d.target_id, e.provider, e.source_id, e.repository_id AS repository,
           COALESCE(NULLIF(TRIM(e.commit_message), ''), 'Deployment waiting to trigger') AS summary, d.created_at AS occurred_at
    FROM deliveries d
    JOIN events e ON e.id = d.event_id

    UNION ALL

    SELECT '2:audit:' || transitions.id AS id, 'deployment' AS category, transitions.after_status AS outcome,
           transitions.event_id, transitions.delivery_id, transitions.target_id, transitions.provider, transitions.source_id, transitions.repository,
           COALESCE(NULLIF(TRIM(transitions.commit_message), ''), CASE
             WHEN transitions.after_status = 'triggering' THEN 'Deployment triggering'
             WHEN transitions.after_status = 'running' THEN 'Deployment running'
             WHEN transitions.after_status = 'success' THEN 'Deployment succeeded'
             WHEN transitions.after_status = 'failure' THEN 'Deployment failed'
             WHEN transitions.after_status = 'timeout' THEN 'Deployment timed out'
             WHEN transitions.after_status = 'cancelled' THEN 'Deployment cancelled'
             ELSE 'Deployment waiting to trigger'
           END) AS summary, transitions.created_at AS occurred_at
    FROM (
      SELECT a.id, e.id AS event_id, d.id AS delivery_id, d.target_id, e.provider, e.source_id, e.repository_id AS repository, e.commit_message, a.created_at,
             CASE
               WHEN json_extract(a.before_json, '$.deployment_status') = 'done' THEN 'success'
               WHEN json_extract(a.before_json, '$.deployment_status') = 'timeout' THEN 'timeout'
               WHEN json_extract(a.before_json, '$.deployment_status') = 'cancelled' THEN 'cancelled'
               WHEN json_extract(a.before_json, '$.deployment_status') IN ('error', 'unknown', 'unrecognized')
                 OR json_extract(a.before_json, '$.transport_status') IN ('failed', 'unknown') THEN 'failure'
               WHEN json_extract(a.before_json, '$.deployment_status') = 'running' THEN 'running'
               WHEN json_extract(a.before_json, '$.deployment_status') = 'locating'
                 OR json_extract(a.before_json, '$.transport_status') IN ('sending', 'enqueued') THEN 'triggering'
               WHEN json_extract(a.before_json, '$.transport_status') = 'pending' THEN 'pending'
             END AS before_status,
             CASE
               WHEN json_extract(a.after_json, '$.deployment_status') = 'done' THEN 'success'
               WHEN json_extract(a.after_json, '$.deployment_status') = 'timeout' THEN 'timeout'
               WHEN json_extract(a.after_json, '$.deployment_status') = 'cancelled' THEN 'cancelled'
               WHEN json_extract(a.after_json, '$.deployment_status') IN ('error', 'unknown', 'unrecognized')
                 OR json_extract(a.after_json, '$.transport_status') IN ('failed', 'unknown') THEN 'failure'
               WHEN json_extract(a.after_json, '$.deployment_status') = 'running' THEN 'running'
               WHEN json_extract(a.after_json, '$.deployment_status') = 'locating'
                 OR json_extract(a.after_json, '$.transport_status') IN ('sending', 'enqueued') THEN 'triggering'
               WHEN json_extract(a.after_json, '$.transport_status') = 'pending' THEN 'pending'
             END AS after_status
      FROM audit_logs a
      JOIN events e ON e.id = a.event_id
      JOIN deliveries d ON d.id = a.delivery_id
    ) transitions
    WHERE transitions.after_status IS NOT NULL AND transitions.after_status <> transitions.before_status`
