package runtimecfg

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/hoomoli/hookfly/internal/config"
	"github.com/hoomoli/hookfly/internal/store"
)

// State is the reload lifecycle visible to runtime consumers and the management API.
type State string

const (
	StateIdle       State = "idle"
	StateValidating State = "validating"
	StateWaiting    State = "waiting"
	StateApplying   State = "applying"
	StateDraining   State = "draining"
)

// ReloadOutcome is the synchronous result returned by a reload request.
type ReloadOutcome string

const (
	ReloadApplied ReloadOutcome = "applied"
	ReloadWaiting ReloadOutcome = "waiting"
)

var (
	ErrReloadInProgress   = errors.New("config reload is already in progress")
	ErrInvalidConfig      = errors.New("configuration is invalid")
	ErrPreWaitUnavailable = errors.New("configuration reload is unavailable")
)

type lifecycleStore interface {
	CountActive(context.Context) (int, error)
	CountQueued(context.Context) (int, error)
}

// Options supplies the process-owned boundaries used by Manager.
type Options struct {
	Path   string
	Lookup func(string) (string, bool)
	Logger *slog.Logger
	Clock  func() time.Time
}

// LastResult is a safe lifecycle result without error text or configuration material.
type LastResult struct {
	Code string    `json:"code"`
	At   time.Time `json:"at"`
}

// Status is the complete safe management projection of the reload lifecycle.
type Status struct {
	State            State       `json:"state"`
	CurrentDigest    string      `json:"current_digest"`
	LoadedAt         time.Time   `json:"loaded_at"`
	PendingDigest    string      `json:"pending_digest,omitempty"`
	Recovery         bool        `json:"recovery,omitempty"`
	ActiveDeliveries int         `json:"active_deliveries"`
	QueuedEvents     int         `json:"queued_events"`
	LastResult       *LastResult `json:"last_result,omitempty"`
}

// Manager owns the process-local generation gate and single reload candidate.
type Manager struct {
	gate sync.RWMutex

	current       *Generation
	candidate     *Generation
	state         State
	recovery      bool
	loadedAt      time.Time
	pendingDigest string
	lastResult    *LastResult

	store  lifecycleStore
	path   string
	lookup func(string) (string, bool)
	logger *slog.Logger
	clock  func() time.Time

	loadGeneration func(string, config.EnvLookup, *slog.Logger) (*Generation, error)

	terminalWake  chan struct{}
	drainWake     chan struct{}
	pollerWake    chan struct{}
	retentionWake chan struct{}

	retryMinimum time.Duration
	retryMaximum time.Duration
}

// NewManager creates an idle manager around an already compiled initial generation.
func NewManager(initial *Generation, store lifecycleStore, options Options) *Manager {
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	loadGeneration := Load
	return &Manager{
		current: initial, state: StateIdle, loadedAt: options.Clock(),
		store: store, path: options.Path, lookup: options.Lookup, logger: options.Logger, clock: options.Clock,
		loadGeneration: loadGeneration,
		terminalWake:   make(chan struct{}, 1), drainWake: make(chan struct{}, 1),
		pollerWake: make(chan struct{}, 1), retentionWake: make(chan struct{}, 1),
		retryMinimum: 250 * time.Millisecond, retryMaximum: 5 * time.Second,
	}
}

// Read acquires the shared lifecycle gate. Unlock must be called exactly once.
func (m *Manager) Read() *ReadView {
	m.gate.RLock()
	return &ReadView{manager: m, Generation: m.current, State: m.state}
}

// ReadView is one linearized generation and lifecycle state under the shared gate.
type ReadView struct {
	manager    *Manager
	Generation *Generation
	State      State
	once       sync.Once
}

// Unlock releases the shared lifecycle gate.
func (v *ReadView) Unlock() {
	if v == nil || v.manager == nil {
		return
	}
	v.once.Do(v.manager.gate.RUnlock)
}

// Current returns the immutable current generation.
func (m *Manager) Current() *Generation {
	m.gate.RLock()
	defer m.gate.RUnlock()
	return m.current
}

// Repositories returns the current generation's canonical repository projection.
func (m *Manager) Repositories() []store.ConfiguredRepository {
	m.gate.RLock()
	defer m.gate.RUnlock()
	return m.current.Repositories()
}

// BindingStatus checks one historical delivery against the current generation.
func (m *Manager) BindingStatus(targetID string, snapshot []byte) BindingStatus {
	m.gate.RLock()
	defer m.gate.RUnlock()
	return m.current.BindingStatus(targetID, snapshot)
}

// Initialize derives the startup lifecycle before listeners and workers are exposed.
func (m *Manager) Initialize(ctx context.Context) error {
	m.gate.Lock()
	queued, err := m.store.CountQueued(ctx)
	if err != nil {
		m.gate.Unlock()
		return err
	}
	active := 0
	if queued > 0 {
		active, err = m.store.CountActive(ctx)
		if err != nil {
			m.gate.Unlock()
			return err
		}
	}
	m.candidate = nil
	m.pendingDigest = ""
	m.recovery = queued > 0
	switch {
	case queued == 0:
		m.state = StateIdle
	case active > 0:
		m.state = StateWaiting
	default:
		m.state = StateDraining
	}
	state := m.state
	m.gate.Unlock()
	if state == StateWaiting {
		m.NotifyTerminal()
	}
	if state == StateDraining {
		m.NotifyDrain()
	}
	return nil
}

// Reload reads and compiles exactly one candidate after winning the lifecycle CAS.
func (m *Manager) Reload(ctx context.Context, actor string) (ReloadOutcome, error) {
	m.gate.Lock()
	if m.state != StateIdle {
		m.gate.Unlock()
		return "", ErrReloadInProgress
	}
	m.state = StateValidating
	m.recovery = false
	m.candidate = nil
	m.pendingDigest = ""
	m.gate.Unlock()

	candidate, err := m.loadGeneration(m.path, m.lookup, m.logger)
	if err != nil {
		if reloadUnavailable(err, m.path) {
			m.failValidation("unavailable")
			return "", ErrPreWaitUnavailable
		}
		m.failValidation("invalid_config")
		return "", ErrInvalidConfig
	}

	m.gate.Lock()
	m.candidate = candidate
	m.pendingDigest = candidate.Digest()
	active, err := m.store.CountActive(ctx)
	if err != nil {
		m.candidate = nil
		m.pendingDigest = ""
		m.state = StateIdle
		m.setLastResultLocked("unavailable")
		m.gate.Unlock()
		return "", ErrPreWaitUnavailable
	}
	if active > 0 {
		m.state = StateWaiting
		m.setLastResultLocked("waiting")
		digest := m.pendingDigest
		m.gate.Unlock()
		m.logger.InfoContext(ctx, "configuration reload waiting", "actor", actor, "state", StateWaiting, "digest", digest, "active_deliveries", active)
		m.NotifyTerminal()
		return ReloadWaiting, nil
	}
	digest := m.publishCandidateLocked()
	m.gate.Unlock()
	m.afterPublish()
	m.logger.InfoContext(ctx, "configuration reload applied", "actor", actor, "state", StateDraining, "digest", digest, "active_deliveries", 0)
	return ReloadApplied, nil
}

func reloadUnavailable(err error, globalPath string) bool {
	var pathError *fs.PathError
	if errors.As(err, &pathError) {
		if errors.Is(pathError.Err, fs.ErrNotExist) {
			return filepath.Clean(pathError.Path) == filepath.Clean(globalPath)
		}
		return true
	}
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission)
}

func (m *Manager) failValidation(code string) {
	m.gate.Lock()
	m.candidate = nil
	m.pendingDigest = ""
	m.state = StateIdle
	m.setLastResultLocked(code)
	m.gate.Unlock()
}

func (m *Manager) publishCandidateLocked() string {
	m.state = StateApplying
	m.current = m.candidate
	m.candidate = nil
	m.pendingDigest = ""
	m.loadedAt = m.clock()
	m.recovery = false
	m.state = StateDraining
	m.setLastResultLocked("applied")
	return m.current.Digest()
}

func (m *Manager) afterPublish() {
	m.NotifyDrain()
	coalesce(m.pollerWake)
	coalesce(m.retentionWake)
}

// Run advances waiting reloads from terminal notifications and capped retry timers.
func (m *Manager) Run(ctx context.Context) error {
	delay := m.retryMinimum
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-m.terminalWake:
		case <-timer.C:
		}
		waiting, retryErr := m.advanceWaiting(ctx)
		if !waiting {
			delay = m.retryMinimum
			resetTimer(timer, time.Hour)
			continue
		}
		if retryErr != nil {
			delay *= 2
			if delay > m.retryMaximum {
				delay = m.retryMaximum
			}
		} else {
			delay = m.retryMaximum
		}
		resetTimer(timer, delay)
	}
}

func (m *Manager) advanceWaiting(ctx context.Context) (bool, error) {
	m.gate.Lock()
	if m.state != StateWaiting {
		m.gate.Unlock()
		return false, nil
	}
	active, err := m.store.CountActive(ctx)
	if err != nil {
		m.setLastResultLocked("waiting_retry")
		m.gate.Unlock()
		return true, err
	}
	if active > 0 {
		m.gate.Unlock()
		return true, nil
	}
	if m.candidate != nil {
		m.publishCandidateLocked()
	} else {
		m.state = StateApplying
		m.state = StateDraining
		m.setLastResultLocked("recovery_draining")
	}
	m.gate.Unlock()
	m.afterPublish()
	return false, nil
}

// SetDraining moves startup recovery into draining and kicks the level-triggered worker.
func (m *Manager) SetDraining() {
	m.gate.Lock()
	m.state = StateDraining
	m.gate.Unlock()
	m.NotifyDrain()
}

// SetIdleIfQueueEmpty atomically closes the drain/deferred-insert boundary.
func (m *Manager) SetIdleIfQueueEmpty(ctx context.Context) (bool, error) {
	m.gate.Lock()
	defer m.gate.Unlock()
	if m.state != StateDraining {
		return m.state == StateIdle, nil
	}
	queued, err := m.store.CountQueued(ctx)
	if err != nil {
		return false, err
	}
	if queued != 0 {
		return false, nil
	}
	m.state = StateIdle
	m.recovery = false
	return true, nil
}

// Status queries authoritative counts while holding the shared gate before store access.
func (m *Manager) Status(ctx context.Context) (Status, error) {
	m.gate.RLock()
	defer m.gate.RUnlock()
	active, err := m.store.CountActive(ctx)
	if err != nil {
		return Status{}, err
	}
	queued, err := m.store.CountQueued(ctx)
	if err != nil {
		return Status{}, err
	}
	status := Status{
		State: m.state, CurrentDigest: m.current.Digest(), LoadedAt: m.loadedAt,
		PendingDigest: m.pendingDigest, Recovery: m.recovery,
		ActiveDeliveries: active, QueuedEvents: queued,
	}
	if m.lastResult != nil {
		last := *m.lastResult
		status.LastResult = &last
	}
	return status, nil
}

// NotifyTerminal coalesces terminal commits after their read gate has been released.
func (m *Manager) NotifyTerminal() { coalesce(m.terminalWake) }

// NotifyDrain coalesces durable queue wakeups.
func (m *Manager) NotifyDrain() { coalesce(m.drainWake) }

// DrainWake is consumed by the single drain worker.
func (m *Manager) DrainWake() <-chan struct{} { return m.drainWake }

// PollerWake resets the poller schedule after publication.
func (m *Manager) PollerWake() <-chan struct{} { return m.pollerWake }

// RetentionWake reruns retention with the newly published limits.
func (m *Manager) RetentionWake() <-chan struct{} { return m.retentionWake }

func (m *Manager) setLastResultLocked(code string) {
	m.lastResult = &LastResult{Code: code, At: m.clock()}
}

func (m *Manager) statusState() State {
	m.gate.RLock()
	defer m.gate.RUnlock()
	return m.state
}

func (m *Manager) lastResultCode() string {
	m.gate.RLock()
	defer m.gate.RUnlock()
	if m.lastResult == nil {
		return ""
	}
	return m.lastResult.Code
}

func coalesce(channel chan struct{}) {
	select {
	case channel <- struct{}{}:
	default:
	}
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}
