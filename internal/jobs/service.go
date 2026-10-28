package jobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// TypeCatalog is what the service needs to know about job types.
type TypeCatalog interface {
	// Lookup returns the attempt and timeout policy of a job type.
	Lookup(jobType string) (maxAttempts int, timeout time.Duration, ok bool)
	// ValidateInput checks the input against the type's input contract.
	ValidateInput(jobType string, input json.RawMessage) error
}

// Service implements the request-side use cases.
type Service struct {
	repo  Repository
	types TypeCatalog
}

func NewService(repo Repository, types TypeCatalog) *Service {
	return &Service{repo: repo, types: types}
}

// CreateRequest is a validated-at-the-edge request to create a job. TenantID
// and CreatedBy come from the verified caller identity, never from the body.
type CreateRequest struct {
	TenantID       string
	CreatedBy      string
	Type           string
	Input          json.RawMessage
	IdempotencyKey string
	CorrelationID  string
}

// Create validates the request and creates the job idempotently. replayed is
// true when an earlier identical request already created it.
func (s *Service) Create(ctx context.Context, req CreateRequest) (job Job, replayed bool, err error) {
	maxAttempts, timeout, ok := s.types.Lookup(req.Type)
	if !ok {
		return Job{}, false, ErrUnknownJobType
	}
	if err := s.types.ValidateInput(req.Type, req.Input); err != nil {
		return Job{}, false, &InvalidInputError{Err: err}
	}
	hash, err := RequestHash(req.Type, req.Input)
	if err != nil {
		return Job{}, false, &InvalidInputError{Err: err}
	}
	j, created, err := s.repo.Create(ctx, NewJob{
		TenantID:       req.TenantID,
		Type:           req.Type,
		Input:          req.Input,
		IdempotencyKey: req.IdempotencyKey,
		RequestHash:    hash,
		CreatedBy:      req.CreatedBy,
		CorrelationID:  req.CorrelationID,
		MaxAttempts:    maxAttempts,
		TimeoutSeconds: int(timeout / time.Second),
	})
	if err != nil {
		return Job{}, false, err
	}
	if !created && j.RequestHash != hash {
		return Job{}, false, ErrIdempotencyConflict
	}
	return j, !created, nil
}

// Get returns a job of the caller's tenant.
func (s *Service) Get(ctx context.Context, tenantID, id string) (Job, *Result, error) {
	if !ValidJobID(id) {
		return Job{}, nil, ErrNotFound
	}
	return s.repo.Get(ctx, tenantID, id)
}

// Cancel requests cancellation of a job of the caller's tenant.
func (s *Service) Cancel(ctx context.Context, tenantID, id string) (Job, error) {
	if !ValidJobID(id) {
		return Job{}, ErrNotFound
	}
	return s.repo.RequestCancel(ctx, tenantID, id)
}

// RequestHash fingerprints a request independent of JSON key order and
// whitespace, so a retried request matches its original.
func RequestHash(jobType string, input json.RawMessage) (string, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber() // keep numbers exact instead of rounding through float64
	if err := dec.Decode(&v); err != nil {
		return "", fmt.Errorf("input is not valid JSON")
	}
	canonical, err := json.Marshal(v) // encoding/json sorts object keys
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(jobType))
	h.Write([]byte{0})
	h.Write(canonical)
	return hex.EncodeToString(h.Sum(nil)), nil
}
