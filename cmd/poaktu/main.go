package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Adopten123/digital-ruble-deals/internal/core/config"
	"github.com/Adopten123/digital-ruble-deals/internal/core/documentfile"
	corepg "github.com/Adopten123/digital-ruble-deals/internal/core/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	httpapi "github.com/Adopten123/digital-ruble-deals/internal/core/transport/http"
	auditrepo "github.com/Adopten123/digital-ruble-deals/internal/features/audit/repository/postgres"
	auditservice "github.com/Adopten123/digital-ruble-deals/internal/features/audit/service"
	audithttp "github.com/Adopten123/digital-ruble-deals/internal/features/audit/transport/http"
	authrepo "github.com/Adopten123/digital-ruble-deals/internal/features/auth/repository/postgres"
	authservice "github.com/Adopten123/digital-ruble-deals/internal/features/auth/service"
	authhttp "github.com/Adopten123/digital-ruble-deals/internal/features/auth/transport/http"
	"github.com/Adopten123/digital-ruble-deals/internal/features/bootstrap"
	dealrepo "github.com/Adopten123/digital-ruble-deals/internal/features/deals/repository/postgres"
	dealservice "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	dealhttp "github.com/Adopten123/digital-ruble-deals/internal/features/deals/transport/http"
	edorepo "github.com/Adopten123/digital-ruble-deals/internal/features/edo/repository/postgres"
	edoservice "github.com/Adopten123/digital-ruble-deals/internal/features/edo/service"
	edohttp "github.com/Adopten123/digital-ruble-deals/internal/features/edo/transport/http"
	orgrepo "github.com/Adopten123/digital-ruble-deals/internal/features/organizations/repository/postgres"
	orgservice "github.com/Adopten123/digital-ruble-deals/internal/features/organizations/service"
	orghttp "github.com/Adopten123/digital-ruble-deals/internal/features/organizations/transport/http"
	settlementrepo "github.com/Adopten123/digital-ruble-deals/internal/features/settlement/repository/postgres"
	settlementservice "github.com/Adopten123/digital-ruble-deals/internal/features/settlement/service"
	settlementhttp "github.com/Adopten123/digital-ruble-deals/internal/features/settlement/transport/http"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger, os.Args[1:]); err != nil {
		logger.Error("command failed", "error", err.Error())
		os.Exit(1)
	}
}
func run(logger *slog.Logger, args []string) error {
	command := "serve"
	if len(args) > 0 {
		command = args[0]
		args = args[1:]
	}
	if command == "healthcheck" {
		return healthcheck(args)
	}
	if len(args) > 0 {
		return errors.New("unexpected command arguments")
	}
	if command != "serve" && command != "migrate" && command != "bootstrap" {
		return errors.New("usage: poaktu [serve|migrate|bootstrap|healthcheck]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return errors.New("invalid DATABASE_URL")
	}
	poolConfig.MaxConns = 10
	poolConfig.MinConns = 1
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	poolConfig.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return errors.New("cannot initialize PostgreSQL pool")
	}
	defer pool.Close()
	connectCtx, connectCancel := context.WithTimeout(ctx, 10*time.Second)
	err = pool.Ping(connectCtx)
	connectCancel()
	if err != nil {
		return errors.New("PostgreSQL is unavailable")
	}
	switch command {
	case "migrate":
		if err := corepg.Migrate(ctx, corepg.UnitOfWork{Pool: pool}, cfg.MigrationsDir); err != nil {
			return safeCommandError("migration", err)
		}
		logger.Info("migrations completed")
		return nil
	case "bootstrap":
		if _, err := security.LoadOrCreateKey(cfg.KeyFile); err != nil {
			return errors.New("cannot initialize persistent app key")
		}
		if err := bootstrap.Run(ctx, pool); err != nil {
			return safeCommandError("bootstrap", err)
		}
		logger.Info("test bootstrap completed")
		return nil
	default:
		return serve(ctx, logger, cfg, pool)
	}
}
func safeCommandError(operation string, err error) error {
	if errors.Is(err, corepg.ErrCommitUnknown) {
		return fmt.Errorf("%s commit outcome is unknown; inspect database state before retry", operation)
	}
	return fmt.Errorf("%s failed: %w", operation, err)
}

func serve(ctx context.Context, logger *slog.Logger, cfg config.Config, pool *pgxpool.Pool) error {
	key, err := security.LoadKey(cfg.KeyFile)
	if err != nil {
		return errors.New("persistent app key is missing or invalid; run bootstrap")
	}
	if err := bootstrap.Ready(ctx, pool); err != nil {
		return errors.New("schema or bootstrap not ready; run migrate and bootstrap")
	}
	authService, err := authservice.New(authrepo.New(pool))
	if err != nil {
		return errors.New("cannot initialize password service")
	}
	csrf := httpapi.NewCSRF(key, cfg.AppOrigin, cfg.SecureCookies)
	authHandler := authhttp.New(authService, csrf)
	organizationService := orgservice.New(orgrepo.New(pool), key)
	files, err := documentfile.NewStore(cfg.DocumentsDir)
	if err != nil {
		return errors.New("cannot initialize private document storage")
	}
	defer files.Close()
	deliveryURL, err := internalEDOURL(cfg.HTTPAddr)
	if err != nil {
		return err
	}
	edoService := edoservice.New(edorepo.New(pool), key)
	sender, err := edohttp.NewSender(deliveryURL)
	if err != nil {
		return errors.New("cannot initialize internal EDO transport")
	}
	worker := edoservice.NewWorker(edoService, sender)
	mux := http.NewServeMux()
	authHandler.RegisterRoutes(mux)
	orghttp.New(organizationService).RegisterRoutes(mux, authHandler.Require)
	dealhttp.New(dealservice.New(dealrepo.New(pool), key, files), csrf).RegisterRoutes(mux, authHandler.Require)
	settlementhttp.New(settlementservice.New(settlementrepo.New(pool), key), csrf).RegisterRoutes(mux, authHandler.Require)
	edohttp.New(edoService, csrf).RegisterRoutes(mux, authHandler.Require)
	audithttp.New(auditservice.New(auditrepo.New(pool), key)).RegisterRoutes(mux, authHandler.Require)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpapi.JSON(w, 200, map[string]string{"status": "ok", "contour": "test"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		checkCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := bootstrap.Ready(checkCtx, pool); err != nil {
			httpapi.Error(w, r, 503, "TEMPORARILY_UNAVAILABLE", "База, схема или демоданные не готовы")
			return
		}
		httpapi.JSON(w, 200, map[string]string{"status": "ready", "contour": "test", "stage": "edo_settlement"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpapi.Error(w, r, 404, "RESOURCE_NOT_FOUND", "Ресурс не найден")
	})
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: httpapi.Middleware(logger, mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	workerCtx, stopWorker := context.WithCancel(ctx)
	var workerWG sync.WaitGroup
	workerErrors := make(chan error, 1)
	workerWG.Add(1)
	go func() { defer workerWG.Done(); workerErrors <- worker.Run(workerCtx) }()
	defer func() { stopWorker(); workerWG.Wait() }()
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("test API listening", "address", cfg.HTTPAddr, "origin", cfg.AppOrigin)
		serverErrors <- server.ListenAndServe()
	}()
	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case workerErr := <-workerErrors:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		if workerErr != nil {
			return fmt.Errorf("EDO worker stopped: %w", workerErr)
		}
		if ctx.Err() != nil {
			return nil
		}
		return errors.New("EDO worker stopped unexpectedly")
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
func healthcheck(args []string) error {
	flags := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	defaultURL := os.Getenv("HEALTHCHECK_URL")
	if defaultURL == "" {
		defaultURL = "http://127.0.0.1:8080/readyz"
	}
	url := flags.String("url", defaultURL, "HTTP readiness URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected healthcheck arguments")
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(*url)
	if err != nil {
		return errors.New("readiness request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("readiness returned HTTP %d", response.StatusCode)
	}
	return nil
}
