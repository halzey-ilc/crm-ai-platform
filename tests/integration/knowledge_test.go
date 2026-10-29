//go:build integration

package integration

import (
	"bytes"
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
	"crmai/internal/knowledge"
	"crmai/internal/store/postgres"
	"crmai/internal/testutil"
)

const (
	salesPolicy = "Скидка для новых клиентов составляет не более 10 процентов. Скидку утверждает руководитель отдела продаж."
	hrPolicy    = "Отпуск согласуется с руководителем за две недели. Скидка сотрудникам на продукцию компании не предусмотрена."
)

func put(t *testing.T, tenant, ref, text string, tags ...string) {
	t.Helper()
	if _, _, err := store.PutDocument(context.Background(), tenant, postgres.Document{
		Ref: ref, Title: ref, Language: "ru", Tags: tags, Text: text, CreatedBy: "user:it",
	}); err != nil {
		t.Fatal(err)
	}
}

func refs(ps []knowledge.Passage) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Ref)
	}
	return strings.Join(out, ",")
}

func TestKnowledgeSearchFiltersByAccessBeforeSearch(t *testing.T) {
	ctx := context.Background()
	tenant, other := newID("kb"), newID("kb")
	put(t, tenant, "sales-policy", salesPolicy, "role:manager", "role:sales_rep")
	put(t, tenant, "hr-policy", hrPolicy, "role:hr")
	put(t, other, "foreign", salesPolicy, "all")

	q := knowledge.Query("Какая скидка для новых клиентов?")
	rep, err := store.Search(ctx, tenant, []string{"role:sales_rep", "user:u1"}, q, 8)
	if err != nil {
		t.Fatal(err)
	}
	if refs(rep) != "sales-policy#0" {
		t.Fatalf("a sales rep must find only the sales policy, got %q", refs(rep))
	}
	hr, _ := store.Search(ctx, tenant, []string{"role:hr"}, q, 8)
	if refs(hr) != "hr-policy#0" {
		t.Fatalf("HR must find only the HR policy (it also mentions a discount), got %q", refs(hr))
	}
	nobody, _ := store.Search(ctx, tenant, []string{"user:someone"}, q, 8)
	if len(nobody) != 0 {
		t.Fatalf("no shared tag, no passages: %q", refs(nobody))
	}
	// Another tenant's document tagged "all" stays invisible.
	all, _ := store.Search(ctx, tenant, []string{"all", "role:manager"}, q, 8)
	if strings.Contains(refs(all), "foreign") {
		t.Fatal("another tenant's documents must never be found")
	}

	// Re-sending the same content changes nothing; new tags apply at once.
	if _, changed, _ := store.PutDocument(ctx, tenant, postgres.Document{Ref: "sales-policy", Title: "sales-policy",
		Language: "ru", Tags: []string{"role:manager"}, Text: salesPolicy, CreatedBy: "user:it"}); changed {
		t.Fatal("unchanged content must not be re-indexed")
	}
	if rep, _ := store.Search(ctx, tenant, []string{"role:sales_rep"}, q, 8); len(rep) != 0 {
		t.Fatal("removing a tag must remove access immediately")
	}
	if err := store.DeleteDocument(ctx, tenant, "sales-policy"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteDocument(ctx, tenant, "sales-policy"); err != postgres.ErrDocumentNotFound {
		t.Fatalf("second delete: want not found, got %v", err)
	}
}

func TestKnowledgeAskThroughHTTP(t *testing.T) {
	types := testutil.Registry(t)
	v, _ := auth.NewVerifier([][]byte{signingKey}, "crm-platform", "crm-ai-platform", 10*time.Minute)
	srv := httptest.NewServer(httpapi.NewHandler(httpapi.Deps{
		Jobs: jobs.NewService(store, types), Types: types, Verifier: v, Knowledge: store, Ready: store.Ready,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), RequestTimeout: 5 * time.Second,
		MaxBodyBytes: 1 << 20, RateLimitRPS: 1000, RateLimitBurst: 1000,
	}))
	defer srv.Close()
	tenant := newID("kbhttp")
	token := func(scopes ...string) string {
		tok, _ := auth.Mint(auth.MintRequest{Key: signingKey, Issuer: "crm-platform", Audience: "crm-ai-platform",
			Subject: "user:it", Tenant: tenant, Scopes: scopes, TTL: time.Minute})
		return tok
	}
	call := func(tok, method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Idempotency-Key", "kb-key-"+randomHex(4))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	reader := token(auth.ScopeJobsCreate, auth.ScopeJobsRead)
	writer := token(auth.ScopeKnowledgeWrite)
	doc, _ := json.Marshal(map[string]any{"title": "Скидки", "language": "ru", "access_tags": []string{"role:manager"}, "text": salesPolicy})

	if code, _ := call(reader, "PUT", "/v1/knowledge/documents/sales-policy", string(doc)); code != 403 {
		t.Fatalf("writing documents needs ai.knowledge.write, got %d", code)
	}
	if code, out := call(writer, "PUT", "/v1/knowledge/documents/sales-policy", string(doc)); code != 200 || out["passages"] != float64(1) {
		t.Fatalf("put: %d %v", code, out)
	}
	bad := strings.Replace(string(doc), "role:manager", "admin", 1)
	if code, _ := call(writer, "PUT", "/v1/knowledge/documents/x", bad); code != 400 {
		t.Fatalf("unknown tag form must be rejected, got %d", code)
	}

	ask := `{"question":"Какая скидка для новых клиентов?","language":"ru","access_tags":["role:manager"]}`
	code, job := call(reader, "POST", "/v1/knowledge/ask", ask)
	if code != 202 {
		t.Fatalf("ask: %d %v", code, job)
	}
	id := job["id"].(string)
	w := newWorker(t, newID("kbw"))
	w.Process(context.Background(), claimJob(t, w, id, time.Minute))
	_, got := call(reader, "GET", "/v1/jobs/"+id, "")
	out, _ := json.Marshal(got["result"].(map[string]any)["output"])
	if !strings.Contains(string(out), `"passage_ref":"sales-policy#0"`) || !strings.Contains(string(out), `"insufficient_data":false`) {
		t.Fatalf("the answer must quote the sales policy: %s", out)
	}

	// The same question with tags that see nothing: an honest "not found".
	_, job = call(reader, "POST", "/v1/knowledge/ask", strings.Replace(ask, "role:manager", "role:hr", 1))
	id = job["id"].(string)
	w.Process(context.Background(), claimJob(t, w, id, time.Minute))
	_, got = call(reader, "GET", "/v1/jobs/"+id, "")
	out, _ = json.Marshal(got["result"].(map[string]any)["output"])
	if !strings.Contains(string(out), `"insufficient_data":true`) {
		t.Fatalf("without access the answer must be empty: %s", out)
	}
}
