// Package publichook accepts authenticated provider webhooks.
package publichook

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"time"

	"github.com/hoomoli/hookfly/internal/domain"
	"github.com/hoomoli/hookfly/internal/provider"
	"github.com/hoomoli/hookfly/internal/runtimecfg"
)

const maxBodyBytes = 2 << 20

// Ingester is the durable direct and deferred webhook persistence boundary.
type Ingester interface {
	Ingest(context.Context, domain.IngestCommand) (domain.IngestResult, error)
	IngestDeferred(context.Context, domain.IngestCommand) (domain.IngestResult, error)
}

type handler struct {
	provider string
	manager  *runtimecfg.Manager
	ingester Ingester
	clock    func() time.Time
}

// New returns a generation-aware handler bound to one webhook provider.
func New(providerName string, manager *runtimecfg.Manager, ingester Ingester, clock func() time.Time) http.Handler {
	if clock == nil {
		clock = time.Now
	}
	if ingester != nil {
		value := reflect.ValueOf(ingester)
		switch value.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
			if value.IsNil() {
				ingester = nil
			}
		}
	}
	return &handler{provider: providerName, manager: manager, ingester: ingester, clock: clock}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil || h.ingester == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Status: "unavailable"})
		return
	}

	view := h.manager.Read()
	if view == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Status: "unavailable"})
		return
	}
	defer view.Unlock()
	if view.Generation == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Status: "unavailable"})
		return
	}
	var source *runtimecfg.Source
	var found bool
	if h.provider == "gitlab" {
		source, found = view.Generation.GitLabSource(r.Header)
		if !found {
			writeJSON(w, http.StatusUnauthorized, errorResponse{Status: "unauthorized"})
			return
		}
	} else {
		source, found = view.Generation.Source(h.provider, r.PathValue("source_id"))
		if !found {
			writeJSON(w, http.StatusNotFound, errorResponse{Status: "not_found"})
			return
		}
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Status: "payload_too_large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, errorResponse{Status: "bad_request"})
		return
	}
	if !source.Authenticate(r.Header, body) {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Status: "unauthorized"})
		return
	}
	event, incoming, err := source.Normalize(r.Header, body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Status: "bad_request"})
		return
	}
	if h.provider == "gitlab" && incoming.Supported && event.Repository == "" {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Status: "unauthorized"})
		return
	}
	headersJSON, err := json.Marshal(incoming.SafeHeaders)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Status: "internal_error"})
		return
	}
	command := domain.IngestCommand{
		DeliveryID: deliveryIdentity(event, incoming, body), CanonicalEvent: event,
		ReceivedAt: h.clock(), ConfigDigest: view.Generation.Digest(),
		HeadersJSON: headersJSON, PayloadJSON: body,
	}
	deferred := incoming.Supported && reloadDefers(view.State)
	switch {
	case !incoming.Supported:
		command.RoutingResult = domain.RoutingUnsupported
	case deferred:
		command.RoutingResult = domain.RoutingDeferred
	default:
		decision, routeErr := view.Generation.Route(event)
		if routeErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorResponse{Status: "unavailable"})
			return
		}
		command.RoutingResult = decision.RoutingResult
		command.RuleID = decision.RuleID
		command.RuleSnapshot = append([]byte(nil), decision.RuleSnapshot...)
		command.Deliveries = cloneDeliveries(decision.Deliveries)
	}
	if deferred || hasForwardDelivery(command.Deliveries) {
		command.RequestJSON, err = json.Marshal(struct {
			Version  int         `json:"version"`
			Method   string      `json:"method"`
			Host     string      `json:"host"`
			RawQuery string      `json:"raw_query,omitempty"`
			Headers  http.Header `json:"headers"`
		}{Version: 1, Method: r.Method, Host: r.Host, RawQuery: r.URL.RawQuery, Headers: r.Header})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Status: "internal_error"})
			return
		}
	}

	var result domain.IngestResult
	if deferred {
		result, err = h.ingester.IngestDeferred(r.Context(), command)
	} else {
		result, err = h.ingester.Ingest(r.Context(), command)
	}
	view.Unlock()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Status: "unavailable"})
		return
	}
	if deferred && h.manager != nil {
		h.manager.NotifyDrain()
	}
	writeJSON(w, http.StatusAccepted, acceptedResponse{
		EventID: result.EventID, Status: "accepted", Duplicate: result.Duplicate,
	})
}

func reloadDefers(state runtimecfg.State) bool {
	return state == runtimecfg.StateWaiting || state == runtimecfg.StateDraining || state == runtimecfg.StateApplying
}

func deliveryIdentity(event domain.CanonicalEvent, incoming provider.IncomingEvent, body []byte) string {
	hash := sha256.New()
	writeIdentityComponent(hash, event.Provider)
	writeIdentityComponent(hash, event.Source)
	if incoming.DeliveryID != "" {
		writeIdentityComponent(hash, "delivery")
		writeIdentityComponent(hash, incoming.DeliveryID)
	} else {
		writeIdentityComponent(hash, "fallback")
		for _, component := range []string{
			incoming.RepositoryExternalID, event.Event, event.Ref, event.Status,
			event.Revision, event.ExternalID, event.Trigger,
		} {
			writeIdentityComponent(hash, component)
		}
		writeIdentityBytes(hash, body)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

type identityWriter interface {
	Write([]byte) (int, error)
}

func writeIdentityComponent(writer identityWriter, value string) {
	writeIdentityBytes(writer, []byte(value))
}

func writeIdentityBytes(writer identityWriter, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}

func cloneDeliveries(deliveries []domain.NewDelivery) []domain.NewDelivery {
	cloned := make([]domain.NewDelivery, len(deliveries))
	for index, delivery := range deliveries {
		cloned[index] = domain.NewDelivery{
			TargetID:       delivery.TargetID,
			TargetSnapshot: append([]byte(nil), delivery.TargetSnapshot...),
			DispatchKey:    delivery.DispatchKey,
		}
	}
	return cloned
}

func hasForwardDelivery(deliveries []domain.NewDelivery) bool {
	for _, delivery := range deliveries {
		var snapshot struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(delivery.TargetSnapshot, &snapshot) == nil && snapshot.Type == "forward" {
			return true
		}
	}
	return false
}

type acceptedResponse struct {
	EventID   string `json:"event_id"`
	Status    string `json:"status"`
	Duplicate bool   `json:"duplicate"`
}

type errorResponse struct {
	Status string `json:"status"`
}

func writeJSON(w http.ResponseWriter, status int, response any) {
	data, err := json.Marshal(response)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}
