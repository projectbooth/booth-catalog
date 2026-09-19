// Command catalog is booth-catalog's entrypoint: the data, code and dashboard catalog, with
// lineage and cross-asset search (ADR 0001, 0018, 0042–0045).
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/projectbooth/booth-catalog/internal/api"
	"github.com/projectbooth/booth-catalog/internal/app"
	"github.com/projectbooth/booth-catalog/internal/auth"
	"github.com/projectbooth/booth-catalog/internal/code"
	"github.com/projectbooth/booth-catalog/internal/config"
	"github.com/projectbooth/booth-catalog/internal/dashboards"
	"github.com/projectbooth/booth-catalog/internal/data"
	"github.com/projectbooth/booth-catalog/internal/db"
	"github.com/projectbooth/booth-catalog/internal/events"
)

const shutdownTimeout = 10 * time.Second

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	stores, closeStores, err := openStores(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeStores()
	catalog := app.New(stores, cfg.MaxCodeSourceBytes)

	verifier, err := auth.NewVerifier(ctx, cfg.OIDC)
	if err != nil {
		return fmt.Errorf("creating OIDC verifier: %w", err)
	}

	// The dashboard subscription runs beside the HTTP server, not inside its startup path: a
	// NATS outage (or booth-core not having created the stream yet) must never keep the
	// catalog from serving datasets and code. Subscriber.Run retries on its own and reports
	// its state through /healthz.
	deps := api.Deps{Verifier: verifier, Catalog: catalog}
	if cfg.NATSURL == "" {
		log.Print("BOOTH_NATS_URL is not set: the dashboard event subscription is disabled, so no dashboards will be indexed")
	} else {
		sub := events.NewSubscriber(events.SubscriberConfig{URL: cfg.NATSURL}, events.NewProcessor(catalog.Dashboards))
		deps.Events = sub
		go sub.Run(ctx)
	}

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.NewRouter(deps),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second, // request bodies are small (code is capped at 1 MiB by default)
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("booth-catalog listening on %s", cfg.HTTPAddr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// openStores builds the three asset stores: PostgreSQL in a real deployment (ADR 0014), or
// in-process memory in explicit dev mode.
func openStores(ctx context.Context, cfg config.Config) (app.Stores, func(), error) {
	if cfg.DevMemory {
		log.Print("WARNING: BOOTH_CATALOG_DEV_MEMORY=true — the catalog is held only in this process's memory and is lost on restart. Local development only.")
		return app.Stores{Data: data.NewMemoryStore(), Code: code.NewMemoryStore(), Dashboards: dashboards.NewMemoryStore()}, func() {}, nil
	}

	pool, err := db.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		return app.Stores{}, nil, err
	}
	ds, err := data.NewPostgresStore(ctx, pool)
	if err != nil {
		pool.Close()
		return app.Stores{}, nil, fmt.Errorf("opening dataset store: %w", err)
	}
	cs, err := code.NewPostgresStore(ctx, pool)
	if err != nil {
		pool.Close()
		return app.Stores{}, nil, fmt.Errorf("opening code store: %w", err)
	}
	bs, err := dashboards.NewPostgresStore(ctx, pool)
	if err != nil {
		pool.Close()
		return app.Stores{}, nil, fmt.Errorf("opening dashboard store: %w", err)
	}
	return app.Stores{Data: ds, Code: cs, Dashboards: bs}, pool.Close, nil
}
