// Package httpx contains shared HTTP middleware.
package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type requestIDKey struct{}

// RequestID returns the request ID injected by RequestContext.
func RequestID(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDKey{}).(string)
	return requestID
}

// RequestContext injects a UUID request ID and emits one metadata-only access record.
func RequestContext(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := uuid.NewString()
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, requestID))
		writer := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if recovered := recover(); recovered != nil {
				if !writer.wroteHeader {
					writer.status = http.StatusInternalServerError
				}
				logAccess(logger, r, requestID, writer.status, started)
				panic(recovered)
			}
			logAccess(logger, r, requestID, writer.status, started)
		}()

		next.ServeHTTP(writer, r)
	})
}

func logAccess(logger *slog.Logger, r *http.Request, requestID string, status int, started time.Time) {
	logger.InfoContext(r.Context(), "http request",
		"request_id", requestID,
		"method", r.Method,
		"path", r.URL.Path,
		"status", status,
		"duration_ms", float64(time.Since(started).Microseconds())/1000,
	)
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
