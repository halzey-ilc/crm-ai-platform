// Package jobtypes is the catalog of job types: for each one, its input and
// output contracts, retry/timeout policy, the model task it needs and the
// checks a model output must pass before it is accepted as a result.
package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"crmai/contracts"
	"crmai/internal/jobs"
	"crmai/internal/modelgw"
)

// Definition describes one job type.
type Definition struct {
	Type         string
	Capability   string
	Task         string
	InputSchema  string
	OutputSchema string
	MaxAttempts  int
	Timeout      time.Duration
	// Ground checks semantic rules a schema cannot express, e.g. that every
	// evidence quote occurs in the input. Nil means no extra checks.
	Ground func(input, output json.RawMessage) error
}

// Registry holds the known job types.
type Registry struct {
	schemas *contracts.Schemas
	defs    map[string]Definition
}

// NewRegistry builds the registry and checks that every referenced contract
// exists.
func NewRegistry(schemas *contracts.Schemas, defs ...Definition) (*Registry, error) {
	r := &Registry{schemas: schemas, defs: map[string]Definition{}}
	for _, d := range defs {
		if _, dup := r.defs[d.Type]; dup {
			return nil, fmt.Errorf("jobtypes: duplicate type %q", d.Type)
		}
		if !schemas.Has(d.InputSchema) || !schemas.Has(d.OutputSchema) {
			return nil, fmt.Errorf("jobtypes: %q references an unknown schema", d.Type)
		}
		if d.MaxAttempts < 1 || d.Timeout <= 0 {
			return nil, fmt.Errorf("jobtypes: %q needs MaxAttempts >= 1 and a timeout", d.Type)
		}
		r.defs[d.Type] = d
	}
	return r, nil
}

// Builtin returns the registry with all job types shipped in this release.
func Builtin(schemas *contracts.Schemas) (*Registry, error) {
	return NewRegistry(schemas, CallSummaryV1(), CallSummaryV2(), OperationsDigestV1(), KpiExplanationV1(), MessageReplyV1(), DealCoachV1(), RiskReviewV1(), AgentPlanV1())
}

func (r *Registry) Get(jobType string) (Definition, bool) {
	d, ok := r.defs[jobType]
	return d, ok
}

// List returns definitions sorted by type.
func (r *Registry) List() []Definition {
	out := make([]Definition, 0, len(r.defs))
	for _, d := range r.defs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// Tasks returns the distinct model tasks the registry needs routed.
func (r *Registry) Tasks() []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range r.defs {
		if !seen[d.Task] {
			seen[d.Task] = true
			out = append(out, d.Task)
		}
	}
	sort.Strings(out)
	return out
}

// Lookup implements jobs.TypeCatalog.
func (r *Registry) Lookup(jobType string) (int, time.Duration, bool) {
	d, ok := r.defs[jobType]
	return d.MaxAttempts, d.Timeout, ok
}

// ValidateInput implements jobs.TypeCatalog.
func (r *Registry) ValidateInput(jobType string, input json.RawMessage) error {
	d, ok := r.defs[jobType]
	if !ok {
		return jobs.ErrUnknownJobType
	}
	return r.schemas.Validate(d.InputSchema, input)
}

// PermanentError is an execution failure that retrying cannot fix (contract
// violation, unknown type).
type PermanentError struct {
	Code string
	Err  error
}

func (e *PermanentError) Error() string { return e.Code + ": " + e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Execute runs a job through the model gateway and returns a result only if
// the output satisfies the output contract and the grounding rules.
func (r *Registry) Execute(ctx context.Context, gw *modelgw.Gateway, j jobs.Job) (jobs.Result, error) {
	d, ok := r.defs[j.Type]
	if !ok {
		return jobs.Result{}, &PermanentError{Code: "unknown_job_type", Err: jobs.ErrUnknownJobType}
	}
	resp, err := gw.Invoke(ctx, modelgw.Request{Task: d.Task, TenantID: j.TenantID, JobID: j.ID, Input: j.Input})
	if err != nil {
		if modelgw.IsTransient(err) {
			return jobs.Result{}, err
		}
		return jobs.Result{}, &PermanentError{Code: "provider_error", Err: err}
	}
	if err := r.schemas.Validate(d.OutputSchema, resp.Output); err != nil {
		return jobs.Result{}, &PermanentError{Code: "output_contract_violation", Err: err}
	}
	if d.Ground != nil {
		if err := d.Ground(j.Input, resp.Output); err != nil {
			return jobs.Result{}, &PermanentError{Code: "output_not_grounded", Err: err}
		}
	}
	return jobs.Result{
		JobID:    j.ID,
		TenantID: j.TenantID,
		SchemaID: d.OutputSchema,
		Provider: resp.Provider,
		Model:    resp.Model,
		Output:   resp.Output,
	}, nil
}

// IsPermanent reports whether err is a PermanentError.
func IsPermanent(err error) bool {
	var p *PermanentError
	return errors.As(err, &p)
}
