// Package httpapi is the AI Gateway's HTTP adapter (contract: docs/API_CONTRACTS.md).
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"crmai/contracts"
	"crmai/internal/auth"
	"crmai/internal/jobs"
	"crmai/internal/jobtypes"
	"crmai/internal/knowledge"
)

// Readiness reports whether dependencies are usable.
type Readiness func(ctx context.Context) error

// Deps are the gateway's collaborators.
type Deps struct {
	Jobs           *jobs.Service
	Types          *jobtypes.Registry
	Verifier       *auth.Verifier
	Knowledge      KnowledgeStore     // nil disables the /v1/knowledge endpoints
	Embedder       knowledge.Embedder // nil: full-text search only
	Audio          AudioStore         // nil disables the /v1/audio endpoints
	Ready          Readiness
	Log            *slog.Logger
	RequestTimeout time.Duration
	MaxBodyBytes   int64
	// MaxUploadBytes bounds file uploads (recordings, knowledge files).
	MaxUploadBytes int64
	RateLimitRPS   float64
	RateLimitBurst int
}

// NewHandler builds the gateway's HTTP handler.
func NewHandler(d Deps) http.Handler {
	h := &handlers{d: d}
	api := http.NewServeMux()
	api.HandleFunc("POST /v1/jobs", h.requireScope(auth.ScopeJobsCreate, h.createJob))
	api.HandleFunc("GET /v1/jobs/{id}", h.requireScope(auth.ScopeJobsRead, h.getJob))
	api.HandleFunc("POST /v1/jobs/{id}/cancel", h.requireScope(auth.ScopeJobsCancel, h.cancelJob))
	api.HandleFunc("GET /v1/job-types", h.requireScope(auth.ScopeJobsRead, h.listJobTypes))
	if d.Knowledge != nil {
		api.HandleFunc("PUT /v1/knowledge/documents/{ref}", h.requireScope(auth.ScopeKnowledgeWrite, h.putDocument))
		api.HandleFunc("DELETE /v1/knowledge/documents/{ref}", h.requireScope(auth.ScopeKnowledgeWrite, h.deleteDocument))
		api.HandleFunc("POST /v1/knowledge/ask", h.requireScope(auth.ScopeJobsCreate, h.ask))
		api.HandleFunc("PUT /v1/knowledge/files/{ref}", h.requireScope(auth.ScopeKnowledgeWrite, h.putFile))
	}
	if d.Audio != nil {
		api.HandleFunc("PUT /v1/audio/{ref}", h.requireScope(auth.ScopeAudioWrite, h.putAudio))
		api.HandleFunc("DELETE /v1/audio/{ref}", h.requireScope(auth.ScopeAudioWrite, h.deleteAudio))
	}
	api.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such endpoint")
	})

	limiter := newRateLimiter(d.RateLimitRPS, d.RateLimitBurst)
	protected := withAuth(d.Log, d.Verifier, limiter.middleware(api))

	root := http.NewServeMux()
	root.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	root.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if err := d.Ready(r.Context()); err != nil {
			d.Log.Warn("not ready", "reason", err.Error())
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	root.Handle("/v1/", protected)
	root.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such endpoint")
	})

	return withCorrelation(withRecover(d.Log, withAccessLog(d.Log,
		withLimits(d.RequestTimeout, d.MaxBodyBytes, d.MaxUploadBytes, root))))
}

type handlers struct{ d Deps }

func (h *handlers) requireScope(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principalFrom(r.Context())
		if !ok || !p.Has(scope) {
			writeError(w, r, http.StatusForbidden, "forbidden", "token lacks scope "+scope)
			return
		}
		next(w, r)
	}
}

type createJobBody struct {
	Type  string          `json:"type"`
	Input json.RawMessage `json:"input"`
}

func (h *handlers) createJob(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	key := r.Header.Get("Idempotency-Key")
	if !jobs.ValidIdempotencyKey(key) {
		writeError(w, r, http.StatusBadRequest, "idempotency_key_required",
			"Idempotency-Key header is required (8-128 chars of [A-Za-z0-9._:-])")
		return
	}
	var body createJobBody
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "request body too large")
			return
		}
		writeError(w, r, http.StatusBadRequest, "invalid_json", "body must be a JSON object with type and input")
		return
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, "invalid_json", "body must contain a single JSON object")
		return
	}
	if body.Type == "" || len(body.Input) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "type and input are required")
		return
	}
	job, replayed, err := h.d.Jobs.Create(r.Context(), jobs.CreateRequest{
		TenantID:       p.Tenant,
		CreatedBy:      p.Subject,
		Type:           body.Type,
		Input:          body.Input,
		IdempotencyKey: key,
		CorrelationID:  CorrelationID(r.Context()),
	})
	if err != nil {
		h.jobError(w, r, err)
		return
	}
	status := http.StatusAccepted
	if replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("Location", "/v1/jobs/"+job.ID)
	writeJSON(w, status, jobView(job, nil))
}

func (h *handlers) getJob(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	job, res, err := h.d.Jobs.Get(r.Context(), p.Tenant, r.PathValue("id"))
	if err != nil {
		h.jobError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, jobView(job, res))
}

func (h *handlers) cancelJob(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	job, err := h.d.Jobs.Cancel(r.Context(), p.Tenant, r.PathValue("id"))
	if err != nil {
		h.jobError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, jobView(job, nil))
}

func (h *handlers) listJobTypes(w http.ResponseWriter, r *http.Request) {
	type view struct {
		Type         string `json:"type"`
		Capability   string `json:"capability"`
		InputSchema  string `json:"input_schema"`
		OutputSchema string `json:"output_schema"`
		MaxAttempts  int    `json:"max_attempts"`
		TimeoutSec   int    `json:"timeout_seconds"`
	}
	var out []view
	for _, d := range h.d.Types.List() {
		out = append(out, view{d.Type, d.Capability, d.InputSchema, d.OutputSchema, d.MaxAttempts, int(d.Timeout / time.Second)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": "ai.job_types.v1", "items": out})
}

func (h *handlers) jobError(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *jobs.InvalidInputError
	var verr *contracts.ValidationError
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		// Same answer for "does not exist" and "belongs to another tenant".
		writeError(w, r, http.StatusNotFound, "not_found", "job not found")
	case errors.Is(err, jobs.ErrUnknownJobType):
		writeError(w, r, http.StatusUnprocessableEntity, "unknown_job_type", "unknown job type")
	case errors.Is(err, jobs.ErrIdempotencyConflict):
		writeError(w, r, http.StatusConflict, "idempotency_conflict", "Idempotency-Key was already used with a different request")
	case errors.Is(err, jobs.ErrNotCancellable):
		writeError(w, r, http.StatusConflict, "not_cancellable", "job already finished")
	case errors.As(err, &invalid):
		detail := []string{}
		if errors.As(err, &verr) {
			detail = verr.Problems
		}
		writeErrorDetail(w, r, http.StatusUnprocessableEntity, "invalid_input", "input does not match the job type's contract", detail)
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, r, http.StatusServiceUnavailable, "timeout", "request timed out")
	default:
		h.d.Log.Error("request failed", "err", err, "correlation_id", CorrelationID(r.Context()))
		writeError(w, r, http.StatusInternalServerError, "internal_error", "internal error")
	}
}
