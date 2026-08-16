// Package service contains application orchestration over durable state and runtime generations.
package service

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/hoomoli/hookfly/internal/domain"
	"github.com/hoomoli/hookfly/internal/runtimecfg"
	"github.com/hoomoli/hookfly/internal/store"
)

var (
	ErrOperationNotAllowed       = errors.New("manual operation is not allowed for this attempt")
	ErrDeploymentStillActive     = errors.New("deployment is still active")
	ErrAlreadySucceeded          = errors.New("deployment already succeeded")
	ErrStateChanged              = errors.New("delivery state changed")
	ErrReconciliationUnavailable = errors.New("deployment reconciliation is unavailable")
	ErrConfigReloadInProgress    = errors.New("configuration reload is in progress")
	ErrTargetChanged             = errors.New("historical target binding changed")
	ErrTargetUnavailable         = errors.New("historical target binding is unavailable")
)

// AttemptResult identifies the current attempt after a manual operation or reconciliation.
type AttemptResult struct {
	AttemptID string
	Created   bool
}

// Attempts performs manual operations under one generation read gate.
type Attempts struct {
	store      *store.Store
	manager    *runtimecfg.Manager
	clock      func() time.Time
	onTerminal func(context.Context) error
}

// SetTerminalHook installs the app-owned retention trigger for corrected terminal states.
func (s *Attempts) SetTerminalHook(hook func(context.Context) error) { s.onTerminal = hook }

func NewAttempts(manager *runtimecfg.Manager, durableStore *store.Store, clock func() time.Time) *Attempts {
	if clock == nil {
		clock = time.Now
	}
	return &Attempts{store: durableStore, manager: manager, clock: clock}
}

// Execute checks lifecycle and binding before any state decision, remote request, or CAS write.
func (s *Attempts) Execute(ctx context.Context, deliveryID, expectedCurrentAttemptID string, operation domain.Operation, actorID, reason string) (AttemptResult, error) {
	if s.store == nil || s.manager == nil {
		return AttemptResult{}, ErrStateChanged
	}
	view := s.manager.Read()
	if view.State != runtimecfg.StateIdle {
		view.Unlock()
		return AttemptResult{}, ErrConfigReloadInProgress
	}
	current, err := s.store.CurrentAttempt(ctx, deliveryID)
	if err != nil {
		view.Unlock()
		if errors.Is(err, sql.ErrNoRows) {
			return AttemptResult{}, ErrStateChanged
		}
		return AttemptResult{}, err
	}
	if current.AttemptID != expectedCurrentAttemptID {
		view.Unlock()
		return AttemptResult{}, ErrStateChanged
	}
	target, bindingStatus := s.resolveTarget(view.Generation, current.TargetID, current.TargetSnapshot)
	if bindingStatus != runtimecfg.BindingCompatible || target == nil {
		view.Unlock()
		if bindingStatus == runtimecfg.BindingChanged {
			return AttemptResult{AttemptID: current.AttemptID}, ErrTargetChanged
		}
		return AttemptResult{AttemptID: current.AttemptID}, ErrTargetUnavailable
	}
	if domain.Active(current.TransportStatus, current.DeploymentStatus) &&
		current.TransportStatus != domain.TransportUnknown && current.DeploymentStatus != domain.DeploymentUnknown {
		view.Unlock()
		return AttemptResult{AttemptID: current.AttemptID}, ErrDeploymentStillActive
	}
	if current.DeploymentStatus == domain.DeploymentDone {
		view.Unlock()
		return AttemptResult{AttemptID: current.AttemptID}, ErrAlreadySucceeded
	}
	if !operationAllowed(current, operation) {
		view.Unlock()
		return AttemptResult{AttemptID: current.AttemptID}, ErrOperationNotAllowed
	}
	if needsReconciliation(current) && target == nil {
		view.Unlock()
		return AttemptResult{AttemptID: current.AttemptID}, ErrReconciliationUnavailable
	}

	var result AttemptResult
	terminalCommitted := false
	if !needsReconciliation(current) {
		result, err = s.createManual(ctx, current, operation, actorID, reason)
	} else {
		result, err, terminalCommitted = s.reconcileThenExecute(ctx, target, current, operation, actorID, reason)
	}
	view.Unlock()
	if terminalCommitted {
		s.manager.NotifyTerminal()
		if s.onTerminal != nil {
			if hookErr := s.onTerminal(ctx); hookErr != nil {
				return result, hookErr
			}
		}
	}
	return result, err
}

func (s *Attempts) resolveTarget(generation *runtimecfg.Generation, targetID string, snapshot []byte) (*runtimecfg.Target, runtimecfg.BindingStatus) {
	return generation.ResolveTarget(targetID, snapshot)
}

func operationAllowed(current store.CurrentAttempt, operation domain.Operation) bool {
	for _, allowed := range domain.AllowedOperations(current.TransportStatus, current.DeploymentStatus) {
		if operation == allowed {
			return true
		}
	}
	return false
}

func needsReconciliation(current store.CurrentAttempt) bool {
	return current.TransportStatus == domain.TransportUnknown ||
		current.DeploymentStatus == domain.DeploymentUnknown ||
		current.DeploymentStatus == domain.DeploymentError ||
		current.DeploymentStatus == domain.DeploymentCancelled ||
		current.DeploymentStatus == domain.DeploymentTimeout
}

func (s *Attempts) createManual(ctx context.Context, current store.CurrentAttempt, operation domain.Operation, actorID, reason string) (AttemptResult, error) {
	attemptID, err := s.store.ApplyManualUpdate(ctx, store.ManualUpdate{
		Expected: current, Create: true, Operation: operation, ActorID: actorID, Reason: reason, At: s.clock(),
	})
	if errors.Is(err, store.ErrCurrentAttemptChanged) {
		return AttemptResult{}, ErrStateChanged
	}
	if err != nil {
		return AttemptResult{}, err
	}
	return AttemptResult{AttemptID: attemptID, Created: true}, nil
}

func (s *Attempts) reconcileThenExecute(ctx context.Context, target *runtimecfg.Target, current store.CurrentAttempt, operation domain.Operation, actorID, reason string) (AttemptResult, error, bool) {
	match, found, err := target.Client.FindDeployment(ctx, target.ComposeID, current.AttemptID, current.DeploymentID)
	if err != nil {
		return AttemptResult{AttemptID: current.AttemptID}, ErrReconciliationUnavailable, false
	}
	if !found {
		result, err := s.createManual(ctx, current, operation, actorID, reason)
		return result, err, false
	}

	now := s.clock()
	status := ""
	if match.Status != nil {
		status = *match.Status
	}
	update := store.ManualUpdate{
		Expected: current, Correct: true, ActorID: actorID, Reason: reason, At: now,
		TransportStatus: domain.TransportEnqueued, DeploymentID: match.DeploymentID,
	}
	switch status {
	case "done":
		update.DeploymentStatus = domain.DeploymentDone
		result, err := s.correctAndReturn(ctx, update, ErrAlreadySucceeded)
		return result, err, true
	case "error":
		update.DeploymentStatus = domain.DeploymentError
	case "cancelled":
		update.DeploymentStatus = domain.DeploymentCancelled
	case "running":
		update.DeploymentStatus = domain.DeploymentRunning
		pollDue, deadline := now.Add(target.PollInterval), now.Add(target.PollTimeout)
		update.PollDue, update.MonitoringDeadline = &pollDue, &deadline
		result, err := s.correctAndReturn(ctx, update, ErrDeploymentStillActive)
		return result, err, false
	default:
		update.DeploymentStatus = domain.DeploymentUnrecognized
		pollDue, deadline := now.Add(target.PollInterval), now.Add(target.PollTimeout)
		update.PollDue, update.MonitoringDeadline = &pollDue, &deadline
		result, err := s.correctAndReturn(ctx, update, ErrDeploymentStillActive)
		return result, err, false
	}
	if operation == domain.OperationRetry {
		result, err := s.correctAndReturn(ctx, update, ErrStateChanged)
		return result, err, true
	}
	update.Create = true
	update.Operation = operation
	attemptID, err := s.store.ApplyManualUpdate(ctx, update)
	if errors.Is(err, store.ErrCurrentAttemptChanged) {
		return AttemptResult{}, ErrStateChanged, false
	}
	if err != nil {
		return AttemptResult{}, err, false
	}
	return AttemptResult{AttemptID: attemptID, Created: true}, nil, false
}

func (s *Attempts) correctAndReturn(ctx context.Context, update store.ManualUpdate, terminal error) (AttemptResult, error) {
	attemptID, err := s.store.ApplyManualUpdate(ctx, update)
	if errors.Is(err, store.ErrCurrentAttemptChanged) {
		return AttemptResult{}, ErrStateChanged
	}
	if err != nil {
		return AttemptResult{}, err
	}
	return AttemptResult{AttemptID: attemptID}, terminal
}
