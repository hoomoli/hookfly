// Package worker dispatches durable delivery attempts to configured deployment targets.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/hoomoli/hookfly/internal/dokploy"
	"github.com/hoomoli/hookfly/internal/httptarget"
	"github.com/hoomoli/hookfly/internal/redact"
	"github.com/hoomoli/hookfly/internal/runtimecfg"
	"github.com/hoomoli/hookfly/internal/store"
)

const (
	dispatchInterval         = time.Second
	snapshotLimit            = 64 << 10
	defaultTransitionTimeout = 5 * time.Second
)

// Dispatcher claims durable attempts under the generation gate and never retries remote POSTs.
type Dispatcher struct {
	store             *store.Store
	manager           *runtimecfg.Manager
	clock             func() time.Time
	transitionTimeout time.Duration
	onTerminal        func(context.Context) error
}

// SetTerminalHook installs the app-owned retention trigger for terminal transitions.
func (d *Dispatcher) SetTerminalHook(hook func(context.Context) error) { d.onTerminal = hook }

func NewDispatcher(manager *runtimecfg.Manager, durableStore *store.Store, logger *slog.Logger, clock func() time.Time) *Dispatcher {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if clock == nil {
		clock = time.Now
	}
	return &Dispatcher{
		store: durableStore, manager: manager,
		clock: clock, transitionTimeout: defaultTransitionTimeout,
	}
}

// RunOnce claims and resolves at most one current pending attempt. HTTP outcomes are persisted, not retried.
func (d *Dispatcher) RunOnce(ctx context.Context) (bool, error) {
	if d.store == nil {
		return false, fmt.Errorf("dispatcher store is required")
	}
	if d.manager == nil {
		return false, fmt.Errorf("dispatcher runtime manager is required")
	}
	view := d.manager.Read()
	work, claimed, err := d.store.ClaimPendingAttempt(ctx, d.clock())
	if err != nil || !claimed {
		view.Unlock()
		return false, err
	}
	target, bindingStatus := d.resolveTarget(view.Generation, work.TargetID, work.TargetSnapshot)
	view.Unlock()
	if bindingStatus != runtimecfg.BindingCompatible || target == nil {
		responseJSON := boundedSnapshot(map[string]any{"kind": "definitive", "error": string(bindingStatus)})
		transitionContext, cancelTransition := d.newTransitionContext(ctx)
		defer cancelTransition()
		commitView := d.manager.Read()
		err := d.store.MarkTransportFailed(transitionContext, work.AttemptID, nil, responseJSON, d.clock())
		commitView.Unlock()
		if err != nil {
			return true, err
		}
		return true, d.afterTerminal(transitionContext)
	}

	if target.Type == "http" {
		return d.dispatchHTTP(ctx, work, target)
	}
	request := dokploy.DeployRequest{ComposeID: target.ComposeID, Description: "attempt_id=" + work.AttemptID}
	var requestJSON []byte
	cursor, _, cursorErr := target.Client.LatestDeploymentID(ctx, target.ComposeID)
	if cursorErr != nil {
		responseJSON := boundedSnapshot(map[string]any{"kind": "definitive", "error": cursorErr.Error()})
		transitionContext, cancelTransition := d.newTransitionContext(ctx)
		defer cancelTransition()
		commitView := d.manager.Read()
		err := d.store.MarkTransportFailed(transitionContext, work.AttemptID, requestJSON, responseJSON, d.clock())
		commitView.Unlock()
		if err != nil {
			return true, err
		}
		return true, d.afterTerminal(transitionContext)
	}
	transitionContext, cancelTransition := d.newTransitionContext(ctx)
	if err := d.store.SetDeploymentCursor(transitionContext, work.AttemptID, cursor); err != nil {
		cancelTransition()
		return true, err
	}
	cancelTransition()
	var recordErr error
	response, deployErr := target.Client.DeployWithRequestEvidence(ctx, request, func(evidence []byte) error {
		if evidence == nil {
			requestJSON = []byte(`{"redacted":true}`)
		} else {
			requestJSON = boundedSnapshot(json.RawMessage(evidence))
		}
		recordContext, cancelRecord := d.newTransitionContext(ctx)
		defer cancelRecord()
		recordErr = d.store.RecordDeploymentRequest(recordContext, work.AttemptID, requestJSON, d.clock())
		return recordErr
	})
	if recordErr != nil {
		return true, recordErr
	}
	at := d.clock()
	responseJSON := responseSnapshot(response, deployErr)
	transitionContext, cancelTransition = d.newTransitionContext(ctx)
	defer cancelTransition()
	commitView := d.manager.Read()
	if deployErr == nil {
		err := d.store.MarkEnqueued(
			transitionContext, work.AttemptID, requestJSON, responseJSON, at,
			at.Add(target.PollInterval), at.Add(target.PollTimeout),
		)
		commitView.Unlock()
		return true, err
	}
	var transportError *dokploy.TransportError
	if errors.As(deployErr, &transportError) && transportError.Kind == dokploy.TransportUncertain {
		err := d.store.MarkTransportUnknown(
			transitionContext, work.AttemptID, requestJSON, responseJSON, at,
			at.Add(target.PollInterval), at.Add(target.PollTimeout),
		)
		commitView.Unlock()
		return true, err
	}
	err = d.store.MarkTransportFailed(transitionContext, work.AttemptID, requestJSON, responseJSON, at)
	commitView.Unlock()
	if err != nil {
		return true, err
	}
	return true, d.afterTerminal(transitionContext)
}

func (d *Dispatcher) dispatchHTTP(ctx context.Context, work store.PendingAttempt, target *runtimecfg.Target) (bool, error) {
	if target == nil || target.HTTP == nil || target.HTTP.Client == nil {
		transitionContext, cancelTransition := d.newTransitionContext(ctx)
		defer cancelTransition()
		commitView := d.manager.Read()
		err := d.store.MarkTransportFailed(transitionContext, work.AttemptID, nil, boundedSnapshot(map[string]any{"kind": "definitive", "error": "HTTP target is unavailable"}), d.clock())
		commitView.Unlock()
		if err != nil {
			return true, err
		}
		return true, d.afterTerminal(transitionContext)
	}
	values := httptarget.Values{
		EventSource: work.Event.Source, EventRepository: work.Event.Repository, EventRef: work.Event.Ref,
		EventRevision: work.Event.Revision, EventStatus: work.Event.Status, EventExternalID: work.Event.ExternalID,
		EventTrigger: work.Event.Trigger, EventCommitMessage: work.Event.CommitMessage, AttemptID: work.AttemptID, DeliveryID: work.DeliveryID, TargetID: target.ID,
	}
	var requestJSON []byte
	var recordErr error
	response, dispatchErr := target.HTTP.Client.Dispatch(ctx, target.HTTP.Request, values, func(evidence []byte) error {
		requestJSON = boundedSnapshot(json.RawMessage(evidence))
		recordContext, cancelRecord := d.newTransitionContext(ctx)
		defer cancelRecord()
		recordErr = d.store.RecordDeploymentRequest(recordContext, work.AttemptID, requestJSON, d.clock())
		return recordErr
	})
	if recordErr != nil {
		return true, recordErr
	}
	at := d.clock()
	responseJSON := httpResponseSnapshot(response, dispatchErr)
	transitionContext, cancelTransition := d.newTransitionContext(ctx)
	defer cancelTransition()
	commitView := d.manager.Read()
	if dispatchErr == nil {
		err := d.store.MarkAccepted(transitionContext, work.AttemptID, requestJSON, responseJSON, at)
		commitView.Unlock()
		if err != nil {
			return true, err
		}
		return true, d.afterTerminal(transitionContext)
	}
	var transportError *httptarget.TransportError
	if errors.As(dispatchErr, &transportError) && transportError.Kind == httptarget.TransportUncertain {
		err := d.store.MarkTransportUnknownTerminal(transitionContext, work.AttemptID, requestJSON, responseJSON, at)
		commitView.Unlock()
		if err != nil {
			return true, err
		}
		return true, d.afterTerminal(transitionContext)
	}
	err := d.store.MarkTransportFailed(transitionContext, work.AttemptID, requestJSON, responseJSON, at)
	commitView.Unlock()
	if err != nil {
		return true, err
	}
	return true, d.afterTerminal(transitionContext)
}

func (d *Dispatcher) resolveTarget(generation *runtimecfg.Generation, targetID string, snapshot []byte) (*runtimecfg.Target, runtimecfg.BindingStatus) {
	return generation.ResolveTarget(targetID, snapshot)
}

func (d *Dispatcher) afterTerminal(ctx context.Context) error {
	d.manager.NotifyTerminal()
	if d.onTerminal == nil {
		return nil
	}
	return d.onTerminal(ctx)
}

func (d *Dispatcher) newTransitionContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), d.transitionTimeout)
}

// Run performs one bounded dispatch on startup and then one per tick until cancellation.
func (d *Dispatcher) Run(ctx context.Context) error {
	return runDispatchLoop(ctx, dispatchInterval, d.RunOnce)
}

func runDispatchLoop(ctx context.Context, interval time.Duration, runOnce func(context.Context) (bool, error)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if _, err := runOnce(ctx); err != nil {
			if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func boundedSnapshot(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		data = []byte(`{"error":"snapshot encoding failed"}`)
	}
	output, _ := redact.JSON(data, snapshotLimit)
	return output
}

func responseSnapshot(response dokploy.DeployResponse, deployErr error) []byte {
	var body any
	if len(response.Body) > 0 {
		if err := json.Unmarshal(response.Body, &body); err != nil {
			body = string(response.Body)
		}
	}
	value := map[string]any{"status_code": response.StatusCode, "truncated": response.Truncated}
	if body != nil {
		value["body"] = body
	}
	if deployErr != nil {
		value["error"] = deployErr.Error()
		var transportError *dokploy.TransportError
		if errors.As(deployErr, &transportError) {
			value["kind"] = transportError.Kind
		}
	}
	return boundedSnapshot(value)
}

func httpResponseSnapshot(response httptarget.Response, dispatchErr error) []byte {
	var body any
	if len(response.Body) > 0 {
		if err := json.Unmarshal(response.Body, &body); err != nil {
			body = string(response.Body)
		}
	}
	value := map[string]any{"status_code": response.StatusCode, "truncated": response.Truncated}
	if body != nil {
		value["body"] = body
	}
	if dispatchErr != nil {
		value["error"] = dispatchErr.Error()
		var transportError *httptarget.TransportError
		if errors.As(dispatchErr, &transportError) {
			value["kind"] = transportError.Kind
		}
	}
	return boundedSnapshot(value)
}
