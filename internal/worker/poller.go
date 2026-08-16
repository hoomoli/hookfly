package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/hoomoli/hookfly/internal/dokploy"
	"github.com/hoomoli/hookfly/internal/domain"
	"github.com/hoomoli/hookfly/internal/runtimecfg"
	"github.com/hoomoli/hookfly/internal/store"
)

// Poller reconciles durable active attempts with the compatible generation target.
type Poller struct {
	store      *store.Store
	manager    *runtimecfg.Manager
	logger     *slog.Logger
	onTerminal func(context.Context) error
}

// SetTerminalHook installs the app-owned retention trigger for terminal transitions.
func (p *Poller) SetTerminalHook(hook func(context.Context) error) { p.onTerminal = hook }

func NewPoller(manager *runtimecfg.Manager, durableStore *store.Store, logger *slog.Logger) *Poller {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Poller{store: durableStore, manager: manager, logger: logger}
}

// Recover releases crashed poll claims and converts interrupted sends to reconciliation only.
func (p *Poller) Recover(ctx context.Context, now time.Time) error {
	if p.store == nil {
		return fmt.Errorf("poller store is required")
	}
	if p.manager == nil {
		return fmt.Errorf("poller runtime manager is required")
	}
	view := p.manager.Read()
	targetSchedules := make(map[string]store.PollSchedule)
	for _, target := range view.Generation.Targets() {
		targetSchedules[target.ID] = store.PollSchedule{Interval: target.PollInterval, Timeout: target.PollTimeout}
	}
	changes, err := p.store.RecoverInterrupted(ctx, now, store.PollSchedule{
		Interval: view.Generation.PollInterval(), Timeout: view.Generation.PollTimeout(),
	}, targetSchedules)
	view.Unlock()
	if err != nil {
		return err
	}
	for _, change := range changes {
		p.logStatusChange(ctx, change)
	}
	return nil
}

// RunOnce claims and reconciles one bounded batch of due attempts.
func (p *Poller) RunOnce(ctx context.Context, now time.Time) error {
	if p.store == nil {
		return fmt.Errorf("poller store is required")
	}
	if p.manager == nil {
		return fmt.Errorf("poller runtime manager is required")
	}
	claimView := p.manager.Read()
	workItems, err := p.store.ListDuePolls(ctx, now)
	generation := claimView.Generation
	claimView.Unlock()
	if err != nil {
		return err
	}
	for _, work := range workItems {
		if err := ctx.Err(); err != nil {
			return err
		}
		target, bindingStatus := p.resolveTarget(generation, work.TargetID, work.TargetSnapshot)
		interval := generation.PollInterval()
		var match dokploy.Deployment
		var matched bool
		var unsafeCorrelation bool
		if bindingStatus == runtimecfg.BindingCompatible && target != nil {
			interval = target.PollInterval
			var lookupErr error
			if work.DeploymentID != "" || work.DeploymentCursor == nil {
				match, matched, lookupErr = target.Client.FindDeployment(ctx, target.ComposeID, work.AttemptID, work.DeploymentID)
			} else {
				var lookup dokploy.DeploymentLookup
				match, lookup, lookupErr = target.Client.FindDeploymentAfter(ctx, target.ComposeID, work.AttemptID, *work.DeploymentCursor)
				matched = lookup == dokploy.DeploymentFound
				unsafeCorrelation = lookup == dokploy.DeploymentAmbiguous || lookup == dokploy.DeploymentCursorMissing
			}
			if lookupErr != nil {
				p.logger.WarnContext(ctx, "Dokploy deployment lookup failed",
					"attempt_id", work.AttemptID, "target_id", work.TargetID, "error", lookupErr)
			}
		}
		update := pollUpdate(work, match, matched, now, interval)
		if unsafeCorrelation {
			update.DeploymentStatus = "unknown"
			update.NextPollAt = nil
		}
		commitView := p.manager.Read()
		changed, err := p.store.CompletePoll(ctx, work, update, now)
		commitView.Unlock()
		if errors.Is(err, store.ErrPollNotClaimed) {
			continue
		}
		if err != nil {
			return err
		}
		if changed {
			p.logStatusChange(ctx, store.StatusChange{
				AttemptID: work.AttemptID, TargetID: work.TargetID,
				BeforeTransport: work.TransportStatus, BeforeDeployment: work.DeploymentStatus,
				AfterTransport: update.TransportStatus, AfterDeployment: update.DeploymentStatus,
			})
			if !domain.Active(domain.TransportStatus(update.TransportStatus), domain.DeploymentStatus(update.DeploymentStatus)) {
				p.manager.NotifyTerminal()
				if p.onTerminal != nil {
					if err := p.onTerminal(ctx); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (p *Poller) resolveTarget(generation *runtimecfg.Generation, targetID string, snapshot []byte) (*runtimecfg.Target, runtimecfg.BindingStatus) {
	return generation.ResolveTarget(targetID, snapshot)
}

// Run immediately polls and resets its timer on every generation publication.
func (p *Poller) Run(ctx context.Context) error {
	for {
		if err := p.RunOnce(ctx, time.Now()); err != nil {
			if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				return nil
			}
			return err
		}
		interval := p.currentInterval()
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-p.manager.PollerWake():
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}

func (p *Poller) currentInterval() time.Duration {
	if p.manager == nil {
		return time.Second
	}
	view := p.manager.Read()
	defer view.Unlock()
	return view.Generation.PollInterval()
}

func normalizedPollInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return time.Second
	}
	if interval < time.Millisecond {
		return time.Millisecond
	}
	return interval
}

func pollUpdate(work store.PollWork, match dokploy.Deployment, matched bool, now time.Time, interval time.Duration) store.PollUpdate {
	update := store.PollUpdate{TransportStatus: work.TransportStatus, DeploymentStatus: work.DeploymentStatus}
	deadlineReached := !now.Before(work.MonitoringDeadline)
	if matched {
		update.TransportStatus = "enqueued"
		update.DeploymentID = match.DeploymentID
		status := ""
		if match.Status != nil {
			status = *match.Status
		}
		switch status {
		case "done", "error", "cancelled":
			update.DeploymentStatus = status
			return update
		case "running":
			update.DeploymentStatus = "running"
		default:
			update.DeploymentStatus = "unrecognized"
		}
		if deadlineReached {
			update.DeploymentStatus = "timeout"
			return update
		}
		next := now.Add(interval)
		update.NextPollAt = &next
		return update
	}
	if deadlineReached {
		if work.DeploymentID != "" {
			update.DeploymentStatus = "timeout"
		} else {
			update.DeploymentStatus = "unknown"
		}
		return update
	}
	next := now.Add(interval)
	update.NextPollAt = &next
	return update
}

func (p *Poller) logStatusChange(ctx context.Context, change store.StatusChange) {
	p.logger.InfoContext(ctx, "delivery status changed",
		"attempt_id", change.AttemptID, "target_id", change.TargetID,
		"before_transport", change.BeforeTransport, "before_deployment", change.BeforeDeployment,
		"after_transport", change.AfterTransport, "after_deployment", change.AfterDeployment)
}
