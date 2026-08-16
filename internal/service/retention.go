package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hoomoli/hookfly/internal/runtimecfg"
	"github.com/hoomoli/hookfly/internal/store"
)

const retentionInterval = time.Hour

// Retention reads generation limits under the lifecycle gate for every prune transaction.
type Retention struct {
	store   *store.Store
	manager *runtimecfg.Manager
}

func NewRetention(manager *runtimecfg.Manager, durableStore *store.Store) *Retention {
	return &Retention{store: durableStore, manager: manager}
}

// RunOnce holds the read gate from generation limit selection through prune commit.
func (r *Retention) RunOnce(ctx context.Context) error {
	if r.store == nil {
		return fmt.Errorf("retention store is required")
	}
	if r.manager == nil {
		return fmt.Errorf("retention runtime manager is required")
	}
	view := r.manager.Read()
	repositoryLimits, defaultLimit, globalLimit := view.Generation.RetentionLimits()
	_, err := r.store.Prune(ctx, repositoryLimits, defaultLimit, globalLimit)
	view.Unlock()
	return err
}

// Run prunes on startup, publication wake, and then hourly until cancellation.
func (r *Retention) Run(ctx context.Context) error {
	for {
		if err := r.RunOnce(ctx); err != nil {
			if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				return nil
			}
			return err
		}
		timer := time.NewTimer(retentionInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-r.manager.RetentionWake():
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}
