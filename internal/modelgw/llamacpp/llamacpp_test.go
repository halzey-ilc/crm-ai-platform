package llamacpp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"crmai/contracts"
	"crmai/internal/modelgw"
	"crmai/internal/modelgw/llamacpp"
)

const transcript = "Здравствуйте, это Айбек. Клиенту нужна интеграция с 1С. Договорились, что я перезвоню в пятницу."

// fakeServer answers /v1/chat/completions with the given content and records the request.
func fakeServer(t *testing.T, status int, content string, seen *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if seen != nil {
			_ = json.NewDecoder(r.Body).Decode(seen)
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 50},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func invoke(t *testing.T, url, task, input string) (modelgw.Response, error) {
	t.Helper()
	p := llamacpp.Provider{BaseURL: url, Model: "test-gguf"}
	return p.Invoke(context.Background(), modelgw.Request{Task: task, Input: json.RawMessage(input)})
}

func callInput(lang string) string {
	b, _ := json.Marshal(map[string]string{"transcript": transcript, "language": lang})
	return string(b)
}

func TestCallSummaryKeepsOnlyGroundedItems(t *testing.T) {
	answer := `{"summary":"Клиенту нужна интеграция, перезвон в пятницу.",
	 "key_points":[
	   {"text":"Нужна интеграция с 1С","evidence_quote":"Клиенту нужна интеграция с 1С."},
	   {"text":"Регистр изменён моделью","evidence_quote":"клиенту  НУЖНА интеграция"},
	   {"text":"Выдумано","evidence_quote":"Клиент готов заплатить 1 млн"}],
	 "next_step":{"text":"Перезвонить в пятницу","evidence_quote":"Договорились, что я перезвоню в пятницу."}}`
	var seen map[string]any
	srv := fakeServer(t, 200, answer, &seen)
	resp, err := invoke(t, srv.URL, "call_summary", callInput("ru"))
	if err != nil {
		t.Fatal(err)
	}
	schemas, _ := contracts.Load()
	if err := schemas.Validate("call_summary.output.v1", resp.Output); err != nil {
		t.Fatalf("output violates the public contract: %v\n%s", err, resp.Output)
	}
	var out struct {
		KeyPoints []struct {
			EvidenceQuote string `json:"evidence_quote"`
		} `json:"key_points"`
		Generator struct {
			Provider string `json:"provider"`
			IsMock   bool   `json:"is_mock"`
		} `json:"generator"`
	}
	_ = json.Unmarshal(resp.Output, &out)
	if len(out.KeyPoints) != 2 {
		t.Fatalf("the invented key point must be dropped, got %s", resp.Output)
	}
	for _, kp := range out.KeyPoints {
		if !strings.Contains(transcript, kp.EvidenceQuote) {
			t.Fatalf("stored quote must be a verbatim span of the text: %q", kp.EvidenceQuote)
		}
	}
	if out.Generator.Provider != "llamacpp" || out.Generator.IsMock {
		t.Fatalf("provenance must name the real provider: %+v", out.Generator)
	}
	// The request asks for schema-constrained JSON without the thinking phase.
	if seen["response_format"].(map[string]any)["type"] != "json_schema" {
		t.Fatal("response_format must be json_schema")
	}
	if seen["chat_template_kwargs"].(map[string]any)["enable_thinking"] != false {
		t.Fatal("thinking must be disabled")
	}
	system := seen["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, "Russian") {
		t.Fatal("the answer language must follow the input language")
	}
}

func TestDigestRestrictsRefsAndSkipsModelWhenEmpty(t *testing.T) {
	input := `{"language":"ru","thresholds":{"stale_lead_days":3,"stalled_deal_days":14},
	 "counts":{"stale_lead":1,"overdue_task":1,"deal_without_next_step":0,"stalled_deal":0},
	 "findings":[{"ref":"lead:a","kind":"stale_lead","age_days":9},{"ref":"task:b","kind":"overdue_task","age_days":2}]}`
	answer := `{"headline":"Требуют внимания: 2.",
	 "items":[{"kind":"stale_lead","text":"Лид без движения 9 дн.","finding_refs":["lead:a","task:b"]},
	          {"kind":"overdue_task","text":"Просрочена задача","finding_refs":["task:b"]}],
	 "recommendations":[{"text":"Связаться с лидом","finding_refs":["lead:a"]}]}`
	var seen map[string]any
	srv := fakeServer(t, 200, answer, &seen)
	resp, err := invoke(t, srv.URL, "operations_digest", input)
	if err != nil {
		t.Fatal(err)
	}
	schemas, _ := contracts.Load()
	if err := schemas.Validate("operations_digest.output.v1", resp.Output); err != nil {
		t.Fatalf("contract: %v\n%s", err, resp.Output)
	}
	if strings.Contains(string(resp.Output), `"finding_refs":["lead:a","task:b"]`) {
		t.Fatal("a ref of another kind must be removed from an item")
	}
	// The grammar sent to the model only allows refs from the input.
	schema, _ := json.Marshal(seen["response_format"])
	if !strings.Contains(string(schema), `"enum":["lead:a","task:b"]`) {
		t.Fatalf("refs must be constrained to the input: %s", schema)
	}

	called := false
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv2.Close()
	empty := `{"language":"en","thresholds":{"stale_lead_days":3,"stalled_deal_days":14},
	 "counts":{"stale_lead":0,"overdue_task":0,"deal_without_next_step":0,"stalled_deal":0},"findings":[]}`
	resp, err = invoke(t, srv2.URL, "operations_digest", empty)
	if err != nil || called || !strings.Contains(string(resp.Output), `"insufficient_data":true`) {
		t.Fatalf("empty digest must not call the model: called=%v err=%v %s", called, err, resp.Output)
	}
}

func TestFailureClassification(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		content   string
		transient bool
	}{
		{"model loading", 503, "", true},
		{"server error", 500, "", true},
		{"not json", 200, "Sure! Here is the summary...", true},
		{"rejected request", 400, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := fakeServer(t, tc.status, tc.content, nil)
			_, err := invoke(t, srv.URL, "call_summary", callInput("ru"))
			if err == nil || modelgw.IsTransient(err) != tc.transient {
				t.Fatalf("want transient=%v, got %v", tc.transient, err)
			}
		})
	}
	_, err := invoke(t, "http://127.0.0.1:1", "call_summary", callInput("ru"))
	if !modelgw.IsTransient(err) {
		t.Fatalf("unreachable server must be transient, got %v", err)
	}
	long, _ := json.Marshal(map[string]string{"transcript": strings.Repeat("а", 13000), "language": "ru"})
	if _, err := invoke(t, "http://unused", "call_summary", string(long)); err == nil || modelgw.IsTransient(err) {
		t.Fatalf("a transcript beyond the context window is a permanent error, got %v", err)
	}
}
