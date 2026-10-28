// Package platform holds process bootstrap shared by the binaries: logging,
// model routing and signal handling.
package platform

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"crmai/internal/jobtypes"
	"crmai/internal/modelgw"
	"crmai/internal/modelgw/mock"
)

// Logger returns a JSON logger tagged with the service name.
func Logger(service, level string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})).With("service", service)
}

// ModelGateway routes every task the registry needs to an enabled provider.
// Startup fails if a task has no provider, so a misconfigured worker never
// claims jobs it cannot run.
func ModelGateway(reg *jobtypes.Registry, enabled []string) (*modelgw.Gateway, error) {
	var providers []modelgw.Provider
	for _, name := range enabled {
		switch strings.TrimSpace(name) {
		case mock.Name:
			providers = append(providers, mock.Provider{})
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
			return nil, fmt.Errorf("no enabled provider supports task %q", task)
		}
	}
	return modelgw.New(routes)
}

// SignalContext is cancelled on SIGINT/SIGTERM.
func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
