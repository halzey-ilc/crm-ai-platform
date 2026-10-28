// Package jobs is the AI job domain: job lifecycle, the use cases that create,
// read and cancel jobs, and the ports the storage adapter implements.
package jobs

import (
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

// Status is a job's lifecycle state.
//
//	queued -> running -> succeeded
//	   |        |  \---> queued (retry after a transient failure)
//	   |        +------> failed | cancelled
//	   +---------------> cancelled
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Terminal reports whether no further transitions are possible.
func (s Status) Terminal() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusCancelled
}

// Job is one unit of AI work owned by exactly one tenant.
type Job struct {
	ID              string
	TenantID        string
	Type            string
	Status          Status
	Input           json.RawMessage
	IdempotencyKey  string
	RequestHash     string
	CreatedBy       string
	CorrelationID   string
	Attempts        int
	MaxAttempts     int
	TimeoutSeconds  int
	CancelRequested bool
	ErrorCode       string
	ErrorMessage    string
	NextRunAt       time.Time
	LeaseOwner      string
	LeaseExpiresAt  *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
}

// Result is the validated structured output of a succeeded job. A job has at
// most one result (enforced by the storage primary key).
type Result struct {
	JobID     string
	TenantID  string
	SchemaID  string
	Provider  string
	Model     string
	Output    json.RawMessage
	CreatedAt time.Time
}

// NewJob is what the create use case asks the repository to persist.
type NewJob struct {
	TenantID       string
	Type           string
	Input          json.RawMessage
	IdempotencyKey string
	RequestHash    string
	CreatedBy      string
	CorrelationID  string
	MaxAttempts    int
	TimeoutSeconds int
}

var (
	ErrNotFound            = errors.New("job not found")
	ErrIdempotencyConflict = errors.New("idempotency key reused with a different request")
	ErrNotCancellable      = errors.New("job already finished")
	ErrUnknownJobType      = errors.New("unknown job type")
	ErrLeaseLost           = errors.New("job lease lost")
)

// InvalidInputError wraps a contract validation failure of the job input.
type InvalidInputError struct{ Err error }

func (e *InvalidInputError) Error() string { return "invalid job input: " + e.Err.Error() }
func (e *InvalidInputError) Unwrap() error { return e.Err }

var (
	tenantPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	keyPattern    = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)
	uuidPattern   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// ValidTenantID reports whether s is a well-formed tenant slug (same rule as
// the CRM's tenant slugs).
func ValidTenantID(s string) bool { return tenantPattern.MatchString(s) }

// ValidIdempotencyKey reports whether s is an acceptable Idempotency-Key.
func ValidIdempotencyKey(s string) bool { return keyPattern.MatchString(s) }

// ValidJobID reports whether s is a canonical lowercase UUID.
func ValidJobID(s string) bool { return uuidPattern.MatchString(s) }
