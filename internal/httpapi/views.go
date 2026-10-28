package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"crmai/internal/jobs"
)

// errorBody is contract ai.error.v1.
type errorBody struct {
	Schema string      `json:"schema"`
	Error  errorDetail `json:"error"`
}

type errorDetail struct {
	Code          string   `json:"code"`
	Message       string   `json:"message"`
	CorrelationID string   `json:"correlation_id"`
	Details       []string `json:"details,omitempty"`
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	writeErrorDetail(w, r, status, code, msg, nil)
}

func writeErrorDetail(w http.ResponseWriter, r *http.Request, status int, code, msg string, details []string) {
	writeJSON(w, status, errorBody{Schema: "ai.error.v1", Error: errorDetail{
		Code: code, Message: msg, CorrelationID: CorrelationID(r.Context()), Details: details,
	}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// jobBody is contract ai.job.v1. The job input is never echoed back.
type jobBody struct {
	Schema          string      `json:"schema"`
	ID              string      `json:"id"`
	Type            string      `json:"type"`
	Tenant          string      `json:"tenant"`
	Status          jobs.Status `json:"status"`
	Attempts        int         `json:"attempts"`
	MaxAttempts     int         `json:"max_attempts"`
	CancelRequested bool        `json:"cancel_requested"`
	CorrelationID   string      `json:"correlation_id"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
	StartedAt       *time.Time  `json:"started_at"`
	FinishedAt      *time.Time  `json:"finished_at"`
	Error           *jobError   `json:"error"`
	Result          *resultBody `json:"result"`
}

type jobError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type resultBody struct {
	SchemaID  string          `json:"schema_id"`
	Provider  string          `json:"provider"`
	Model     string          `json:"model"`
	Output    json.RawMessage `json:"output"`
	CreatedAt time.Time       `json:"created_at"`
}

func jobView(j jobs.Job, r *jobs.Result) jobBody {
	b := jobBody{
		Schema: "ai.job.v1", ID: j.ID, Type: j.Type, Tenant: j.TenantID, Status: j.Status,
		Attempts: j.Attempts, MaxAttempts: j.MaxAttempts, CancelRequested: j.CancelRequested,
		CorrelationID: j.CorrelationID, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
		StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
	}
	// A queued job may carry the last transient error; only expose errors of
	// failed jobs so clients do not mistake a pending retry for a failure.
	if j.Status == jobs.StatusFailed && j.ErrorCode != "" {
		b.Error = &jobError{Code: j.ErrorCode, Message: j.ErrorMessage}
	}
	if r != nil {
		b.Result = &resultBody{SchemaID: r.SchemaID, Provider: r.Provider, Model: r.Model, Output: r.Output, CreatedAt: r.CreatedAt}
	}
	return b
}
