// Package platform holds process bootstrap shared by the binaries: logging,
// model routing and signal handling.
package platform

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"crmai/internal/jobtypes"
	"crmai/internal/modelgw"
	"crmai/internal/modelgw/llamacpp"
	"crmai/internal/modelgw/mock"
	"crmai/internal/modelgw/whispercpp"
)

// Logger returns a JSON logger tagged with the service name.
func Logger(service, level string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})).With("service", service)
}

// ModelGateway routes every task the registry needs to an enabled provider.
// Startup fails if a task has no provider, so a misconfigured worker never
// claims jobs it cannot run. Optional tasks (speech recognition) may stay
// unrouted: their jobs then fail with provider_error.
//
// Providers are tried in the configured order: the first enabled provider that
// supports a task serves it.
func ModelGateway(reg *jobtypes.Registry, cfg Providers) (*modelgw.Gateway, error) {
	var providers []modelgw.Provider
	for _, name := range cfg.Enabled {
		switch strings.TrimSpace(name) {
		case mock.Name:
			providers = append(providers, mock.Provider{})
		case llamacpp.Name:
			providers = append(providers, llamacpp.Provider{
				BaseURL: cfg.LlamaCppURL,
				Model:   cfg.LlamaCppModel,
				// Per-call deadlines come from the job's context; this is a backstop.
				HTTP: &http.Client{Timeout: 10 * time.Minute},
				Log:  cfg.Log,
			})
		case whispercpp.Name:
			providers = append(providers, whispercpp.Provider{
				URL:        cfg.WhisperURL,
				Model:      cfg.WhisperModel,
				KyURL:      cfg.WhisperKyURL,
				KyLanguage: cfg.WhisperKyLanguage,
				Audio:      cfg.Audio,
				HTTP:       &http.Client{Timeout: 15 * time.Minute},
				Log:        cfg.Log,
			})
		default:
			return nil, fmt.Errorf("unknown provider %q", name)
		}
	}
	routes := map[string]modelgw.Provider{}
	for _, task := range reg.Tasks() {
		for _, p := range providers {
			if p.Supports(task) {
				routes[task] = p
				break
			}
		}
		if routes[task] == nil {
			if reg.OptionalTask(task) {
				delete(routes, task)
				if cfg.Log != nil {
					cfg.Log.Warn("optional task has no provider; its jobs will fail", "task", task)
				}
				continue
			}
			return nil, fmt.Errorf("no enabled provider supports task %q", task)
		}
	}
	// A second, larger llama.cpp model for the listed tasks (e.g. the agent and the
	// sales coach on a GPU server), the default model for everything else.
	if len(cfg.LargeTasks) > 0 {
		if cfg.LlamaCppLargeURL == "" {
			return nil, fmt.Errorf("AI_LLAMACPP_LARGE_TASKS needs AI_LLAMACPP_LARGE_URL")
		}
		large := llamacpp.Provider{BaseURL: cfg.LlamaCppLargeURL, Model: cfg.LlamaCppLargeModel,
			HTTP: &http.Client{Timeout: 10 * time.Minute}, Log: cfg.Log}
		known := map[string]bool{}
		for _, t := range reg.Tasks() {
			known[t] = true
		}
		for _, task := range cfg.LargeTasks {
			if !known[task] || !large.Supports(task) {
				return nil, fmt.Errorf("AI_LLAMACPP_LARGE_TASKS: %q is not a task the large model can serve", task)
			}
			routes[task] = large
		}
	}
	return modelgw.New(routes)
}

// Providers selects and configures model providers.
type Providers struct {
	Enabled       []string
	LlamaCppURL   string
	LlamaCppModel string
	// Optional larger llama.cpp model and the tasks routed to it.
	LlamaCppLargeURL   string
	LlamaCppLargeModel string
	LargeTasks         []string
	WhisperURL         string
	WhisperModel       string
	WhisperKyURL       string
	WhisperKyLanguage  string
	// Audio is where the whispercpp provider reads uploaded recordings.
	Audio whispercpp.AudioSource
	Log   *slog.Logger
}

// SignalContext is cancelled on SIGINT/SIGTERM.
func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
