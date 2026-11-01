package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crmai/internal/auth"
)

const testKey = "0123456789abcdef0123456789abcdef"

func TestLoadExamplesSkipsCommentsAndConvertsOldCallFormat(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.jsonl")
	data := "// synthetic only\n\n" +
		`{"id":"kpi-1","type":"kpi.explanation.v1","input":{"language":"ky","metrics":[]}}` + "\n" +
		`{"id":"ru-01","language":"ru","transcript":"Здравствуйте","expect_facts":["x"]}` + "\n"
	if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadExamples([]string{file})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 examples, got %d", len(got))
	}
	// Sorted by type: call.summary.v2 comes first.
	if got[0].Type != "call.summary.v2" || !strings.Contains(string(got[0].Input), `"transcript":"Здравствуйте"`) {
		t.Fatalf("old call format must become a call.summary.v2 input: %+v", got[0])
	}
	if got[1].Language != "ky" {
		t.Fatalf("language must be read from the input: %+v", got[1])
	}
}

func TestRequireLoopback(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:8095": true, "localhost:8095": true, "[::1]:8095": true,
		"0.0.0.0:8095": false, ":8095": false, "192.168.1.5:8095": false,
	} {
		if err := requireLoopback(addr); (err == nil) != ok {
			t.Errorf("%s: want ok=%v, got %v", addr, ok, err)
		}
	}
}

func TestRunForwardsWithFreshTokenAndIdempotencyKey(t *testing.T) {
	var seen *http.Request
	var seenBody []byte
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		seenBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"id":"0f8e2c1a-1111-4111-8111-111111111111","status":"queued"}`)
	}))
	defer gw.Close()
	s := &server{gateway: gw.URL, tenant: "playground", key: []byte(testKey), iss: "crm-platform",
		aud: "crm-ai-platform", client: gw.Client()}
	page := httptest.NewServer(s.routes())
	defer page.Close()

	resp, err := http.Post(page.URL+"/api/run", "application/json",
		strings.NewReader(`{"type":"call.summary.v2","input":{"transcript":"x","language":"ru"}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || seen == nil || seen.URL.Path != "/v1/jobs" {
		t.Fatalf("the job must be created at the gateway: status %d, request %+v", resp.StatusCode, seen)
	}
	if !strings.HasPrefix(seen.Header.Get("Idempotency-Key"), "playground-") {
		t.Fatalf("an idempotency key is required by the gateway: %q", seen.Header.Get("Idempotency-Key"))
	}
	tok := strings.TrimPrefix(seen.Header.Get("Authorization"), "Bearer ")
	v, _ := auth.NewVerifier([][]byte{[]byte(testKey)}, "crm-platform", "crm-ai-platform", 15*time.Minute)
	p, err := v.Verify(tok)
	if err != nil || p.Tenant != "playground" {
		t.Fatalf("token must be valid for the playground tenant: %+v %v", p, err)
	}
	var body map[string]any
	if json.Unmarshal(seenBody, &body) != nil || body["type"] != "call.summary.v2" {
		t.Fatalf("type and input must be forwarded: %s", seenBody)
	}

	bad, _ := http.Post(page.URL+"/api/run", "application/json", strings.NewReader(`{"type":"x","input":"not an object"}`))
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("a non-object input must be rejected before the gateway, got %d", bad.StatusCode)
	}
	traversal, _ := http.Get(page.URL + "/api/jobs/..%2F..%2Fv1%2Fjob-types")
	traversal.Body.Close()
	if traversal.StatusCode != http.StatusBadRequest {
		t.Fatalf("only job ids may be forwarded, got %d", traversal.StatusCode)
	}
}

func TestPageAndExamplesAreServed(t *testing.T) {
	s := &server{cases: []example{{ID: "a", Type: "call.summary.v2", Input: json.RawMessage(`{}`)}}}
	page := httptest.NewServer(s.routes())
	defer page.Close()
	r, _ := http.Get(page.URL + "/")
	html, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(html), "Песочница AI Platform") {
		t.Fatal("the page must be served at /")
	}
	r, _ = http.Get(page.URL + "/api/examples")
	var got []example
	_ = json.NewDecoder(r.Body).Decode(&got)
	r.Body.Close()
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("examples must be listed: %+v", got)
	}
}
