package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hoomoli/hookfly/internal/app"
	"github.com/hoomoli/hookfly/internal/auth"
	"github.com/hoomoli/hookfly/internal/runtimecfg"
	"github.com/hoomoli/hookfly/internal/store"
)

const (
	defaultConfigPath  = "hookfly.yaml"
	defaultDBPath      = "hookfly.db"
	defaultPublicAddr  = ":8080"
	defaultAdminAddr   = ":8081"
	healthcheckTimeout = 5 * time.Second
	oidcStartupTimeout = 10 * time.Second
)

type oidcServiceFactory func(context.Context, auth.Settings, *slog.Logger) (auth.Service, error)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	handled, err := dispatchCommand(os.Args[1:], healthcheckTimeout)
	if !handled {
		err = run(logger)
	}
	if err != nil {
		logger.Error("hookfly stopped", "error", err)
		os.Exit(1)
	}
}

func dispatchCommand(args []string, timeout time.Duration) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	if args[0] != "healthcheck" {
		return true, fmt.Errorf("unknown command %q", args[0])
	}

	flags := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	url := flags.String("url", "", "admin health endpoint URL")
	if err := flags.Parse(args[1:]); err != nil {
		return true, err
	}
	if *url == "" || flags.NArg() != 0 {
		return true, fmt.Errorf("healthcheck requires --url and no positional arguments")
	}
	return true, checkHealth(*url, timeout)
}

func checkHealth(url string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create health request: %w", err)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request health endpoint: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned %s", response.Status)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return fmt.Errorf("decode health response: %w", err)
	}
	if body.Status != "ok" {
		return fmt.Errorf("health endpoint status is %q", body.Status)
	}
	return nil
}

func run(logger *slog.Logger) error {
	managementAuth, err := buildManagementAuth(context.Background(), os.LookupEnv, logger)
	if err != nil {
		return fmt.Errorf("configure management authentication: %w", err)
	}
	configPath := envOrDefault("HOOKFLY_CONFIG", defaultConfigPath)
	generation, err := runtimecfg.Load(configPath, os.LookupEnv, logger)
	if err != nil {
		return fmt.Errorf("compile config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	database, err := store.Open(ctx, envOrDefault("HOOKFLY_DB", defaultDBPath))
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer database.Close()

	manager := runtimecfg.NewManager(generation, database, runtimecfg.Options{
		Path: configPath, Lookup: os.LookupEnv, Logger: logger,
	})
	application := app.New(manager, database, logger, managementAuth)
	application.PublicAddr = envOrDefault("HOOKFLY_PUBLIC_ADDR", defaultPublicAddr)
	application.AdminAddr = envOrDefault("HOOKFLY_ADMIN_ADDR", defaultAdminAddr)
	if err := application.Prepare(ctx); err != nil {
		return fmt.Errorf("prepare app: %w", err)
	}
	return application.Run(ctx)
}

func buildManagementAuth(ctx context.Context, lookup func(string) (string, bool), logger *slog.Logger) (auth.Service, error) {
	return buildManagementAuthWithOIDC(ctx, lookup, logger, func(ctx context.Context, settings auth.Settings, logger *slog.Logger) (auth.Service, error) {
		return auth.NewOIDCService(ctx, settings, logger)
	})
}

func buildManagementAuthWithOIDC(ctx context.Context, lookup func(string) (string, bool), logger *slog.Logger, newOIDCService oidcServiceFactory) (auth.Service, error) {
	settings, err := auth.LoadSettings(lookup)
	if err != nil {
		return nil, err
	}
	if settings.Mode == auth.ModeNone {
		logger.Warn("management authentication is disabled by explicit local development configuration")
		return auth.NewDevelopmentService(), nil
	}
	discoveryContext, cancel := context.WithTimeout(ctx, oidcStartupTimeout)
	defer cancel()
	return newOIDCService(discoveryContext, settings, logger)
}

func envOrDefault(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok && value != "" {
		return value
	}
	return fallback
}
