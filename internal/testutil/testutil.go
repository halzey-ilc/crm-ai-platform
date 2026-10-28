// Package testutil has shared fixtures for unit tests.
package testutil

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"testing"
	"time"

	"crmai/contracts"
	"crmai/internal/jobs"
	"crmai/internal/jobtypes"
)

// Registry returns the builtin job type registry.
func Registry(t testing.TB) *jobtypes.Registry {
	t.Helper()
	s, err := contracts.Load()
	if err != nil {
		t.Fatal(err)
	}
	r, err := jobtypes.Builtin(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// NewUUID returns a random version-4 UUID in canonical lowercase form.
func NewUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// MemoryRepo is an in-memory jobs.Repository that scopes every lookup by
// tenant the way the PostgreSQL adapter does.
type MemoryRepo struct {
	mu      sync.Mutex
	Jobs    map[string]jobs.Job
	Results map[string]jobs.Result
}

func NewMemoryRepo() *MemoryRepo {
	return &MemoryRepo{Jobs: map[string]jobs.Job{}, Results: map[string]jobs.Result{}}
}

func (m *MemoryRepo) Create(_ context.Context, n jobs.NewJob) (jobs.Job, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.Jobs {
		if j.TenantID == n.TenantID && j.CreatedBy == n.CreatedBy && j.IdempotencyKey == n.IdempotencyKey {
			return j, false, nil
		}
	}
	now := time.Now().UTC()
	j := jobs.Job{
		ID: NewUUID(), TenantID: n.TenantID, Type: n.Type, Status: jobs.StatusQueued,
		Input: n.Input, IdempotencyKey: n.IdempotencyKey, RequestHash: n.RequestHash,
		CreatedBy: n.CreatedBy, CorrelationID: n.CorrelationID, MaxAttempts: n.MaxAttempts,
		TimeoutSeconds: n.TimeoutSeconds, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
	}
	m.Jobs[j.ID] = j
	return j, true, nil
}

func (m *MemoryRepo) Get(_ context.Context, tenant, id string) (jobs.Job, *jobs.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.Jobs[id]
	if !ok || j.TenantID != tenant {
		return jobs.Job{}, nil, jobs.ErrNotFound
	}
	if r, ok := m.Results[id]; ok {
		return j, &r, nil
	}
	return j, nil, nil
}

func (m *MemoryRepo) RequestCancel(_ context.Context, tenant, id string) (jobs.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.Jobs[id]
	if !ok || j.TenantID != tenant {
		return jobs.Job{}, jobs.ErrNotFound
	}
	if j.Status.Terminal() {
		return jobs.Job{}, jobs.ErrNotCancellable
	}
	j.CancelRequested = true
	if j.Status == jobs.StatusQueued {
		j.Status = jobs.StatusCancelled
	}
	m.Jobs[id] = j
	return j, nil
}
