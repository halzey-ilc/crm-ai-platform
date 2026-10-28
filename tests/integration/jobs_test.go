//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"crmai/contracts"
	"crmai/internal/auth"
	"crmai/internal/httpapi"
	"crmai/internal/jobs"
	"crmai/internal/orchestrator"
	"crmai/internal/platform"
	"crmai/internal/testutil"
)

var signingKey = []byte("integration-signing-key-0123456789abcdef")

func newID(prefix string) string { return prefix + "-" + randomHex(6) }

func createJob(t *testing.T, tenant, key string) jobs.Job {
	t.Helper()
	svc := jobs.NewService(store, testutil.Registry(t))
	j, _, err := svc.Create(context.Background(), jobs.CreateRequest{
		TenantID: tenant, CreatedBy: "user:it", Type: "call.summary.v1", IdempotencyKey: key,
		Input:         []byte(`{"transcript":"Добрый день. Клиенту нужна интеграция. Перезвоню в пятницу.","language":"ru"}`),
		CorrelationID: "corr-integration",
	})
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func newWorker(t *testing.T, id string) *orchestrator.Worker {
	t.Helper()
	types := testutil.Registry(t)
	models, err := platform.ModelGateway(types, platform.Providers{Enabled: []string{"mock"}})
	if err != nil {
		t.Fatal(err)
	}
	return &orchestrator.Worker{
		Queue: store, Types: types, Models: models, ID: id, Concurrency: 2,
		PollInterval: 10 * time.Millisecond, Lease: time.Minute,
		Backoff: jobs.Backoff{Base: time.Millisecond, Cap: time.Millisecond, Rand: func() float64 { return 0 }},
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// claimJob claims until the given job is among the claimed ones. Jobs from
// other tests may be claimed too; those are processed normally.
func claimJob(t *testing.T, w *orchestrator.Worker, id string, lease time.Duration) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		claimed, err := store.Claim(context.Background(), w.ID, 100, lease)
		if err != nil {
			t.Fatal(err)
		}
		var mine *jobs.Job
		for i := range claimed {
			if claimed[i].ID == id {
				mine = &claimed[i]
			} else {
				w.Process(context.Background(), claimed[i])
			}
		}
		if mine != nil {
			return *mine
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s was never claimed", id)
	return jobs.Job{}
}

func resultCount(t *testing.T, jobID string) int {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), ownerDBURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var n int
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM ai_job_result WHERE job_id = $1`, jobID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestReady(t *testing.T) {
	if err := store.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEndToEndThroughHTTP(t *testing.T) {
	types := testutil.Registry(t)
	v, _ := auth.NewVerifier([][]byte{signingKey}, "crm-platform", "crm-ai-platform", 10*time.Minute)
	srv := httptest.NewServer(httpapi.NewHandler(httpapi.Deps{
		Jobs: jobs.NewService(store, types), Types: types, Verifier: v, Ready: store.Ready,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), RequestTimeout: 5 * time.Second,
		MaxBodyBytes: 1 << 20, RateLimitRPS: 1000, RateLimitBurst: 1000,
	}))
	defer srv.Close()

	tenant := newID("e2e")
	tok, _ := auth.Mint(auth.MintRequest{Key: signingKey, Issuer: "crm-platform", Audience: "crm-ai-platform",
		Subject: "user:it", Tenant: tenant, Scopes: []string{auth.ScopeJobsCreate, auth.ScopeJobsRead}, TTL: time.Minute})
	body := `{"type":"call.summary.v1","input":{"transcript":"Здравствуйте. Нужна интеграция с 1С. Перезвоню в пятницу.","language":"ru"}}`
	call := func(method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Idempotency-Key", "e2e-key-0001")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	code, created := call("POST", "/v1/jobs", body)
	if code != 202 {
		t.Fatalf("create: %d %v", code, created)
	}
	id := created["id"].(string)
	if code, again := call("POST", "/v1/jobs", body); code != 200 || again["id"] != id {
		t.Fatalf("idempotent replay: %d %v", code, again)
	}

	w := newWorker(t, newID("w"))
	w.Process(context.Background(), claimJob(t, w, id, time.Minute))

	code, got := call("GET", "/v1/jobs/"+id, "")
	if code != 200 || got["status"] != "succeeded" {
		t.Fatalf("get: %d %v", code, got)
	}
	result := got["result"].(map[string]any)
	if result["provider"] != "mock" || result["schema_id"] != "call_summary.output.v1" {
		t.Fatalf("unexpected result %v", result)
	}
	out, _ := json.Marshal(result["output"])
	schemas, _ := contracts.Load()
	if err := schemas.Validate("call_summary.output.v1", out); err != nil {
		t.Fatalf("stored output violates contract: %v", err)
	}
}

func TestReprocessingNeverDuplicatesResult(t *testing.T) {
	j := createJob(t, newID("dup"), "dup-key-0001")
	w1, w2 := newWorker(t, newID("w1")), newWorker(t, newID("w2"))

	// w1 claims with a short lease and "stalls" past it; w2 re-claims.
	stalled := claimJob(t, w1, j.ID, time.Second)
	time.Sleep(1500 * time.Millisecond)
	reclaimed := claimJob(t, w2, j.ID, time.Minute)
	if reclaimed.Attempts != 2 {
		t.Fatalf("re-claim must count an attempt, got %d", reclaimed.Attempts)
	}
	w2.Process(context.Background(), reclaimed)

	// The stalled worker finishes late: its outcome must be discarded.
	res, err := w1.Types.Execute(context.Background(), w1.Models, stalled)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(context.Background(), stalled, w1.ID, res); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("late completion must lose the lease, got %v", err)
	}
	// Even the lease holder completing twice cannot add a second result.
	if err := store.Complete(context.Background(), reclaimed, w2.ID, res); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("second completion must be rejected, got %v", err)
	}
	if n := resultCount(t, j.ID); n != 1 {
		t.Fatalf("want exactly 1 result, got %d", n)
	}
	got, _, _ := store.Get(context.Background(), j.TenantID, j.ID)
	if got.Status != jobs.StatusSucceeded {
		t.Fatalf("status %s", got.Status)
	}
}

func TestTransientFailureIsRetried(t *testing.T) {
	j := createJob(t, newID("retry"), "retry-key-0001")
	w := newWorker(t, newID("w"))
	claimed := claimJob(t, w, j.ID, time.Minute)
	past := time.Now().Add(-time.Second)
	if err := store.Fail(context.Background(), claimed, w.ID, "transient_error", "temporary", &past); err != nil {
		t.Fatal(err)
	}
	got, _, _ := store.Get(context.Background(), j.TenantID, j.ID)
	if got.Status != jobs.StatusQueued || got.LeaseOwner != "" {
		t.Fatalf("want re-queued without lease, got %s %q", got.Status, got.LeaseOwner)
	}
	again := claimJob(t, w, j.ID, time.Minute)
	if again.Attempts != 2 {
		t.Fatalf("want attempt 2, got %d", again.Attempts)
	}
	w.Process(context.Background(), again)
	got, res, _ := store.Get(context.Background(), j.TenantID, j.ID)
	if got.Status != jobs.StatusSucceeded || res == nil {
		t.Fatalf("retry must succeed, got %s", got.Status)
	}
}

func TestCancelQueuedAndRunning(t *testing.T) {
	tenant := newID("cancel")
	queued := createJob(t, tenant, "cancel-key-0001")
	got, err := store.RequestCancel(context.Background(), tenant, queued.ID)
	if err != nil || got.Status != jobs.StatusCancelled {
		t.Fatalf("queued job must cancel immediately: %v %s", err, got.Status)
	}
	if _, err := store.RequestCancel(context.Background(), tenant, queued.ID); !errors.Is(err, jobs.ErrNotCancellable) {
		t.Fatalf("got %v", err)
	}

	running := createJob(t, tenant, "cancel-key-0002")
	w := newWorker(t, newID("w"))
	claimed := claimJob(t, w, running.ID, time.Minute)
	if got, err := store.RequestCancel(context.Background(), tenant, running.ID); err != nil || got.Status != jobs.StatusRunning || !got.CancelRequested {
		t.Fatalf("running job must be flagged: %v %+v", err, got)
	}
	w.Process(context.Background(), claimed)
	got, res, _ := store.Get(context.Background(), tenant, running.ID)
	if got.Status != jobs.StatusCancelled || res != nil {
		t.Fatalf("want cancelled without result, got %s %v", got.Status, res)
	}
}

func TestConcurrentClaimsAreDisjoint(t *testing.T) {
	tenant := newID("conc")
	want := map[string]bool{}
	for i := 0; i < 30; i++ {
		want[createJob(t, tenant, "conc-key-"+randomHex(4)).ID] = true
	}
	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func(worker string) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				claimed, err := store.Claim(context.Background(), worker, 3, time.Minute)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				for _, j := range claimed {
					seen[j.ID]++
				}
				mu.Unlock()
			}
		}(newID("cw"))
	}
	wg.Wait()
	for id := range want {
		if seen[id] != 1 {
			t.Errorf("job %s claimed %d times", id, seen[id])
		}
	}
}
