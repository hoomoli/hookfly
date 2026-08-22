package adminapi

import (
	"net/http"
	"time"
)

func (h *handler) statistics(w http.ResponseWriter, r *http.Request) {
	if !h.storeAvailable(w) {
		return
	}
	query := r.URL.Query()
	for key, values := range query {
		if (key != "source" && key != "repository" && key != "from" && key != "to") || len(values) != 1 {
			writeError(w, 400, "invalid_query", "invalid query parameters")
			return
		}
	}
	now := time.Now().UTC()
	from, to := now.AddDate(0, 0, -7), now
	var err error
	if raw := query.Get("from"); raw != "" {
		from, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, 400, "invalid_query", "invalid time range")
			return
		}
	}
	if raw := query.Get("to"); raw != "" {
		to, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, 400, "invalid_query", "invalid time range")
			return
		}
	}
	if !from.Before(to) || to.Sub(from) > 90*24*time.Hour || to.After(now.Add(time.Minute)) {
		writeError(w, 400, "invalid_query", "invalid time range")
		return
	}
	result, err := h.dependencies.Store.PipelineStatistics(r.Context(), query.Get("source"), query.Get("repository"), from, to)
	if err != nil {
		writeError(w, 503, "unavailable", "service unavailable")
		return
	}
	writeJSON(w, 200, result)
}

// changesStream is an invalidation feed, independent of notification preferences.
// Every connection begins with a refresh so reconnects cannot miss updates.
func (h *handler) changesStream(w http.ResponseWriter, r *http.Request) {
	if !h.storeAvailable(w) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 503, "unavailable", "stream unavailable")
		return
	}
	changes, unsubscribe := h.dependencies.Store.SubscribeNotifications()
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func() bool {
		_, err := w.Write([]byte("event: change\ndata: {}\n\n"))
		flusher.Flush()
		return err == nil
	}
	if !send() {
		return
	}
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-changes:
			if !send() {
				return
			}
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
