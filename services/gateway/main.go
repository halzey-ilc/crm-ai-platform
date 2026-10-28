// Command gateway is the AI Gateway: authenticated HTTP entry point for
// creating and reading AI jobs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"crmai/contracts"
	"crmai/internal/auth"
	"crmai/internal/config"
	"crmai/internal/httpapi"
	"crmai/internal/jobs"
	"crmai/internal/jobtypes"
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
		fmt.Fprintln(os.Stderr, "gateway:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadGateway()
	if err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	log := platform.Logger("gateway", cfg.LogLevel)

	schemas, err := contracts.Load()
	if err != nil {
		return err
	}
	types, err := jobtypes.Builtin(schemas)
	if err != nil {
		return err
	}
	verifier, err := auth.NewVerifier(cfg.AuthKeys, cfg.AuthIssuer, cfg.AuthAudience, cfg.AuthMaxTokenTTL)
	if err != nil {
		return err
	}

	ctx, stop := platform.SignalContext()
	defer stop()
	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	handler := httpapi.NewHandler(httpapi.Deps{
		Jobs:           jobs.NewService(store, types),
		Types:          types,
		Verifier:       verifier,
		Ready:          store.Ready,
		Log:            log,
		RequestTimeout: cfg.RequestTimeout,
		MaxBodyBytes:   cfg.MaxBodyBytes,
		RateLimitRPS:   cfg.RateLimitRPS,
		RateLimitBurst: cfg.RateLimitBurst,
	})
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.RequestTimeout,
		WriteTimeout:      cfg.RequestTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("gateway listening", "addr", cfg.HTTPAddr, "env", cfg.Env)

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
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
