package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/adapters/httpadapter"
	"github.com/alan1-666/mcp-gateway/internal/adapters/mcpadapter"
	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/alan1-666/mcp-gateway/internal/execution"
	"github.com/alan1-666/mcp-gateway/internal/identity"
	"github.com/alan1-666/mcp-gateway/internal/runs"
	"github.com/alan1-666/mcp-gateway/internal/store/postgres"
	"github.com/alan1-666/mcp-gateway/internal/transport/httpapi"
	"github.com/alan1-666/mcp-gateway/internal/transport/mcpserver"
	"github.com/alan1-666/mcp-gateway/internal/upstreams"
	"github.com/alan1-666/mcp-gateway/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func Run(mode string) error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("invalid database configuration")
	}
	config.MaxConns = 12
	config.MinConns = 1
	config.MaxConnLifetime = 30 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("could not create database pool")
	}
	defer pool.Close()
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = pool.Ping(checkCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("database is unavailable")
	}
	if mode == "migrate" {
		if err := migrations.Migrate(ctx, pool); err != nil {
			return fmt.Errorf("migration failed: %w", err)
		}
		slog.Info("database migrations applied")
		return nil
	}
	service := core.NewService(postgres.New(pool))
	if mode == "worker" {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		slog.Info("operation and agent run recovery worker started")
		runRepository := runs.NewRepository(pool)
		for {
			recoverCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			n, err := service.Recover(recoverCtx, 150*time.Second)
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.Error("operation recovery failed")
			} else if n > 0 {
				slog.Warn("interrupted operations require reconciliation", "count", n)
			}
			if env("AUTH_MODE", "token") == "cloud" {
				recoveryCtx, recoveryCancel := context.WithTimeout(ctx, 10*time.Second)
				recovered, recoveryErr := runRepository.RecoverExpired(recoveryCtx, env("RUNNER_WORKSPACE_ID", "team"))
				recoveryCancel()
				if recoveryErr != nil && ctx.Err() == nil {
					slog.Error("agent run recovery failed")
				} else if recovered > 0 {
					slog.Warn("interrupted agent runs require review", "count", recovered)
				}
			}
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
		}
	}
	var auth *identity.Auth
	switch env("AUTH_MODE", "token") {
	case "cloud":
		bootstrap := ""
		if path := os.Getenv("GATEWAY_BOOTSTRAP_FILE"); path != "" {
			b, e := os.ReadFile(path)
			if e != nil {
				return fmt.Errorf("cannot read bootstrap invitation")
			}
			bootstrap = strings.TrimSpace(string(b))
		}
		auth, err = identity.NewCloud(ctx, pool, os.Getenv("PUBLIC_ORIGIN"), bootstrap)
	case "token":
		auth, err = identity.FromFile(env("GATEWAY_IDENTITIES_FILE", ".local/identities.json"), strings.Split(env("GATEWAY_ALLOWED_ORIGINS", "http://127.0.0.1:4782,http://localhost:4782"), ","))
	default:
		return fmt.Errorf("AUTH_MODE must be cloud or token")
	}
	if err != nil {
		return err
	}
	credentials := []httpadapter.Credential{}
	if path := os.Getenv("GATEWAY_CREDENTIALS_FILE"); path != "" {
		b, e := os.ReadFile(path)
		if e != nil {
			return fmt.Errorf("cannot read credential file")
		}
		if e = json.Unmarshal(b, &credentials); e != nil {
			return fmt.Errorf("invalid credential file")
		}
	}
	adapter, err := httpadapter.New(strings.Split(os.Getenv("HTTP_ALLOWED_ORIGINS"), ","), strings.Split(os.Getenv("HTTP_ALLOWED_CIDRS"), ","), credentials)
	if err != nil {
		return err
	}
	upstreamStore := upstreams.NewStore(pool)
	remote := mcpadapter.New(adapter, upstreamStore)
	upstreamService := upstreams.New(upstreamStore, remote)
	executor := &execution.Executor{Service: service, Adapter: &execution.Router{HTTP: adapter, MCP: remote}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		c, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if pool.Ping(c) != nil {
			http.Error(w, "not ready", 503)
			return
		}
		w.WriteHeader(204)
	})
	address := env("API_ADDR", "127.0.0.1:8090")
	if mode == "gateway" {
		address = env("GATEWAY_ADDR", "127.0.0.1:8091")
		mux.Handle("/mcp", mcpserver.Handler(service, executor, auth))
	} else {
		api := &httpapi.API{Service: service, Executor: executor, Adapter: adapter, Upstreams: upstreamService}

		if env("AUTH_MODE", "token") == "cloud" {
			secretPath := os.Getenv("RUNNER_SHARED_SECRET_FILE")
			if secretPath != "" {
				secret, readErr := os.ReadFile(secretPath)
				if readErr != nil {
					return fmt.Errorf("cannot read runner authentication secret")
				}
				runAPI, runErr := runs.New(pool, service, executor, runs.Config{
					WorkspaceID: env("RUNNER_WORKSPACE_ID", "team"), SharedSecret: strings.TrimSpace(string(secret)),
				})
				if runErr != nil {
					return runErr
				}
				mux.Handle("/api/v1/runs", runAPI.PublicHandler(auth))
				mux.Handle("/api/v1/runs/", runAPI.PublicHandler(auth))
				mux.Handle("/internal/runner/", runAPI.InternalHandler())
			}
		}
		mux.Handle("/api/v1/auth/", auth.CloudHandler())
		mux.Handle("/api/", api.Handler(auth))
	}
	server := &http.Server{Addr: address, Handler: requests(mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 140 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	finished := make(chan error, 1)
	go func() {
		slog.Info("server started", "component", mode, "address", address)
		finished <- server.ListenAndServe()
	}()
	select {
	case err := <-finished:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 135*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return err
	}
	return nil
}
