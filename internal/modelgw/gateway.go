// Package modelgw is the Model Gateway: the only place that knows which
// provider (mock, local runtime, external API) serves a model task. Business
// capabilities name a task, never a vendor, so swapping a provider is a
// routing change, not a code change in the capability.
package modelgw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"crmai/internal/metrics"
)

// Request is one model invocation.
type Request struct {
	Task     string
	TenantID string
	JobID    string
	Input    json.RawMessage
}

// Response is the raw structured output of a provider, before contract
// validation.
type Response struct {
	Output   json.RawMessage
	Provider string
	Model    string
}

// Provider runs model tasks.
type Provider interface {
	Name() string
	Supports(task string) bool
	Invoke(ctx context.Context, req Request) (Response, error)
}

// TransientError marks a failure worth retrying (timeout, overload, network).
type TransientError struct{ Err error }

func (e *TransientError) Error() string { return "transient: " + e.Err.Error() }
func (e *TransientError) Unwrap() error { return e.Err }

// Transient wraps err as retryable.
func Transient(err error) error { return &TransientError{Err: err} }

// IsTransient reports whether err (or its cause) is retryable. Context
// deadline expiry counts as transient: the next attempt gets a fresh budget.
func IsTransient(err error) bool {
	var t *TransientError
	return errors.As(err, &t) || errors.Is(err, context.DeadlineExceeded)
}

// Gateway routes tasks to providers.
type Gateway struct {
	routes map[string]Provider
}

// New builds a gateway from task -> provider routes. Every route must point at
// a provider that declares support for the task.
func New(routes map[string]Provider) (*Gateway, error) {
	for task, p := range routes {
		if p == nil || !p.Supports(task) {
			return nil, fmt.Errorf("modelgw: provider for task %q does not support it", task)
		}
	}
	return &Gateway{routes: routes}, nil
}

// Tasks lists routed tasks.
func (g *Gateway) Tasks() []string {
	out := make([]string, 0, len(g.routes))
	for t := range g.routes {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Invoke runs the task on its routed provider.
func (g *Gateway) Invoke(ctx context.Context, req Request) (Response, error) {
	p, ok := g.routes[req.Task]
	if !ok {
		return Response{}, fmt.Errorf("modelgw: no provider routed for task %q", req.Task)
	}
	started := time.Now()
	resp, err := p.Invoke(ctx, req)
	metrics.ModelLatency.Observe(time.Since(started).Seconds(), req.Task, p.Name())
	switch {
	case err == nil:
		metrics.ModelCalls.Inc(req.Task, p.Name(), "ok")
	case IsTransient(err):
		metrics.ModelCalls.Inc(req.Task, p.Name(), "transient")
	default:
		metrics.ModelCalls.Inc(req.Task, p.Name(), "error")
	}
	if err != nil {
		return Response{}, err
	}
	if resp.Provider == "" {
		resp.Provider = p.Name()
	}
	return resp, nil
}
