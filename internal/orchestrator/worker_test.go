package orchestrator_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"crmai/internal/jobs"
	"crmai/internal/modelgw"
	"crmai/internal/orchestrator"
	"crmai/internal/testutil"
)

// fakeQueue records outcomes instead of persisting them.
type fakeQueue struct {
	mu        sync.Mutex
	completed []jobs.Result
	failed    []string
	retryAt   []*time.Time
	cancelled int
	cancelReq bool
}

func (q *fakeQueue) Claim(context.Context, string, int, time.Duration) ([]jobs.Job, error) {
	return nil, nil
}
func (q *fakeQueue) Complete(_ context.Context, _ jobs.Job, _ string, r jobs.Result) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.completed = append(q.completed, r)
	return nil
}
func (q *fakeQueue) Fail(_ context.Context, _ jobs.Job, _, code, _ string, retryAt *time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.failed = append(q.failed, code)
	q.retryAt = append(q.retryAt, retryAt)
	return nil
}
func (q *fakeQueue) MarkCancelled(context.Context, jobs.Job, string) error {
	q.cancelled++
	return nil
}
func (q *fakeQueue) CancelRequested(context.Context, jobs.Job) (bool, error) { return q.cancelReq, nil }

func worker(t *testing.T, q jobs.Queue, p modelgw.Provider) *orchestrator.Worker {
	gw, err := modelgw.New(map[string]modelgw.Provider{"call_summary": p})
	if err != nil {
		t.Fatal(err)
	}
	return &orchestrator.Worker{
		Queue: q, Types: testutil.Registry(t), Models: gw, ID: "w1", Concurrency: 1,
		PollInterval: time.Millisecond, Lease: time.Minute,
		Backoff: jobs.Backoff{Base: time.Second, Cap: time.Minute, Rand: func() float64 { return 0.5 }},
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:     func() time.Time { return time.Unix(1000, 0) },
	}
}

func job(attempt int) jobs.Job {
	return jobs.Job{ID: testutil.NewUUID(), TenantID: "acme", Type: "call.summary.v1", Status: jobs.StatusRunning,
		Input:    []byte(`{"transcript":"Добрый день. Перезвоню завтра.","language":"ru"}`),
		Attempts: attempt, MaxAttempts: 3, TimeoutSeconds: 5}
}

// provider is a model provider whose behaviour each test chooses.
type provider struct {
	output string
	err    error
	delay  time.Duration
}

func (provider) Name() string              { return "test" }
func (provider) Supports(task string) bool { return task == "call_summary" }
func (p provider) Invoke(ctx context.Context, _ modelgw.Request) (modelgw.Response, error) {
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return modelgw.Response{}, ctx.Err()
		}
	}
	if p.err != nil {
		return modelgw.Response{}, p.err
	}
	return modelgw.Response{Output: []byte(p.output), Model: "m"}, nil
}

const goodOutput = `{"schema":"call_summary.output.v1","summary":"Добрый день.","key_points":[{"text":"Добрый день.","evidence_quote":"Добрый день."}],"next_step":{"text":"Перезвоню завтра.","evidence_quote":"Перезвоню завтра."},"language":"ru","generator":{"provider":"test","model":"m","is_mock":true}}`

func TestSuccessStoresValidatedResult(t *testing.T) {
	q := &fakeQueue{}
	worker(t, q, provider{output: goodOutput}).Process(context.Background(), job(1))
	if len(q.completed) != 1 || q.completed[0].SchemaID != "call_summary.output.v1" || q.completed[0].Provider != "test" {
		t.Fatalf("unexpected outcome: %+v failed=%v", q.completed, q.failed)
	}
}

func TestTransientFailureIsRetriedWithBackoff(t *testing.T) {
	q := &fakeQueue{}
	worker(t, q, provider{err: modelgw.Transient(errors.New("overloaded"))}).Process(context.Background(), job(1))
	if len(q.failed) != 1 || q.failed[0] != "transient_error" || q.retryAt[0] == nil {
		t.Fatalf("want scheduled retry, got %v %v", q.failed, q.retryAt)
	}
	if want := time.Unix(1000, 0).Add(500 * time.Millisecond); !q.retryAt[0].Equal(want) {
		t.Fatalf("retry at %s, want %s", q.retryAt[0], want)
	}
}

func TestTransientFailureOnLastAttemptFails(t *testing.T) {
	q := &fakeQueue{}
	worker(t, q, provider{err: modelgw.Transient(errors.New("overloaded"))}).Process(context.Background(), job(3))
	if len(q.failed) != 1 || q.failed[0] != "attempts_exhausted" || q.retryAt[0] != nil {
		t.Fatalf("want final failure, got %v", q.failed)
	}
}

func TestTimeoutCountsAsTransient(t *testing.T) {
	q := &fakeQueue{}
	j := job(1)
	j.TimeoutSeconds = 1
	worker(t, q, provider{output: goodOutput, delay: 3 * time.Second}).Process(context.Background(), j)
	if len(q.failed) != 1 || q.failed[0] != "transient_error" {
		t.Fatalf("timeout must schedule a retry, got %v", q.failed)
	}
}

func TestContractViolationsFailPermanently(t *testing.T) {
	cases := map[string]string{
		"schema violation": `{"schema":"call_summary.output.v1","summary":""}`,
		"ungrounded quote": `{"schema":"call_summary.output.v1","summary":"s","key_points":[{"text":"x","evidence_quote":"Клиент согласился на скидку 50%"}],"next_step":null,"language":"ru","generator":{"provider":"t","model":"m","is_mock":true}}`,
		"language changed": `{"schema":"call_summary.output.v1","summary":"s","key_points":[],"next_step":null,"language":"en","generator":{"provider":"t","model":"m","is_mock":true}}`,
	}
	want := map[string]string{"schema violation": "output_contract_violation", "ungrounded quote": "output_not_grounded", "language changed": "output_not_grounded"}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			q := &fakeQueue{}
			worker(t, q, provider{output: out}).Process(context.Background(), job(1))
			if len(q.completed) != 0 || len(q.failed) != 1 || q.failed[0] != want[name] || q.retryAt[0] != nil {
				t.Fatalf("got completed=%d failed=%v", len(q.completed), q.failed)
			}
		})
	}
}

func TestCancellation(t *testing.T) {
	q := &fakeQueue{}
	j := job(1)
	j.CancelRequested = true
	worker(t, q, provider{output: goodOutput}).Process(context.Background(), j)
	if q.cancelled != 1 || len(q.completed) != 0 {
		t.Fatal("job flagged before start must be cancelled without running")
	}
	q = &fakeQueue{cancelReq: true}
	worker(t, q, provider{output: goodOutput}).Process(context.Background(), job(1))
	if q.cancelled != 1 || len(q.completed) != 0 {
		t.Fatal("cancellation requested during execution must discard the result")
	}
}

func TestReclaimedAfterFinalAttemptFails(t *testing.T) {
	q := &fakeQueue{}
	worker(t, q, provider{output: goodOutput}).Process(context.Background(), job(4))
	if len(q.failed) != 1 || q.failed[0] != "attempts_exhausted" {
		t.Fatalf("got %v", q.failed)
	}
}
