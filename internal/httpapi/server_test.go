package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"crmai/internal/auth"
	"crmai/internal/httpapi"
	"crmai/internal/jobs"
	"crmai/internal/testutil"
)

var signingKey = []byte("0123456789abcdef0123456789abcdef")

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	types := testutil.Registry(t)
	v, err := auth.NewVerifier([][]byte{signingKey}, "crm-platform", "crm-ai-platform", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.NewHandler(httpapi.Deps{
		Jobs: jobs.NewService(testutil.NewMemoryRepo(), types), Types: types, Verifier: v,
		Ready: func(context.Context) error { return nil },
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)), RequestTimeout: 5 * time.Second,
		MaxBodyBytes: 4096, RateLimitRPS: 1000, RateLimitBurst: 1000,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func token(t *testing.T, tenant string, scopes ...string) string {
	t.Helper()
	if len(scopes) == 0 {
		scopes = []string{auth.ScopeJobsCreate, auth.ScopeJobsRead, auth.ScopeJobsCancel}
	}
	tok, err := auth.Mint(auth.MintRequest{Key: signingKey, Issuer: "crm-platform", Audience: "crm-ai-platform",
		Subject: "user:1", Tenant: tenant, Scopes: scopes, TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

type call struct {
	method, path, tok, body string
	headers                 map[string]string
}

func do(t *testing.T, srv *httptest.Server, c call) (int, map[string]any, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(c.method, srv.URL+c.path, strings.NewReader(c.body))
	if c.tok != "" {
		req.Header.Set("Authorization", "Bearer "+c.tok)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body, resp.Header
}

const validJob = `{"type":"call.summary.v1","input":{"transcript":"Добрый день. Перезвоню завтра.","language":"ru"}}`

func errCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func TestHealthIsPublic(t *testing.T) {
	srv := newServer(t)
	for _, p := range []string{"/health/live", "/health/ready"} {
		if code, _, _ := do(t, srv, call{method: "GET", path: p}); code != 200 {
			t.Errorf("%s: %d", p, code)
		}
	}
}

func TestCreateAndReadJob(t *testing.T) {
	srv := newServer(t)
	tok := token(t, "acme")
	code, body, hdr := do(t, srv, call{method: "POST", path: "/v1/jobs", tok: tok, body: validJob,
		headers: map[string]string{"Idempotency-Key": "req-000001", "X-Correlation-ID": "corr-abc-123"}})
	if code != http.StatusAccepted || body["status"] != "queued" || body["schema"] != "ai.job.v1" {
		t.Fatalf("create: %d %v", code, body)
	}
	if hdr.Get("X-Correlation-ID") != "corr-abc-123" || body["correlation_id"] != "corr-abc-123" {
		t.Fatal("correlation id must propagate")
	}
	if _, echoed := body["input"]; echoed {
		t.Fatal("input must never be echoed")
	}
	id := body["id"].(string)

	code, again, hdr := do(t, srv, call{method: "POST", path: "/v1/jobs", tok: tok, body: validJob,
		headers: map[string]string{"Idempotency-Key": "req-000001"}})
	if code != 200 || again["id"] != id || hdr.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %v", code, again)
	}
	if code, got, _ := do(t, srv, call{method: "GET", path: "/v1/jobs/" + id, tok: tok}); code != 200 || got["id"] != id {
		t.Fatalf("get: %d %v", code, got)
	}
}

func TestCrossTenantAccessLooksLikeNotFound(t *testing.T) {
	srv := newServer(t)
	_, body, _ := do(t, srv, call{method: "POST", path: "/v1/jobs", tok: token(t, "acme"), body: validJob,
		headers: map[string]string{"Idempotency-Key": "req-000002"}})
	id := body["id"].(string)
	other := token(t, "globex")
	for _, c := range []call{
		{method: "GET", path: "/v1/jobs/" + id, tok: other},
		{method: "POST", path: "/v1/jobs/" + id + "/cancel", tok: other},
	} {
		code, b, _ := do(t, srv, c)
		if code != 404 || errCode(b) != "not_found" {
			t.Errorf("%s %s: want 404 not_found, got %d %v", c.method, c.path, code, b)
		}
	}
	// Asking for another tenant explicitly is refused, not silently re-scoped.
	code, b, _ := do(t, srv, call{method: "GET", path: "/v1/jobs/" + id, tok: other,
		headers: map[string]string{"X-Tenant": "acme"}})
	if code != 403 || errCode(b) != "tenant_mismatch" {
		t.Errorf("X-Tenant mismatch: %d %v", code, b)
	}
}

func TestTenantInBodyIsRejected(t *testing.T) {
	srv := newServer(t)
	for _, body := range []string{
		`{"type":"call.summary.v1","tenant":"globex","input":{"transcript":"x","language":"ru"}}`,
		`{"type":"call.summary.v1","input":{"transcript":"x","language":"ru","tenant_id":"globex"}}`,
	} {
		code, b, _ := do(t, srv, call{method: "POST", path: "/v1/jobs", tok: token(t, "acme"), body: body,
			headers: map[string]string{"Idempotency-Key": "req-000003"}})
		if code != 400 && code != 422 {
			t.Errorf("body %s: want 400/422, got %d %v", body, code, b)
		}
	}
}

func TestErrors(t *testing.T) {
	srv := newServer(t)
	tok := token(t, "acme")
	readOnly := token(t, "acme", auth.ScopeJobsRead)
	key := map[string]string{"Idempotency-Key": "req-000004"}
	cases := []struct {
		name string
		c    call
		code int
		err  string
	}{
		{"no token", call{method: "GET", path: "/v1/jobs/x"}, 401, "unauthenticated"},
		{"bad token", call{method: "GET", path: "/v1/jobs/x", tok: "abc"}, 401, "unauthenticated"},
		{"missing scope", call{method: "POST", path: "/v1/jobs", tok: readOnly, body: validJob, headers: key}, 403, "forbidden"},
		{"no idempotency key", call{method: "POST", path: "/v1/jobs", tok: tok, body: validJob}, 400, "idempotency_key_required"},
		{"bad json", call{method: "POST", path: "/v1/jobs", tok: tok, body: "{", headers: key}, 400, "invalid_json"},
		{"two objects", call{method: "POST", path: "/v1/jobs", tok: tok, body: validJob + validJob, headers: key}, 400, "invalid_json"},
		{"unknown type", call{method: "POST", path: "/v1/jobs", tok: tok, body: `{"type":"x.v9","input":{}}`, headers: key}, 422, "unknown_job_type"},
		{"invalid input", call{method: "POST", path: "/v1/jobs", tok: tok, body: `{"type":"call.summary.v1","input":{"language":"ru"}}`, headers: key}, 422, "invalid_input"},
		{"too large", call{method: "POST", path: "/v1/jobs", tok: tok, body: `{"type":"call.summary.v1","input":{"transcript":"` + strings.Repeat("a", 5000) + `","language":"ru"}}`, headers: key}, 413, "payload_too_large"},
		{"unknown job", call{method: "GET", path: "/v1/jobs/00000000-0000-4000-8000-000000000000", tok: tok}, 404, "not_found"},
		{"unknown route", call{method: "GET", path: "/v1/nothing", tok: tok}, 404, "not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body, _ := do(t, srv, tc.c)
			if code != tc.code || errCode(body) != tc.err {
				t.Fatalf("want %d %s, got %d %v", tc.code, tc.err, code, body)
			}
			if body["schema"] != "ai.error.v1" {
				t.Fatalf("error body must follow ai.error.v1: %v", body)
			}
		})
	}
}

func TestUnsafeCorrelationIDIsReplaced(t *testing.T) {
	srv := newServer(t)
	_, _, hdr := do(t, srv, call{method: "GET", path: "/health/live",
		headers: map[string]string{"X-Correlation-ID": "bad id\" injected"}})
	if got := hdr.Get("X-Correlation-ID"); got == "" || strings.ContainsAny(got, " \"") {
		t.Fatalf("unsafe correlation id must be replaced, got %q", got)
	}
}

func TestCancelQueuedJob(t *testing.T) {
	srv := newServer(t)
	tok := token(t, "acme")
	_, body, _ := do(t, srv, call{method: "POST", path: "/v1/jobs", tok: tok, body: validJob,
		headers: map[string]string{"Idempotency-Key": "req-000005"}})
	id := body["id"].(string)
	code, b, _ := do(t, srv, call{method: "POST", path: "/v1/jobs/" + id + "/cancel", tok: tok})
	if code != 202 || b["status"] != "cancelled" {
		t.Fatalf("cancel: %d %v", code, b)
	}
	code, b, _ = do(t, srv, call{method: "POST", path: "/v1/jobs/" + id + "/cancel", tok: tok})
	if code != 409 || errCode(b) != "not_cancellable" {
		t.Fatalf("second cancel: %d %v", code, b)
	}
}
