// Package adminapi exposes Hookfly's stable management HTTP contract.
package adminapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hoomoli/hookfly/internal/auth"
	"github.com/hoomoli/hookfly/internal/domain"
	"github.com/hoomoli/hookfly/internal/runtimecfg"
	"github.com/hoomoli/hookfly/internal/service"
	"github.com/hoomoli/hookfly/internal/store"
)

const maxManualBody = 64 * 1024

type queryStore interface {
	Ready(context.Context) error
	ListRepositories(context.Context, []store.ConfiguredRepository) ([]store.ConfiguredRepository, error)
	ListEvents(context.Context, store.EventQuery) (store.EventPage, error)
	GetEventDetail(context.Context, string) (store.EventDetail, error)
	ListNotifications(context.Context, store.NotificationQuery) (store.NotificationPage, error)
	SubscribeNotifications() (<-chan struct{}, func())
	ClearHistory(context.Context) (store.ClearHistoryResult, error)
}

type attemptExecutor interface {
	Execute(context.Context, string, string, domain.Operation, string, string) (service.AttemptResult, error)
}

type configController interface {
	Status(context.Context) (runtimecfg.Status, error)
	Reload(context.Context, string) (runtimecfg.ReloadOutcome, error)
	BindingStatus(string, []byte) runtimecfg.BindingStatus
	Repositories() []store.ConfiguredRepository
	Connections() []runtimecfg.ConnectionSummary
	DiscoverConnectionResources(context.Context, string) ([]runtimecfg.ConnectionResource, error)
	TargetInventory(context.Context) runtimecfg.TargetInventory
}

// Dependencies are the narrow app-owned boundaries used by the management handler.
type Dependencies struct {
	Store    queryStore
	Attempts attemptExecutor
	Config   configController
	Provider auth.Provider
	Auth     auth.Service
}

type handler struct{ dependencies Dependencies }

// New constructs a management-only mux. The public listener never receives it.
func New(dependencies Dependencies) http.Handler {
	h := &handler{dependencies: dependencies}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", h.health)
	if dependencies.Auth != nil {
		mux.HandleFunc("GET /api/v1/auth/login", dependencies.Auth.Login)
		mux.HandleFunc("GET /api/v1/auth/callback", dependencies.Auth.Callback)
		mux.HandleFunc("GET /api/v1/auth/session", dependencies.Auth.Session)
		mux.Handle("POST /api/v1/auth/logout", h.sameOrigin(http.HandlerFunc(dependencies.Auth.Logout)))
	}
	mux.Handle("GET /api/v1/repositories", h.protected(auth.PermissionEventsRead, http.HandlerFunc(h.repositories)))
	mux.Handle("GET /api/v1/connections", h.protected(auth.PermissionConnectionsRead, http.HandlerFunc(h.connections)))
	mux.Handle("GET /api/v1/connections/{connection_id}/resources", h.protected(auth.PermissionConnectionsRead, http.HandlerFunc(h.connectionResources)))
	mux.Handle("GET /api/v1/targets", h.protected(auth.PermissionConnectionsRead, http.HandlerFunc(h.targets)))
	mux.Handle("GET /api/v1/events", h.protected(auth.PermissionEventsRead, http.HandlerFunc(h.events)))
	mux.Handle("GET /api/v1/events/{event_id}", h.protected(auth.PermissionEventsRead, http.HandlerFunc(h.eventDetail)))
	mux.Handle("DELETE /api/v1/events", h.protectedUnsafe(auth.PermissionEventsDelete, http.HandlerFunc(h.clearHistory)))
	mux.Handle("GET /api/v1/notifications", h.protected(auth.PermissionEventsRead, http.HandlerFunc(h.notifications)))
	mux.Handle("GET /api/v1/notifications/stream", h.protected(auth.PermissionEventsRead, http.HandlerFunc(h.notificationsStream)))
	mux.Handle("POST /api/v1/deliveries/{delivery_id}/attempts", h.protectedUnsafe(auth.PermissionDeploymentsRetry, http.HandlerFunc(h.createAttempt)))
	mux.Handle("GET /api/v1/config/status", h.protected(auth.PermissionEventsRead, http.HandlerFunc(h.configStatus)))
	mux.Handle("POST /api/v1/config/reload", h.protectedUnsafe(auth.PermissionConfigReload, http.HandlerFunc(h.reloadConfig)))
	return noStoreAuthRoutes(mux)
}

func noStoreAuthRoutes(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/api/v1/auth/") {
			writer.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(writer, request)
	})
}

func (h *handler) targets(w http.ResponseWriter, r *http.Request) {
	if h.dependencies.Config == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		return
	}
	writeJSON(w, http.StatusOK, h.dependencies.Config.TargetInventory(r.Context()))
}

func (h *handler) notifications(w http.ResponseWriter, r *http.Request) {
	if !h.storeAvailable(w) {
		return
	}
	values := r.URL.Query()
	for key := range values {
		if key != "after" && key != "limit" {
			writeError(w, http.StatusBadRequest, "invalid_query", "invalid query parameters")
			return
		}
	}
	query := store.NotificationQuery{Limit: 50}
	var err error
	if query.After, _, err = singleValue(values, "after"); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", "invalid query parameters")
		return
	}
	if raw, present, valueErr := singleValue(values, "limit"); valueErr != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", "invalid query parameters")
		return
	} else if present {
		query.Limit, err = strconv.Atoi(raw)
		if err != nil || query.Limit < 1 || query.Limit > 100 {
			writeError(w, http.StatusBadRequest, "invalid_query", "invalid query parameters")
			return
		}
	}
	page, err := h.dependencies.Store.ListNotifications(r.Context(), query)
	if errors.Is(err, store.ErrInvalidNotificationCursor) {
		writeError(w, http.StatusBadRequest, "invalid_query", "invalid query parameters")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		return
	}
	items := make([]notificationResponse, 0, len(page.Items))
	for _, fact := range page.Items {
		items = append(items, toNotificationResponse(fact))
	}
	writeJSON(w, http.StatusOK, struct {
		Items        []notificationResponse `json:"items"`
		LatestCursor string                 `json:"latest_cursor"`
	}{Items: items, LatestCursor: page.LatestCursor})
}

// notificationsStream serves the notification feed as server-sent events. The
// client baselines through the REST feed, then opens this stream with its
// cursor; the server pushes newer facts as they are committed and re-checks on
// every heartbeat so a missed wake-up only delays delivery.
func (h *handler) notificationsStream(w http.ResponseWriter, r *http.Request) {
	if !h.storeAvailable(w) {
		return
	}
	values := r.URL.Query()
	for key := range values {
		if key != "after" {
			writeError(w, http.StatusBadRequest, "invalid_query", "invalid query parameters")
			return
		}
	}
	after, _, err := singleValue(values, "after")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", "invalid query parameters")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		return
	}
	cursor, resumed := after, false
	if lastEventID := r.Header.Get("Last-Event-ID"); lastEventID != "" {
		cursor, resumed = lastEventID, true
	}
	load := func() (store.NotificationPage, error) {
		return h.dependencies.Store.ListNotifications(r.Context(), store.NotificationQuery{After: cursor, Limit: 100})
	}
	page, err := load()
	if errors.Is(err, store.ErrInvalidNotificationCursor) && resumed {
		cursor, resumed = "", false
		page, err = load()
	}
	if errors.Is(err, store.ErrInvalidNotificationCursor) {
		writeError(w, http.StatusBadRequest, "invalid_query", "invalid query parameters")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		return
	}
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	changes, unsubscribe := h.dependencies.Store.SubscribeNotifications()
	defer unsubscribe()
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	first := true
	for {
		if first && cursor == "" {
			// A fresh stream starts from the current tail instead of replaying history.
			cursor = page.LatestCursor
		} else {
			for _, fact := range page.Items {
				payload, err := json.Marshal(toNotificationResponse(fact))
				if err != nil {
					continue
				}
				if _, err := fmt.Fprintf(w, "id: %s\nevent: fact\ndata: %s\n\n", fact.Cursor, payload); err != nil {
					return
				}
				cursor = fact.Cursor
			}
		}
		first = false
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-changes:
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
		page, err = load()
		if err != nil {
			// Mid-stream failures end the response; EventSource reconnects and resumes.
			return
		}
	}
}

func (h *handler) connections(w http.ResponseWriter, _ *http.Request) {
	if h.dependencies.Config == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		return
	}
	connections := h.dependencies.Config.Connections()
	if connections == nil {
		connections = []runtimecfg.ConnectionSummary{}
	}
	writeJSON(w, http.StatusOK, struct {
		Connections []runtimecfg.ConnectionSummary `json:"connections"`
	}{Connections: connections})
}

func (h *handler) connectionResources(w http.ResponseWriter, r *http.Request) {
	if h.dependencies.Config == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		return
	}
	resources, err := h.dependencies.Config.DiscoverConnectionResources(r.Context(), r.PathValue("connection_id"))
	if err != nil {
		switch {
		case errors.Is(err, runtimecfg.ErrConnectionNotFound):
			writeError(w, http.StatusNotFound, "not_found", "connection not found")
		case errors.Is(err, runtimecfg.ErrConnectionCapabilityUnavailable):
			writeError(w, http.StatusUnprocessableEntity, "capability_unavailable", "resource discovery unavailable")
		default:
			writeError(w, http.StatusBadGateway, "upstream_unavailable", "resource discovery unavailable")
		}
		return
	}
	if resources == nil {
		resources = []runtimecfg.ConnectionResource{}
	}
	writeJSON(w, http.StatusOK, struct {
		Resources []runtimecfg.ConnectionResource `json:"resources"`
	}{Resources: resources})
}

func (h *handler) protected(permission string, next http.Handler) http.Handler {
	return auth.Authenticate(h.dependencies.Provider, auth.Require(permission, next))
}

func (h *handler) protectedUnsafe(permission string, next http.Handler) http.Handler {
	return auth.Authenticate(h.dependencies.Provider, auth.Require(permission, h.sameOrigin(next)))
}

func (h *handler) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := ""
		if h.dependencies.Auth != nil {
			expected = h.dependencies.Auth.ExpectedOrigin()
		}
		if expected != "" && r.Header.Get("Origin") != expected {
			w.Header().Set("Cache-Control", "no-store")
			writeError(w, http.StatusForbidden, "forbidden", "origin not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	if h.dependencies.Store == nil || h.dependencies.Store.Ready(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Status string `json:"status"`
	}{Status: "ok"})
}

func (h *handler) repositories(w http.ResponseWriter, r *http.Request) {
	if !h.storeAvailable(w) {
		return
	}
	if h.dependencies.Config == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		return
	}
	repositories, err := h.dependencies.Store.ListRepositories(r.Context(), h.dependencies.Config.Repositories())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Repositories []store.ConfiguredRepository `json:"repositories"`
	}{Repositories: append([]store.ConfiguredRepository{}, repositories...)})
}

func (h *handler) configStatus(w http.ResponseWriter, r *http.Request) {
	if h.dependencies.Config == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		return
	}
	status, err := h.dependencies.Config.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *handler) reloadConfig(w http.ResponseWriter, r *http.Request) {
	if h.dependencies.Config == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		return
	}
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	outcome, err := h.dependencies.Config.Reload(r.Context(), principal.ID)
	if err != nil {
		switch {
		case errors.Is(err, runtimecfg.ErrReloadInProgress):
			writeError(w, http.StatusConflict, "reload_in_progress", "configuration reload already in progress")
		case errors.Is(err, runtimecfg.ErrInvalidConfig):
			writeError(w, http.StatusUnprocessableEntity, "invalid_config", "configuration is invalid")
		case errors.Is(err, runtimecfg.ErrPreWaitUnavailable):
			writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		default:
			writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		}
		return
	}
	status := http.StatusOK
	if outcome == runtimecfg.ReloadWaiting {
		status = http.StatusAccepted
	} else if outcome != runtimecfg.ReloadApplied {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		return
	}
	writeJSON(w, status, struct {
		Status runtimecfg.ReloadOutcome `json:"status"`
	}{Status: outcome})
}

func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	if !h.storeAvailable(w) {
		return
	}
	query, err := parseEventQuery(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", "invalid query parameters")
		return
	}
	page, err := h.dependencies.Store.ListEvents(r.Context(), query)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		return
	}
	items := make([]eventResponse, 0, len(page.Items))
	principal, _ := auth.PrincipalFromContext(r.Context())
	for _, item := range page.Items {
		items = append(items, publicEvent(item, principal.Permissions[auth.PermissionDeploymentsRetry]))
	}
	writeJSON(w, http.StatusOK, eventPageResponse{
		Items: items, Page: page.Page, PageSize: page.PageSize, Total: page.Total,
		HasPrevious: page.HasPrevious, HasNext: page.HasNext, Active: page.Active,
	})
}

func (h *handler) eventDetail(w http.ResponseWriter, r *http.Request) {
	if !h.storeAvailable(w) {
		return
	}
	detail, err := h.dependencies.Store.GetEventDetail(r.Context(), r.PathValue("event_id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not_found", "event not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		return
	}
	principal, _ := auth.PrincipalFromContext(r.Context())
	writeJSON(w, http.StatusOK, publicDetail(detail, principal.Permissions[auth.PermissionDeploymentsRetry], h.dependencies.Config))
}

func (h *handler) clearHistory(w http.ResponseWriter, r *http.Request) {
	if !h.storeAvailable(w) {
		return
	}
	result, err := h.dependencies.Store.ClearHistory(r.Context())
	if errors.Is(err, store.ErrHistoryActive) {
		writeError(w, http.StatusConflict, "history_active", "active deployments or queued events must finish first")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) storeAvailable(w http.ResponseWriter) bool {
	if h.dependencies.Store != nil {
		return true
	}
	writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
	return false
}

func (h *handler) createAttempt(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "application/json required")
		return
	}
	body := http.MaxBytesReader(w, r.Body, maxManualBody)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var request struct {
		ExpectedCurrentAttemptID string `json:"expected_current_attempt_id"`
		Operation                string `json:"operation"`
		Reason                   string `json:"reason"`
	}
	if err := decoder.Decode(&request); err != nil {
		writeDecodeError(w, err)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeDecodeError(w, err)
		return
	}
	operation := domain.Operation(request.Operation)
	if request.ExpectedCurrentAttemptID == "" || (operation != domain.OperationRetry && operation != domain.OperationRedeploy) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
		return
	}
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if h.dependencies.Attempts == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
		return
	}
	result, err := h.dependencies.Attempts.Execute(r.Context(), r.PathValue("delivery_id"), request.ExpectedCurrentAttemptID, operation, principal.ID, request.Reason)
	if err != nil {
		h.writeAttemptError(w, err)
		return
	}
	if !result.Created {
		writeError(w, http.StatusConflict, "state_changed", "delivery state changed")
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		DeliveryID string `json:"delivery_id"`
		AttemptID  string `json:"attempt_id"`
	}{DeliveryID: r.PathValue("delivery_id"), AttemptID: result.AttemptID})
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body too large")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_request", "invalid request")
}

func (h *handler) writeAttemptError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrOperationNotAllowed):
		writeError(w, http.StatusConflict, "operation_not_allowed", "operation not allowed")
	case errors.Is(err, service.ErrDeploymentStillActive):
		writeError(w, http.StatusConflict, "deployment_still_active", "deployment still active")
	case errors.Is(err, service.ErrAlreadySucceeded):
		writeError(w, http.StatusConflict, "already_succeeded", "deployment already succeeded")
	case errors.Is(err, service.ErrStateChanged):
		writeError(w, http.StatusConflict, "state_changed", "delivery state changed")
	case errors.Is(err, service.ErrReconciliationUnavailable):
		writeError(w, http.StatusServiceUnavailable, "reconciliation_unavailable", "reconciliation unavailable")
	case errors.Is(err, service.ErrConfigReloadInProgress):
		writeError(w, http.StatusConflict, "config_reload_in_progress", "configuration reload in progress")
	case errors.Is(err, service.ErrTargetChanged):
		writeError(w, http.StatusConflict, "target_changed", "historical target changed")
	case errors.Is(err, service.ErrTargetUnavailable):
		writeError(w, http.StatusConflict, "target_unavailable", "historical target unavailable")
	default:
		writeError(w, http.StatusServiceUnavailable, "unavailable", "service unavailable")
	}
}

func parseEventQuery(values url.Values) (store.EventQuery, error) {
	allowed := map[string]bool{"source": true, "repository": true, "target": true, "unmatched": true, "ref": true, "rule": true, "id": true, "status": true, "page": true, "page_size": true}
	for key := range values {
		if !allowed[key] {
			return store.EventQuery{}, fmt.Errorf("unknown query %q", key)
		}
	}
	query := store.EventQuery{Page: 1, PageSize: 50}
	source, sourcePresent, err := singleValue(values, "source")
	if err != nil {
		return query, err
	}
	repository, repositoryPresent, err := singleValue(values, "repository")
	if err != nil {
		return query, err
	}
	if sourcePresent != repositoryPresent {
		return query, errors.New("source and repository filters must be provided together")
	}
	if sourcePresent {
		query.Source, query.Repository = &source, &repository
	}
	if raw, present, err := singleValue(values, "target"); err != nil {
		return query, err
	} else if present {
		query.Target = &raw
	}
	if raw, present, err := singleValue(values, "unmatched"); err != nil {
		return query, err
	} else if present {
		if raw != "true" && raw != "false" {
			return query, errors.New("invalid unmatched filter")
		}
		value := raw == "true"
		query.Unmatched = &value
	}
	for key, target := range map[string]**string{"ref": &query.Ref, "rule": &query.Rule} {
		raw, present, err := singleValue(values, key)
		if err != nil {
			return query, err
		}
		if present {
			value := raw
			*target = &value
		}
	}
	if raw, present, err := singleValue(values, "id"); err != nil {
		return query, err
	} else if present {
		query.ID = raw
	}
	if raw, present, err := singleValue(values, "page"); err != nil {
		return query, err
	} else if present {
		query.Page, err = strconv.Atoi(raw)
		if err != nil || query.Page < 1 {
			return query, errors.New("invalid page")
		}
	}
	if raw, present, err := singleValue(values, "page_size"); err != nil {
		return query, err
	} else if present {
		query.PageSize, err = strconv.Atoi(raw)
		if err != nil || query.PageSize < 1 || query.PageSize > 200 {
			return query, errors.New("invalid page size")
		}
	}
	for _, raw := range values["status"] {
		parts := strings.SplitN(raw, ":", 2)
		if len(parts) != 2 || parts[1] == "" {
			return query, errors.New("invalid status")
		}
		value := parts[1]
		switch parts[0] {
		case "transport":
			if query.TransportStatus != nil || !validTransport(value) {
				return query, errors.New("invalid transport status")
			}
			query.TransportStatus = &value
		case "deployment":
			if query.DeploymentStatus != nil || !validDeployment(value) {
				return query, errors.New("invalid deployment status")
			}
			query.DeploymentStatus = &value
		default:
			return query, errors.New("invalid status dimension")
		}
	}
	return query, nil
}

func singleValue(values url.Values, key string) (string, bool, error) {
	raw, present := values[key]
	if !present {
		return "", false, nil
	}
	if len(raw) != 1 || raw[0] == "" {
		return "", false, fmt.Errorf("invalid %s", key)
	}
	return raw[0], true, nil
}

func validTransport(value string) bool {
	switch domain.TransportStatus(value) {
	case domain.TransportPending, domain.TransportSending, domain.TransportEnqueued, domain.TransportFailed, domain.TransportUnknown:
		return true
	default:
		return false
	}
}

func validDeployment(value string) bool {
	switch domain.DeploymentStatus(value) {
	case domain.DeploymentNotStarted, domain.DeploymentLocating, domain.DeploymentRunning, domain.DeploymentDone,
		domain.DeploymentUnrecognized, domain.DeploymentError, domain.DeploymentCancelled, domain.DeploymentTimeout, domain.DeploymentUnknown:
		return true
	default:
		return false
	}
}

type deliverySummaryResponse struct {
	Kind             string  `json:"kind"`
	Count            int     `json:"count"`
	TransportStatus  *string `json:"transport_status"`
	DeploymentStatus *string `json:"deployment_status"`
}

type eventResponse struct {
	ID              string                  `json:"id"`
	ReceivedAt      string                  `json:"received_at"`
	EventType       string                  `json:"event_type"`
	Provider        string                  `json:"provider"`
	SourceID        string                  `json:"source_id"`
	Repository      string                  `json:"repository"`
	Ref             *string                 `json:"ref"`
	Status          *string                 `json:"status"`
	Revision        *string                 `json:"revision"`
	CommitMessage   *string                 `json:"commit_message"`
	ExternalID      *string                 `json:"external_id"`
	Trigger         *string                 `json:"trigger"`
	RoutingResult   string                  `json:"routing_result"`
	RuleID          *string                 `json:"rule_id"`
	DeliverySummary deliverySummaryResponse `json:"delivery_summary"`
	Deliveries      []deliveryListResponse  `json:"deliveries"`
	Active          bool                    `json:"active"`
	Attention       bool                    `json:"attention"`
}

type deliveryListResponse struct {
	ID                string             `json:"id"`
	TargetID          string             `json:"target_id"`
	CurrentAttemptID  string             `json:"current_attempt_id"`
	TransportStatus   string             `json:"transport_status"`
	DeploymentStatus  string             `json:"deployment_status"`
	Active            bool               `json:"active"`
	Attention         bool               `json:"attention"`
	AllowedOperations []domain.Operation `json:"allowed_operations"`
}

type eventPageResponse struct {
	Items       []eventResponse `json:"items"`
	Page        int             `json:"page"`
	PageSize    int             `json:"page_size"`
	Total       int64           `json:"total"`
	HasPrevious bool            `json:"has_previous"`
	HasNext     bool            `json:"has_next"`
	Active      bool            `json:"active"`
}

type notificationResponse struct {
	ID         string  `json:"id"`
	Cursor     string  `json:"cursor"`
	Category   string  `json:"category"`
	Outcome    string  `json:"outcome"`
	EventID    string  `json:"event_id"`
	DeliveryID *string `json:"delivery_id,omitempty"`
	TargetID   *string `json:"target_id,omitempty"`
	Provider   string  `json:"provider"`
	SourceID   string  `json:"source_id"`
	Repository string  `json:"repository"`
	Summary    string  `json:"summary"`
	OccurredAt string  `json:"occurred_at"`
}

func toNotificationResponse(fact store.NotificationFact) notificationResponse {
	item := notificationResponse{
		ID: fact.ID, Cursor: fact.Cursor, Category: fact.Category, Outcome: fact.Outcome,
		EventID: fact.EventID, Provider: fact.Provider, SourceID: fact.SourceID, Repository: fact.Repository, Summary: fact.Summary, OccurredAt: formatTime(fact.OccurredAt),
	}
	if fact.DeliveryID != "" {
		item.DeliveryID = &fact.DeliveryID
	}
	if fact.TargetID != "" {
		item.TargetID = &fact.TargetID
	}
	return item
}

func publicEvent(item store.EventSummary, canRetry bool) eventResponse {
	result := eventResponse{
		ID: item.ID, ReceivedAt: formatTime(item.ReceivedAt), EventType: item.EventType,
		Provider: item.Provider, SourceID: item.SourceID, Repository: item.Repository,
		Ref: item.Ref, Status: item.Status, Revision: item.Revision, CommitMessage: item.CommitMessage, ExternalID: item.ExternalID, Trigger: item.Trigger,
		RoutingResult: item.RoutingResult, RuleID: item.RuleID, Active: item.Active, Attention: item.Attention,
		DeliverySummary: deliverySummaryResponse(item.DeliverySummary),
		Deliveries:      []deliveryListResponse{},
	}
	for _, delivery := range item.Deliveries {
		allowed := append([]domain.Operation{}, delivery.AllowedOperations...)
		if !canRetry {
			allowed = []domain.Operation{}
		}
		result.Deliveries = append(result.Deliveries, deliveryListResponse{ID: delivery.ID, TargetID: delivery.TargetID, CurrentAttemptID: delivery.CurrentAttemptID, TransportStatus: delivery.TransportStatus, DeploymentStatus: delivery.DeploymentStatus, Active: delivery.Active, Attention: delivery.Attention, AllowedOperations: allowed})
	}
	return result
}

type detailResponse struct {
	eventResponse
	Headers      any                   `json:"headers"`
	Payload      any                   `json:"payload"`
	RuleSnapshot any                   `json:"rule_snapshot"`
	SourceStates []sourceStateResponse `json:"source_states"`
	Deliveries   []deliveryResponse    `json:"deliveries"`
	Activities   []activityResponse    `json:"activities"`
}

type sourceStateResponse struct {
	EventID    string `json:"event_id"`
	Status     string `json:"status"`
	ReceivedAt string `json:"received_at"`
}

type deliveryResponse struct {
	ID                  string                   `json:"id"`
	TargetID            string                   `json:"target_id"`
	CurrentAttemptID    string                   `json:"current_attempt_id"`
	TransportStatus     string                   `json:"transport_status"`
	DeploymentStatus    string                   `json:"deployment_status"`
	Active              bool                     `json:"active"`
	Attention           bool                     `json:"attention"`
	TargetBindingStatus runtimecfg.BindingStatus `json:"target_binding_status"`
	AllowedOperations   []domain.Operation       `json:"allowed_operations"`
	CreatedAt           string                   `json:"created_at"`
	UpdatedAt           string                   `json:"updated_at"`
	Attempts            []attemptResponse        `json:"attempts"`
}

type attemptResponse struct {
	ID                   string  `json:"id"`
	Kind                 string  `json:"kind"`
	Actor                *string `json:"actor"`
	Current              bool    `json:"current"`
	TransportStatus      string  `json:"transport_status"`
	DeploymentStatus     string  `json:"deployment_status"`
	DeploymentID         *string `json:"deployment_id"`
	RequestSnapshot      any     `json:"request_snapshot"`
	ResponseSnapshot     any     `json:"response_snapshot"`
	DeployCommand        *string `json:"deploy_command"`
	RequestAt            *string `json:"request_at"`
	ResponseAt           *string `json:"response_at"`
	EnqueuedAt           *string `json:"enqueued_at"`
	MonitoringDeadlineAt *string `json:"monitoring_deadline_at"`
	CreatedAt            string  `json:"created_at"`
}

type activityResponse struct {
	ID         string  `json:"id"`
	DeliveryID *string `json:"delivery_id"`
	AttemptID  *string `json:"attempt_id"`
	Actor      *string `json:"actor"`
	Action     string  `json:"action"`
	Before     any     `json:"before"`
	After      any     `json:"after"`
	CreatedAt  string  `json:"created_at"`
}

func publicDetail(detail store.EventDetail, canRetry bool, controller configController) detailResponse {
	result := detailResponse{eventResponse: publicEvent(detail.EventSummary, canRetry), Headers: detail.Headers, Payload: detail.Payload, RuleSnapshot: detail.RuleSnapshot, SourceStates: []sourceStateResponse{}, Deliveries: []deliveryResponse{}, Activities: []activityResponse{}}
	for _, state := range detail.SourceStates {
		result.SourceStates = append(result.SourceStates, sourceStateResponse{EventID: state.EventID, Status: state.Status, ReceivedAt: formatTime(state.ReceivedAt)})
	}
	for _, delivery := range detail.Deliveries {
		bindingStatus := runtimecfg.BindingCompatible
		if controller != nil {
			bindingStatus = controller.BindingStatus(delivery.TargetID, delivery.TargetSnapshot)
		}
		allowed := append([]domain.Operation(nil), delivery.AllowedOperations...)
		if !canRetry || bindingStatus != runtimecfg.BindingCompatible || len(allowed) == 0 {
			allowed = []domain.Operation{}
		}
		attention := delivery.Attention || bindingStatus != runtimecfg.BindingCompatible
		if bindingStatus != runtimecfg.BindingCompatible {
			result.Attention = true
		}
		public := deliveryResponse{ID: delivery.ID, TargetID: delivery.TargetID, CurrentAttemptID: delivery.CurrentAttemptID, TransportStatus: delivery.TransportStatus, DeploymentStatus: delivery.DeploymentStatus, Active: delivery.Active, Attention: attention, TargetBindingStatus: bindingStatus, AllowedOperations: allowed, CreatedAt: formatTime(delivery.CreatedAt), UpdatedAt: formatTime(delivery.UpdatedAt), Attempts: []attemptResponse{}}
		for _, attempt := range delivery.Attempts {
			public.Attempts = append(public.Attempts, attemptResponse{ID: attempt.ID, Kind: attempt.Kind, Actor: attempt.Actor, Current: attempt.Current, TransportStatus: attempt.TransportStatus, DeploymentStatus: attempt.DeploymentStatus, DeploymentID: attempt.DeploymentID, RequestSnapshot: attempt.RequestSnapshot, ResponseSnapshot: attempt.ResponseSnapshot, DeployCommand: attempt.DeployCommand, RequestAt: formatTimePointer(attempt.RequestAt), ResponseAt: formatTimePointer(attempt.ResponseAt), EnqueuedAt: formatTimePointer(attempt.EnqueuedAt), MonitoringDeadlineAt: formatTimePointer(attempt.MonitoringDeadlineAt), CreatedAt: formatTime(attempt.CreatedAt)})
		}
		result.Deliveries = append(result.Deliveries, public)
	}
	for _, activity := range detail.Activities {
		result.Activities = append(result.Activities, activityResponse{ID: activity.ID, DeliveryID: activity.DeliveryID, AttemptID: activity.AttemptID, Actor: activity.Actor, Action: activity.Action, Before: activity.Before, After: activity.After, CreatedAt: formatTime(activity.CreatedAt)})
	}
	return result
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func formatTimePointer(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := formatTime(*value)
	return &formatted
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
