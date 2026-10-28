package jobs_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"crmai/internal/jobs"
	"crmai/internal/testutil"
)

func TestRequestHashIgnoresKeyOrderAndWhitespace(t *testing.T) {
	a, _ := jobs.RequestHash("t", []byte(`{"a":1,"b":{"c":"x","d":[1,2]}}`))
	b, _ := jobs.RequestHash("t", []byte(" { \"b\" : {\"d\":[1,2],\"c\":\"x\"}, \"a\" : 1 } "))
	if a != b {
		t.Fatal("hash must be canonical")
	}
	c, _ := jobs.RequestHash("other", []byte(`{"a":1,"b":{"c":"x","d":[1,2]}}`))
	if a == c {
		t.Fatal("job type must be part of the hash")
	}
	big1, _ := jobs.RequestHash("t", []byte(`{"n":9007199254740993}`))
	big2, _ := jobs.RequestHash("t", []byte(`{"n":9007199254740992}`))
	if big1 == big2 {
		t.Fatal("large integers must not collide through float rounding")
	}
}

func newService(t *testing.T) (*jobs.Service, *testutil.MemoryRepo) {
	repo := testutil.NewMemoryRepo()
	return jobs.NewService(repo, testutil.Registry(t)), repo
}

func req(tenant, key, transcript string) jobs.CreateRequest {
	return jobs.CreateRequest{
		TenantID: tenant, CreatedBy: "user:1", Type: "call.summary.v1", IdempotencyKey: key,
		Input: []byte(`{"transcript":"` + transcript + `","language":"ru"}`), CorrelationID: "corr-0001",
	}
}

func TestCreateIsIdempotent(t *testing.T) {
	svc, repo := newService(t)
	ctx := context.Background()
	first, replayed, err := svc.Create(ctx, req("acme", "key-00001", "Привет."))
	if err != nil || replayed {
		t.Fatalf("first create: %v replayed=%v", err, replayed)
	}
	second, replayed, err := svc.Create(ctx, req("acme", "key-00001", "Привет."))
	if err != nil || !replayed || second.ID != first.ID {
		t.Fatalf("replay must return the same job: %v %v", err, replayed)
	}
	if len(repo.Jobs) != 1 {
		t.Fatalf("want 1 job, got %d", len(repo.Jobs))
	}
	if _, _, err := svc.Create(ctx, req("acme", "key-00001", "Другой текст.")); !errors.Is(err, jobs.ErrIdempotencyConflict) {
		t.Fatalf("same key with different body must conflict, got %v", err)
	}
	// The same key in another tenant is an independent request.
	other, replayed, err := svc.Create(ctx, req("globex", "key-00001", "Привет."))
	if err != nil || replayed || other.ID == first.ID {
		t.Fatalf("keys are scoped per tenant: %v %v", err, replayed)
	}
}

func TestCreateRejectsUnknownTypeAndBadInput(t *testing.T) {
	svc, _ := newService(t)
	r := req("acme", "key-00002", "x")
	r.Type = "nope.v1"
	if _, _, err := svc.Create(context.Background(), r); !errors.Is(err, jobs.ErrUnknownJobType) {
		t.Fatalf("got %v", err)
	}
	r = req("acme", "key-00003", "x")
	r.Input = []byte(`{"transcript":"x"}`)
	var invalid *jobs.InvalidInputError
	if _, _, err := svc.Create(context.Background(), r); !errors.As(err, &invalid) {
		t.Fatalf("got %v", err)
	}
}

func TestGetIsTenantScoped(t *testing.T) {
	svc, _ := newService(t)
	j, _, _ := svc.Create(context.Background(), req("acme", "key-00004", "x"))
	if _, _, err := svc.Get(context.Background(), "globex", j.ID); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("other tenant must get not found, got %v", err)
	}
	if _, _, err := svc.Get(context.Background(), "acme", "not-a-uuid"); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("malformed id must be not found, got %v", err)
	}
}

func TestBackoffIsBoundedAndGrows(t *testing.T) {
	b := jobs.Backoff{Base: time.Second, Cap: 10 * time.Second, Rand: func() float64 { return 0.999999 }}
	prev := time.Duration(0)
	for attempt := 1; attempt <= 10; attempt++ {
		d := b.Delay(attempt)
		if d > 10*time.Second || d < prev {
			t.Fatalf("attempt %d: delay %s out of bounds (prev %s)", attempt, d, prev)
		}
		prev = d
	}
	if prev < 9*time.Second {
		t.Fatalf("delay should reach the cap, got %s", prev)
	}
	zero := jobs.Backoff{Base: time.Second, Cap: time.Minute, Rand: func() float64 { return 0 }}
	if zero.Delay(3) != 0 {
		t.Fatal("full jitter allows zero delay")
	}
}
