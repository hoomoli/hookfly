package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hoomoli/hookfly/internal/domain"
	"github.com/hoomoli/hookfly/internal/redact"
)

const (
	readSnapshotLimit   = 64 * 1024
	pipelineWaitTimeout = 15 * time.Minute
)

// RepositoryKey identifies one repository within a provider source.
type RepositoryKey struct {
	SourceID     string `json:"source_id"`
	RepositoryID string `json:"repository_id"`
}

// ConfiguredRepository is the safe configured repository projection.
type ConfiguredRepository struct {
	Provider string `json:"provider"`
	SourceID string `json:"source_id"`
	ID       string `json:"id"`
	Name     string `json:"name"`
}

type EventQuery struct {
	Source           *string
	Repository       *string
	Target           *string
	Unmatched        *bool
	Ref              *string
	Rule             *string
	ID               string
	TransportStatus  *string
	DeploymentStatus *string
	Page             int
	PageSize         int
}

type DeliverySummary struct {
	Kind             string  `json:"kind"`
	Count            int     `json:"count"`
	TransportStatus  *string `json:"transport_status"`
	DeploymentStatus *string `json:"deployment_status"`
}

type DeliveryListItem struct {
	ID                string             `json:"id"`
	TargetID          string             `json:"target_id"`
	CurrentAttemptID  string             `json:"current_attempt_id"`
	TransportStatus   string             `json:"transport_status"`
	DeploymentStatus  string             `json:"deployment_status"`
	Active            bool               `json:"active"`
	Attention         bool               `json:"attention"`
	AllowedOperations []domain.Operation `json:"allowed_operations"`
}

type EventSummary struct {
	ID              string             `json:"id"`
	ReceivedAt      time.Time          `json:"received_at"`
	EventType       string             `json:"event_type"`
	Provider        string             `json:"provider"`
	SourceID        string             `json:"source_id"`
	Repository      string             `json:"repository"`
	Ref             *string            `json:"ref"`
	Status          *string            `json:"status"`
	Revision        *string            `json:"revision"`
	CommitMessage   *string            `json:"commit_message"`
	ExternalID      *string            `json:"external_id"`
	Trigger         *string            `json:"trigger"`
	RoutingResult   string             `json:"routing_result"`
	RuleID          *string            `json:"rule_id"`
	DeliverySummary DeliverySummary    `json:"delivery_summary"`
	Deliveries      []DeliveryListItem `json:"deliveries"`
	Active          bool               `json:"active"`
	Attention       bool               `json:"attention"`
}

type EventPage struct {
	Items       []EventSummary
	Page        int
	PageSize    int
	Total       int64
	HasPrevious bool
	HasNext     bool
	Active      bool
}

type AttemptDetail struct {
	ID                   string
	Kind                 string
	Actor                *string
	Current              bool
	TransportStatus      string
	DeploymentStatus     string
	DeploymentID         *string
	RequestSnapshot      any
	ResponseSnapshot     any
	DeployCommand        *string
	RequestAt            *time.Time
	ResponseAt           *time.Time
	EnqueuedAt           *time.Time
	MonitoringDeadlineAt *time.Time
	CreatedAt            time.Time
}

type DeliveryDetail struct {
	ID                string
	TargetID          string
	TargetSnapshot    []byte `json:"-"`
	CurrentAttemptID  string
	TransportStatus   string
	DeploymentStatus  string
	Active            bool
	Attention         bool
	AllowedOperations []domain.Operation
	CreatedAt         time.Time
	UpdatedAt         time.Time
	Attempts          []AttemptDetail
}

type ActivityDetail struct {
	ID         string
	DeliveryID *string
	AttemptID  *string
	Actor      *string
	Action     string
	Before     any
	After      any
	CreatedAt  time.Time
}

type EventDetail struct {
	EventSummary
	Headers      any
	Payload      any
	RuleSnapshot any
	SourceStates []SourceState
	Deliveries   []DeliveryDetail
	Activities   []ActivityDetail
}

type SourceState struct {
	EventID    string
	Status     string
	ReceivedAt time.Time
}

// ListRepositories returns the current configured projection without consulting event payloads.
func (s *Store) ListRepositories(_ context.Context, configured []ConfiguredRepository) ([]ConfiguredRepository, error) {
	return append([]ConfiguredRepository(nil), configured...), nil
}

// Ready performs only a local SQLite readiness query.
func (s *Store) Ready(ctx context.Context) error {
	var one int
	return s.db.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

// ListEvents returns one stable page and aggregates only current attempts.
func (s *Store) ListEvents(ctx context.Context, query EventQuery) (EventPage, error) {
	if (query.Source == nil) != (query.Repository == nil) {
		return EventPage{}, errors.New("source and repository filters must be provided together")
	}
	where, args := eventWhere(query)
	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events e "+where, args...).Scan(&total); err != nil {
		return EventPage{}, fmt.Errorf("count events: %w", err)
	}
	page := EventPage{Items: []EventSummary{}, Page: query.Page, PageSize: query.PageSize, Total: total, HasPrevious: query.Page > 1}
	if total == 0 {
		return page, nil
	}
	pageIndex, pageSize := int64(query.Page-1), int64(query.PageSize)
	if pageIndex > (total-1)/pageSize {
		return page, nil
	}
	offset := pageIndex * pageSize
	page.HasNext = pageSize < total-offset
	rows, err := s.db.QueryContext(ctx, `SELECT e.id,e.received_at,e.event,e.provider,e.source_id,e.repository_id,e.ref,e.status,e.external_id,e.trigger,e.revision,e.commit_message,e.routing_result,e.rule_id
		FROM events e `+where+` ORDER BY e.received_at DESC,e.id DESC LIMIT ? OFFSET ?`, append(args, query.PageSize, offset)...)
	if err != nil {
		return EventPage{}, fmt.Errorf("list events: %w", err)
	}
	items := []EventSummary{}
	for rows.Next() {
		item, err := scanEventSummary(rows)
		if err != nil {
			_ = rows.Close()
			return EventPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return EventPage{}, fmt.Errorf("iterate events: %w", err)
	}
	if err := rows.Close(); err != nil {
		return EventPage{}, fmt.Errorf("close events: %w", err)
	}
	for index := range items {
		if err := s.fillEventState(ctx, &items[index]); err != nil {
			return EventPage{}, err
		}
		page.Active = page.Active || items[index].Active
		page.Items = append(page.Items, items[index])
	}
	return page, nil
}

func eventWhere(query EventQuery) (string, []any) {
	clauses := []string{`WHERE (
		e.event <> 'pipeline' OR e.external_id IS NULL OR e.external_id = '' OR NOT EXISTS (
			SELECT 1 FROM events newer
			WHERE newer.provider = e.provider
			  AND newer.source_id = e.source_id
			  AND newer.repository_id = e.repository_id
			  AND newer.event = e.event
			  AND newer.external_id = e.external_id
			  AND (newer.received_at > e.received_at OR (newer.received_at = e.received_at AND newer.id > e.id))
		)
	)`, `(
		e.event <> 'push' OR e.revision IS NULL OR e.revision = '' OR EXISTS (
			SELECT 1 FROM deliveries push_delivery WHERE push_delivery.event_id = e.id
		) OR NOT EXISTS (
			SELECT 1 FROM events pipeline
			WHERE pipeline.provider = e.provider
			  AND pipeline.source_id = e.source_id
			  AND pipeline.repository_id = e.repository_id
			  AND pipeline.event = 'pipeline'
			  AND pipeline.trigger = 'push'
			  AND pipeline.revision IS NOT NULL
			  AND pipeline.revision <> ''
			  AND pipeline.revision = e.revision
			  AND pipeline.ref IS e.ref
			  AND pipeline.external_id IS NOT NULL
			  AND pipeline.external_id <> ''
			  AND pipeline.received_at >= e.received_at
			  AND NOT EXISTS (
				SELECT 1 FROM events earlier_pipeline
				WHERE earlier_pipeline.provider = pipeline.provider
				  AND earlier_pipeline.source_id = pipeline.source_id
				  AND earlier_pipeline.repository_id = pipeline.repository_id
				  AND earlier_pipeline.event = 'pipeline'
				  AND earlier_pipeline.external_id = pipeline.external_id
				  AND earlier_pipeline.received_at < e.received_at
			  )
		)
	)`}
	args := []any{}
	if query.Source != nil && query.Repository != nil {
		clauses, args = append(clauses, "e.source_id = ?", "e.repository_id = ?"), append(args, *query.Source, *query.Repository)
	}
	if query.Unmatched != nil {
		operator := "<>"
		if *query.Unmatched {
			operator = "="
		}
		clauses = append(clauses, "e.routing_result "+operator+" 'unmatched'")
	}
	if query.Target != nil {
		clauses, args = append(clauses, "EXISTS (SELECT 1 FROM deliveries target_delivery WHERE target_delivery.event_id = e.id AND target_delivery.target_id = ?)"), append(args, *query.Target)
	}
	if query.Ref != nil {
		clauses, args = append(clauses, "e.ref = ?"), append(args, *query.Ref)
	}
	if query.Rule != nil {
		clauses, args = append(clauses, "e.rule_id = ?"), append(args, *query.Rule)
	}
	if query.ID != "" {
		clauses, args = append(clauses, "e.id = ?"), append(args, query.ID)
	}
	if query.TransportStatus != nil || query.DeploymentStatus != nil {
		sub := []string{"d.event_id = e.id", "a.id = d.current_attempt_id", "a.delivery_id = d.id"}
		if query.TransportStatus != nil {
			sub, args = append(sub, "a.transport_status = ?"), append(args, *query.TransportStatus)
		}
		if query.DeploymentStatus != nil {
			sub, args = append(sub, "a.deployment_status = ?"), append(args, *query.DeploymentStatus)
		}
		clauses = append(clauses, "EXISTS (SELECT 1 FROM deliveries d JOIN delivery_attempts a ON a.id=d.current_attempt_id AND a.delivery_id=d.id WHERE "+strings.Join(sub, " AND ")+")")
	}
	return strings.Join(clauses, " AND "), args
}

type rowScanner interface{ Scan(...any) error }

func scanEventSummary(row rowScanner) (EventSummary, error) {
	var item EventSummary
	var received int64
	var ref, status, externalID, trigger, revision, commitMessage, ruleID sql.NullString
	if err := row.Scan(&item.ID, &received, &item.EventType, &item.Provider, &item.SourceID, &item.Repository, &ref, &status, &externalID, &trigger, &revision, &commitMessage, &item.RoutingResult, &ruleID); err != nil {
		return EventSummary{}, fmt.Errorf("scan event: %w", err)
	}
	item.ReceivedAt = time.UnixMilli(received).UTC()
	item.Ref, item.Status = nullableNonEmptyString(ref), nullableNonEmptyString(status)
	item.ExternalID, item.Trigger, item.Revision = nullableNonEmptyString(externalID), nullableNonEmptyString(trigger), nullableNonEmptyString(revision)
	item.CommitMessage = nullableNonEmptyString(commitMessage)
	if ruleID.Valid {
		value := ruleID.String
		item.RuleID = &value
	}
	return item, nil
}

func (s *Store) fillEventState(ctx context.Context, item *EventSummary) error {
	if item.EventType == "pipeline" && item.Status != nil && sourceStatusActive(*item.Status) {
		item.Active = true
	}
	rows, err := s.db.QueryContext(ctx, `SELECT d.id,d.target_id,d.current_attempt_id,a.transport_status,a.deployment_status
		FROM deliveries d JOIN delivery_attempts a ON a.id=d.current_attempt_id AND a.delivery_id=d.id WHERE d.event_id=? ORDER BY d.target_id,d.id`, item.ID)
	if err != nil {
		return fmt.Errorf("list event delivery states: %w", err)
	}
	defer rows.Close()
	item.Deliveries = []DeliveryListItem{}
	for rows.Next() {
		var current DeliveryListItem
		if err := rows.Scan(&current.ID, &current.TargetID, &current.CurrentAttemptID, &current.TransportStatus, &current.DeploymentStatus); err != nil {
			return fmt.Errorf("scan event delivery state: %w", err)
		}
		current.Active = domain.Active(domain.TransportStatus(current.TransportStatus), domain.DeploymentStatus(current.DeploymentStatus))
		current.AllowedOperations = domain.AllowedOperations(domain.TransportStatus(current.TransportStatus), domain.DeploymentStatus(current.DeploymentStatus))
		if current.AllowedOperations == nil {
			current.AllowedOperations = []domain.Operation{}
		}
		current.Attention = len(current.AllowedOperations) > 0
		item.Deliveries = append(item.Deliveries, current)
		item.Active = item.Active || current.Active
		item.Attention = item.Attention || current.Attention
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate event delivery states: %w", err)
	}
	item.DeliverySummary = DeliverySummary{Kind: "none", Count: len(item.Deliveries)}
	if len(item.Deliveries) == 0 {
		if item.EventType == "push" && item.Revision != nil {
			status := "waiting_for_pipeline"
			item.Active = true
			if s.now().After(item.ReceivedAt.Add(pipelineWaitTimeout)) {
				status = "pipeline_not_received"
				item.Active = false
			}
			item.Status = &status
		}
		return nil
	}
	item.DeliverySummary.Kind = "uniform"
	for _, current := range item.Deliveries[1:] {
		if current.TransportStatus != item.Deliveries[0].TransportStatus || current.DeploymentStatus != item.Deliveries[0].DeploymentStatus {
			item.DeliverySummary.Kind = "mixed"
			return nil
		}
	}
	item.DeliverySummary.TransportStatus = &item.Deliveries[0].TransportStatus
	item.DeliverySummary.DeploymentStatus = &item.Deliveries[0].DeploymentStatus
	return nil
}

func sourceStatusActive(status string) bool {
	switch status {
	case "created", "preparing", "scheduled", "pending", "queued", "waiting_for_resource", "requested", "running", "in_progress":
		return true
	default:
		return false
	}
}

// GetEventDetail returns one event with current deliveries and complete stable timelines.
func (s *Store) GetEventDetail(ctx context.Context, eventID string) (EventDetail, error) {
	currentEventID, err := s.currentLifecycleEventID(ctx, eventID)
	if err != nil {
		return EventDetail{}, err
	}
	eventID = currentEventID
	row := s.db.QueryRowContext(ctx, `SELECT e.id,e.received_at,e.event,e.provider,e.source_id,e.repository_id,e.ref,e.status,e.external_id,e.trigger,e.revision,e.commit_message,e.routing_result,e.rule_id,e.raw_headers_json,e.raw_payload_json,e.rule_snapshot FROM events e WHERE e.id=?`, eventID)
	var detail EventDetail
	var headers, payload, rule []byte
	var received int64
	var ref, status, externalID, trigger, revision, commitMessage, ruleID sql.NullString
	if err := row.Scan(&detail.ID, &received, &detail.EventType, &detail.Provider, &detail.SourceID, &detail.Repository, &ref, &status, &externalID, &trigger, &revision, &commitMessage, &detail.RoutingResult, &ruleID, &headers, &payload, &rule); err != nil {
		return EventDetail{}, fmt.Errorf("get event detail: %w", err)
	}
	detail.ReceivedAt = time.UnixMilli(received).UTC()
	detail.Ref, detail.Status = nullableNonEmptyString(ref), nullableNonEmptyString(status)
	detail.ExternalID, detail.Trigger, detail.Revision = nullableNonEmptyString(externalID), nullableNonEmptyString(trigger), nullableNonEmptyString(revision)
	detail.CommitMessage = nullableNonEmptyString(commitMessage)
	if ruleID.Valid {
		value := ruleID.String
		detail.RuleID = &value
	}
	detail.Headers, detail.Payload, detail.RuleSnapshot = decodedRedacted(headers, false), decodedRedacted(payload, false), decodedRedacted(rule, false)
	sourceStates, err := s.pipelineSourceStates(ctx, detail.EventSummary)
	if err != nil {
		return EventDetail{}, err
	}
	detail.SourceStates = sourceStates
	if err := s.fillEventState(ctx, &detail.EventSummary); err != nil {
		return EventDetail{}, err
	}
	deliveries, err := s.eventDeliveries(ctx, eventID)
	if err != nil {
		return EventDetail{}, err
	}
	detail.Deliveries = deliveries
	activities, err := s.eventActivities(ctx, eventID)
	if err != nil {
		return EventDetail{}, err
	}
	detail.Activities = activities
	return detail, nil
}

func (s *Store) currentLifecycleEventID(ctx context.Context, eventID string) (string, error) {
	var current string
	err := s.db.QueryRowContext(ctx, `SELECT latest.id
		FROM events current
		JOIN events latest
		  ON latest.provider = current.provider
		 AND latest.source_id = current.source_id
		 AND latest.repository_id = current.repository_id
		 AND latest.event = 'pipeline'
		 AND latest.external_id = current.external_id
		WHERE current.id = ? AND current.event = 'pipeline' AND current.external_id IS NOT NULL AND current.external_id <> ''
		ORDER BY latest.received_at DESC, latest.id DESC
		LIMIT 1`, eventID).Scan(&current)
	if err == nil {
		return current, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("resolve pipeline state: %w", err)
	}
	err = s.db.QueryRowContext(ctx, `SELECT pipeline.id
		FROM events push
		JOIN events pipeline
		  ON pipeline.provider = push.provider
		 AND pipeline.source_id = push.source_id
		 AND pipeline.repository_id = push.repository_id
		 AND pipeline.event = 'pipeline'
		 AND pipeline.trigger = 'push'
		 AND pipeline.revision IS NOT NULL
		 AND pipeline.revision <> ''
		 AND pipeline.revision = push.revision
		 AND pipeline.ref IS push.ref
		 AND pipeline.external_id IS NOT NULL
		 AND pipeline.external_id <> ''
		 AND pipeline.received_at >= push.received_at
		 AND NOT EXISTS (
			SELECT 1 FROM events earlier_pipeline
			WHERE earlier_pipeline.provider = pipeline.provider
			  AND earlier_pipeline.source_id = pipeline.source_id
			  AND earlier_pipeline.repository_id = pipeline.repository_id
			  AND earlier_pipeline.event = 'pipeline'
			  AND earlier_pipeline.external_id = pipeline.external_id
			  AND earlier_pipeline.received_at < push.received_at
		 )
		WHERE push.id = ? AND push.event = 'push' AND push.revision IS NOT NULL AND push.revision <> ''
		  AND NOT EXISTS (SELECT 1 FROM deliveries push_delivery WHERE push_delivery.event_id = push.id)
		ORDER BY pipeline.received_at DESC, pipeline.id DESC
		LIMIT 1`, eventID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return eventID, nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve lifecycle event: %w", err)
	}
	return current, nil
}

func (s *Store) pipelineSourceStates(ctx context.Context, event EventSummary) ([]SourceState, error) {
	result := []SourceState{}
	if event.EventType != "pipeline" || event.ExternalID == nil {
		return result, nil
	}
	if event.Revision != nil && event.Trigger != nil && *event.Trigger == "push" {
		var push SourceState
		var received, pipelineStarted int64
		if err := s.db.QueryRowContext(ctx, `SELECT MIN(received_at) FROM events
			WHERE provider=? AND source_id=? AND repository_id=? AND event='pipeline' AND external_id=?`, event.Provider, event.SourceID, event.Repository, *event.ExternalID).Scan(&pipelineStarted); err != nil {
			return nil, fmt.Errorf("find pipeline start: %w", err)
		}
		err := s.db.QueryRowContext(ctx, `SELECT id,received_at FROM events push
			WHERE provider=? AND source_id=? AND repository_id=? AND event='push' AND revision=? AND ref IS ? AND received_at<=?
			  AND NOT EXISTS (SELECT 1 FROM deliveries push_delivery WHERE push_delivery.event_id = push.id)
			ORDER BY received_at DESC,id DESC LIMIT 1`, event.Provider, event.SourceID, event.Repository, *event.Revision, event.Ref, pipelineStarted).Scan(&push.EventID, &received)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("find pipeline push state: %w", err)
		}
		if err == nil {
			push.Status = "waiting_for_pipeline"
			push.ReceivedAt = time.UnixMilli(received).UTC()
			result = append(result, push)
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,status,received_at FROM events
		WHERE provider=? AND source_id=? AND repository_id=? AND event=? AND external_id=?
		ORDER BY received_at,id`, event.Provider, event.SourceID, event.Repository, event.EventType, *event.ExternalID)
	if err != nil {
		return nil, fmt.Errorf("list pipeline source states: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var state SourceState
		var status sql.NullString
		var received int64
		if err := rows.Scan(&state.EventID, &status, &received); err != nil {
			return nil, fmt.Errorf("scan pipeline source state: %w", err)
		}
		state.Status = status.String
		state.ReceivedAt = time.UnixMilli(received).UTC()
		result = append(result, state)
	}
	return result, rows.Err()
}

func (s *Store) eventDeliveries(ctx context.Context, eventID string) ([]DeliveryDetail, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.id,d.target_id,d.target_snapshot,d.current_attempt_id,a.transport_status,a.deployment_status,d.created_at,d.updated_at
		FROM deliveries d JOIN delivery_attempts a ON a.id=d.current_attempt_id AND a.delivery_id=d.id WHERE d.event_id=? ORDER BY d.target_id,d.id`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list event deliveries: %w", err)
	}
	result := []DeliveryDetail{}
	for rows.Next() {
		var delivery DeliveryDetail
		var created, updated int64
		if err := rows.Scan(&delivery.ID, &delivery.TargetID, &delivery.TargetSnapshot, &delivery.CurrentAttemptID, &delivery.TransportStatus, &delivery.DeploymentStatus, &created, &updated); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan event delivery: %w", err)
		}
		delivery.CreatedAt, delivery.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
		delivery.Active = domain.Active(domain.TransportStatus(delivery.TransportStatus), domain.DeploymentStatus(delivery.DeploymentStatus))
		delivery.AllowedOperations = domain.AllowedOperations(domain.TransportStatus(delivery.TransportStatus), domain.DeploymentStatus(delivery.DeploymentStatus))
		if delivery.AllowedOperations == nil {
			delivery.AllowedOperations = []domain.Operation{}
		}
		delivery.Attention = len(delivery.AllowedOperations) > 0
		result = append(result, delivery)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate event deliveries: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close event deliveries: %w", err)
	}
	for index := range result {
		attempts, err := s.deliveryAttempts(ctx, result[index].ID, result[index].CurrentAttemptID, result[index].TargetSnapshot)
		if err != nil {
			return nil, err
		}
		result[index].Attempts = attempts
	}
	return result, nil
}

func (s *Store) deliveryAttempts(ctx context.Context, deliveryID, currentID string, targetSnapshot []byte) ([]AttemptDetail, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,actor,transport_status,deployment_status,deployment_id,request_json,response_json,request_at,response_at,enqueued_at,monitoring_deadline_at,created_at
		FROM delivery_attempts WHERE delivery_id=? ORDER BY created_at,id`, deliveryID)
	if err != nil {
		return nil, fmt.Errorf("list delivery attempts: %w", err)
	}
	defer rows.Close()
	result := []AttemptDetail{}
	for rows.Next() {
		var attempt AttemptDetail
		var actor, deploymentID sql.NullString
		var request, response []byte
		var requestAt, responseAt, enqueuedAt, deadline sql.NullInt64
		var created int64
		if err := rows.Scan(&attempt.ID, &attempt.Kind, &actor, &attempt.TransportStatus, &attempt.DeploymentStatus, &deploymentID, &request, &response, &requestAt, &responseAt, &enqueuedAt, &deadline, &created); err != nil {
			return nil, fmt.Errorf("scan delivery attempt: %w", err)
		}
		attempt.Actor, attempt.DeploymentID = nullableString(actor), nullableString(deploymentID)
		attempt.RequestSnapshot, attempt.ResponseSnapshot = decodedRedacted(request, true), decodedRedacted(response, true)
		attempt.DeployCommand = deploymentCurl(request, targetSnapshot, requestAt.Valid)
		attempt.RequestAt, attempt.ResponseAt, attempt.EnqueuedAt, attempt.MonitoringDeadlineAt = nullableTime(requestAt), nullableTime(responseAt), nullableTime(enqueuedAt), nullableTime(deadline)
		attempt.CreatedAt, attempt.Current = time.UnixMilli(created).UTC(), attempt.ID == currentID
		result = append(result, attempt)
	}
	return result, rows.Err()
}

func (s *Store) eventActivities(ctx context.Context, eventID string) ([]ActivityDetail, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,delivery_id,attempt_id,actor,action,before_json,after_json,created_at FROM audit_logs WHERE event_id=? ORDER BY created_at,id`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list event activities: %w", err)
	}
	defer rows.Close()
	result := []ActivityDetail{}
	for rows.Next() {
		var activity ActivityDetail
		var deliveryID, attemptID, actor sql.NullString
		var before, after []byte
		var created int64
		if err := rows.Scan(&activity.ID, &deliveryID, &attemptID, &actor, &activity.Action, &before, &after, &created); err != nil {
			return nil, fmt.Errorf("scan event activity: %w", err)
		}
		activity.DeliveryID, activity.AttemptID, activity.Actor = nullableString(deliveryID), nullableString(attemptID), nullableString(actor)
		activity.Before, activity.After, activity.CreatedAt = decodedRedacted(before, true), decodedRedacted(after, true), time.UnixMilli(created).UTC()
		result = append(result, activity)
	}
	return result, rows.Err()
}

func decodedRedacted(raw []byte, removeInfrastructure bool) any {
	if raw == nil {
		return nil
	}
	if _, ok := decodeJSON(raw); !ok {
		return "[invalid JSON]"
	}
	masked, _ := redact.JSON(raw, readSnapshotLimit)
	value, ok := decodeJSON(masked)
	if !ok {
		return "[invalid JSON]"
	}
	if removeInfrastructure {
		removeInfrastructureFields(value)
	}
	return value
}

func decodeJSON(input []byte) (any, bool) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	var extra any
	return value, decoder.Decode(&extra) == io.EOF
}

func removeInfrastructureFields(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
			if normalized == "composeid" || normalized == "resourceid" || normalized == "baseurl" || normalized == "url" || normalized == "idempotencykey" || normalized == "headers" || normalized == "error" {
				delete(typed, key)
				continue
			}
			removeInfrastructureFields(child)
		}
	case []any:
		for _, child := range typed {
			removeInfrastructureFields(child)
		}
	}
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	copy := value.String
	return &copy
}

func nullableNonEmptyString(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	copy := value.String
	return &copy
}

func nullableTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	copy := time.UnixMilli(value.Int64).UTC()
	return &copy
}
