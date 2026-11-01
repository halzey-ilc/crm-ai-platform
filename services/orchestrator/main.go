// Command orchestrator runs AI jobs from the PostgreSQL-backed queue.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"time"

	"crmai/contracts"
	"crmai/internal/config"
	"crmai/internal/jobs"
	"crmai/internal/jobtypes"
	"crmai/internal/metrics"
	"crmai/internal/orchestrator"
	"crmai/internal/platform"
	"crmai/internal/store/postgres"
)

func main() {
	healthcheck := flag.String("healthcheck", "", "probe this URL and exit 0 if it returns 200 (container healthcheck)")
	flag.Parse()
	if *healthcheck != "" {
		os.Exit(probe(*healthcheck))
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator:", err)
		os.Exit(1)
	}
}

func run() error {
	schemas, err := contracts.Load()
	if err != nil {
		return err
	}
	types, err := jobtypes.Builtin(schemas)
	if err != nil {
		return err
	}
	var maxTimeout time.Duration
	for _, d := range types.List() {
		maxTimeout = max(maxTimeout, d.Timeout)
	}
	cfg, err := config.LoadOrchestrator(maxTimeout)
	if err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	log := platform.Logger("orchestrator", cfg.LogLevel)
	ctx, stop := platform.SignalContext()
	defer stop()
	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	models, err := platform.ModelGateway(types, platform.Providers{
		Enabled:           cfg.Providers,
		LlamaCppURL:       cfg.LlamaCppURL,
		LlamaCppModel:     cfg.LlamaCppModel,
		WhisperURL:        cfg.WhisperURL,
		WhisperModel:      cfg.WhisperModel,
		WhisperKyURL:      cfg.WhisperKyURL,
		WhisperKyLanguage: cfg.WhisperKyLanguage,
		Audio:             store,
		Log:               log,
	})
	if err != nil {
		return err
	}

	// Liveness only: the worker is alive while its loop runs. Readiness of the
	// database is checked by the loop itself (claim errors are logged).
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := store.Ready(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	// Internal port only (not on the CRM link network); no tenant data in the series.
	mux.Handle("GET /metrics", metrics.Default.Handler())
	health := &http.Server{Addr: cfg.HealthAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := health.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("health server stopped", "err", err)
		}
	}()
	defer health.Shutdown(context.Background()) //nolint:errcheck

	w := &orchestrator.Worker{
		Queue:        store,
		Types:        types,
		Models:       models,
		ID:           cfg.WorkerID,
		Concurrency:  cfg.Concurrency,
		PollInterval: cfg.PollInterval,
		Lease:        cfg.Lease,
		Backoff:      jobs.Backoff{Base: 2 * time.Second, Cap: 5 * time.Minute, Rand: rand.Float64},
		Log:          log,
	}
	hk := &orchestrator.Housekeeping{
		Store:               store,
		WorkerID:            cfg.WorkerID,
		RetentionDays:       cfg.RetentionDays,
		Audio:               store,
		AudioRetentionHours: cfg.AudioRetentionHours,
		StatsEvery:          30 * time.Second,
		PurgeEvery:          time.Hour,
		BatchSize:           1000,
		Log:                 log,
	}
	go hk.Run(ctx)
	log.Info("orchestrator started", "worker_id", cfg.WorkerID, "concurrency", cfg.Concurrency,
		"tasks", models.Tasks(), "env", cfg.Env, "retention_days", cfg.RetentionDays)
	w.Run(ctx)
	log.Info("orchestrator stopped")
	return nil
}

func probe(url string) int {
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(url) //nolint:noctx // one-shot probe
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
