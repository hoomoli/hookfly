package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/hoomoli/hookfly/internal/runtimecfg"
	"github.com/hoomoli/hookfly/internal/store"
)

const (
	drainRetryMinimum = 100 * time.Millisecond
	drainRetryMaximum = 5 * time.Second
	drainLevelCheck   = time.Second
)

// Drainer finalizes durable deferred events in acceptance order without external calls.
type Drainer struct {
	manager *runtimecfg.Manager
	store   *store.Store
	logger  *slog.Logger
}

// NewDrainer creates the process's single deferred-routing worker.
func NewDrainer(manager *runtimecfg.Manager, durableStore *store.Store, logger *slog.Logger) *Drainer {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Drainer{manager: manager, store: durableStore, logger: logger}
}

// RunOnce finalizes at most one queue head or atomically returns draining to idle.
func (d *Drainer) RunOnce(ctx context.Context) (bool, error) {
	if d.manager == nil || d.store == nil {
		return false, errors.New("drainer dependencies are required")
	}
	view := d.manager.Read()
	if view.State != runtimecfg.StateDraining {
		view.Unlock()
		return false, nil
	}
	deferred, found, err := d.store.PeekDeferred(ctx)
	if err != nil {
		view.Unlock()
		return false, err
	}
	if !found {
		view.Unlock()
		_, err := d.manager.SetIdleIfQueueEmpty(ctx)
		return false, err
	}
	decision, err := view.Generation.Route(deferred.CanonicalEvent)
	if err != nil {
		view.Unlock()
		return false, err
	}
	changed, err := d.store.FinalizeDeferred(ctx, deferred, decision, view.Generation.Digest())
	view.Unlock()
	return changed, err
}

// Run is level-triggered on startup, wake notifications, and bounded retry expiry.
func (d *Drainer) Run(ctx context.Context) error {
	retry := drainRetryMinimum
	for {
		processed, err := d.RunOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err == nil && processed {
			retry = drainRetryMinimum
			continue
		}
		wait := drainLevelCheck
		if err != nil {
			d.logger.WarnContext(ctx, "deferred routing finalization failed", "error_code", "routing_store_unavailable")
			wait = retry
			retry *= 2
			if retry > drainRetryMaximum {
				retry = drainRetryMaximum
			}
		} else {
			retry = drainRetryMinimum
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-d.manager.DrainWake():
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}
