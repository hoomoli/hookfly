// Package app wires Hookfly's process-level dependencies and HTTP servers.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/hoomoli/hookfly/internal/adminapi"
	"github.com/hoomoli/hookfly/internal/auth"
	"github.com/hoomoli/hookfly/internal/httpx"
	"github.com/hoomoli/hookfly/internal/publichook"
	"github.com/hoomoli/hookfly/internal/runtimecfg"
	"github.com/hoomoli/hookfly/internal/service"
	"github.com/hoomoli/hookfly/internal/store"
	"github.com/hoomoli/hookfly/internal/worker"
)

const (
	defaultPublicAddr = ":8080"
	defaultAdminAddr  = ":8081"
	shutdownTimeout   = 10 * time.Second
)

// App owns startup recovery, background components, and separate public/admin servers.
type App struct {
	manager *runtimecfg.Manager
	store   *store.Store
	logger  *slog.Logger

	dispatcher     *worker.Dispatcher
	poller         *worker.Poller
	drainer        *worker.Drainer
	retention      *service.Retention
	attempts       *service.Attempts
	managementAuth auth.Service

	prepareOnce sync.Once
	prepareErr  error

	PublicAddr string
	AdminAddr  string
}

// New wires the application to one atomic runtime manager.
func New(manager *runtimecfg.Manager, durableStore *store.Store, logger *slog.Logger, managementAuth auth.Service) *App {
	if logger == nil {
		logger = slog.Default()
	}
	application := &App{
		manager: manager, store: durableStore, logger: logger,
		PublicAddr: defaultPublicAddr, AdminAddr: defaultAdminAddr,
		managementAuth: managementAuth,
	}
	if application.manager != nil && durableStore != nil {
		application.dispatcher = worker.NewDispatcher(manager, durableStore, logger, time.Now)
		application.poller = worker.NewPoller(manager, durableStore, logger)
		application.drainer = worker.NewDrainer(application.manager, durableStore, logger)
		application.retention = service.NewRetention(manager, durableStore)
		application.attempts = service.NewAttempts(manager, durableStore, time.Now)
		application.dispatcher.SetTerminalHook(application.retention.RunOnce)
		application.poller.SetTerminalHook(application.retention.RunOnce)
		application.attempts.SetTerminalHook(application.retention.RunOnce)
	}
	return application
}

// Prepare finishes migration-dependent recovery before any listener or worker can run.
func (a *App) Prepare(ctx context.Context) error {
	a.prepareOnce.Do(func() {
		if a.store == nil || a.manager == nil {
			return
		}
		bindings, err := a.store.ListActiveBindings(ctx)
		if err != nil {
			a.prepareErr = err
			return
		}
		generation := a.manager.Current()
		for _, binding := range bindings {
			status := generation.BindingStatus(binding.TargetID, binding.TargetSnapshot)
			if status == runtimecfg.BindingCompatible {
				continue
			}
			if err := a.store.TerminateIncompatibleActive(ctx, binding, string(status), time.Now()); err != nil {
				a.prepareErr = err
				return
			}
		}
		if a.poller != nil {
			if err := a.poller.Recover(ctx, time.Now()); err != nil {
				a.prepareErr = err
				return
			}
		}
		a.prepareErr = a.manager.Initialize(ctx)
	})
	return a.prepareErr
}

// NewPublicMux exposes only the provider-bound webhook routes.
func NewPublicMux(gitlabHandler, githubHandler, harborHandler http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("POST /hooks/gitlab", gitlabHandler)
	mux.Handle("POST /hooks/github/{source_id}", githubHandler)
	mux.Handle("POST /hooks/harbor/{source_id}", harborHandler)
	return mux
}

func (a *App) servers() (*http.Server, *http.Server) {
	publicMux := NewPublicMux(
		publichook.New("gitlab", a.manager, a.store, time.Now),
		publichook.New("github", a.manager, a.store, time.Now),
		publichook.New("harbor", a.manager, a.store, time.Now),
	)
	adminDependencies := adminapi.Dependencies{Provider: a.managementAuth, Auth: a.managementAuth}
	if a.manager != nil {
		adminDependencies.Config = a.manager
	}
	if a.store != nil {
		adminDependencies.Store = a.store
	}
	if a.attempts != nil {
		adminDependencies.Attempts = a.attempts
	}
	adminMux := adminapi.New(adminDependencies)
	return newServer(a.PublicAddr, httpx.RequestContext(a.logger, publicMux)),
		newServer(a.AdminAddr, httpx.RequestContext(a.logger, adminMux))
}

func newServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: address, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}
}

// Run prepares recovery, then serves listeners and background components until cancellation or failure.
func (a *App) Run(ctx context.Context) error {
	if err := a.Prepare(ctx); err != nil {
		if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return nil
		}
		return err
	}
	publicServer, adminServer := a.servers()
	publicListener, err := net.Listen("tcp", publicServer.Addr)
	if err != nil {
		return err
	}
	adminListener, err := net.Listen("tcp", adminServer.Addr)
	if err != nil {
		_ = publicListener.Close()
		return err
	}

	runContext, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	componentCount := 2
	for _, present := range []bool{a.manager != nil && a.store != nil, a.dispatcher != nil, a.poller != nil, a.drainer != nil, a.retention != nil} {
		if present {
			componentCount++
		}
	}
	errorsCh := make(chan error, componentCount)
	go func() { errorsCh <- publicServer.Serve(publicListener) }()
	go func() { errorsCh <- adminServer.Serve(adminListener) }()
	if a.manager != nil && a.store != nil {
		go func() { errorsCh <- a.manager.Run(runContext) }()
	}
	if a.dispatcher != nil {
		go func() { errorsCh <- a.dispatcher.Run(runContext) }()
	}
	if a.poller != nil {
		go func() { errorsCh <- a.poller.Run(runContext) }()
	}
	if a.drainer != nil {
		go func() { errorsCh <- a.drainer.Run(runContext) }()
	}
	if a.retention != nil {
		go func() { errorsCh <- a.retention.Run(runContext) }()
	}

	received := 0
	var runErr error
	select {
	case <-ctx.Done():
	case err := <-errorsCh:
		received = 1
		runErr = componentRunError(ctx, err)
	}
	cancelRun()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutdownErr := errors.Join(publicServer.Shutdown(shutdownCtx), adminServer.Shutdown(shutdownCtx))
	if shutdownErr != nil {
		_ = publicServer.Close()
		_ = adminServer.Close()
	}
	for received < componentCount {
		err := <-errorsCh
		received++
		runErr = errors.Join(runErr, componentRunError(ctx, err))
	}
	return errors.Join(runErr, shutdownErr)
}

func componentRunError(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) || ctx.Err() != nil {
		return nil
	}
	return err
}
