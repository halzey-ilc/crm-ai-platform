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

func TestMessageReplyDropsDraftWithInventedFacts(t *testing.T) {
	input := `{"language":"ru","channel":"whatsapp","messages":[
	 {"ref":"m1","direction":"inbound","text":"Здравствуйте, сколько стоит доставка в Ош?"},
	 {"ref":"m2","direction":"outbound","text":"Добрый день, уточняю."},
	 {"ref":"m3","direction":"inbound","text":"Жду ответа"}]}`
	answer := `{"intent":"price_request","urgency":"normal",
	 "evidence":{"message_ref":"m1","quote":"сколько  СТОИТ доставка в Ош"},
	 "draft_reply":"Доставка в Ош стоит 300 сом.","needs_human":false}`
	var seen map[string]any
	srv := fakeServer(t, 200, answer, &seen)
	resp, err := invoke(t, srv.URL, "message_reply", input)
	if err != nil {
		t.Fatal(err)
	}
	schemas, _ := contracts.Load()
	if err := schemas.Validate("message_reply.output.v1", resp.Output); err != nil {
		t.Fatalf("contract: %v\n%s", err, resp.Output)
	}
	out := string(resp.Output)
	if !strings.Contains(out, `"draft_reply":null`) || !strings.Contains(out, `"needs_human":true`) {
		t.Fatalf("a draft with an invented price must be withheld: %s", out)
	}
	if !strings.Contains(out, `"quote":"сколько стоит доставка в Ош"`) {
		t.Fatalf("the quote must be the verbatim span of the message: %s", out)
	}
	schema, _ := json.Marshal(seen["response_format"])
	if !strings.Contains(string(schema), `"enum":["m1","m3"]`) {
		t.Fatalf("evidence must be limited to customer messages: %s", schema)
	}

	invented := `{"intent":"order","urgency":"normal","evidence":{"message_ref":"m3","quote":"Хочу купить 5 штук"},"draft_reply":"Хорошо","needs_human":true}`
	srv2 := fakeServer(t, 200, invented, nil)
	if _, err := invoke(t, srv2.URL, "message_reply", input); !modelgw.IsTransient(err) {
		t.Fatalf("an intent without evidence must be resampled, got %v", err)
	}
}

func TestDealCoachDropsUngroundedPointsAndSkipsModelWhenEmpty(t *testing.T) {
	input := `{"language":"ru","today":"2026-10-29",
	 "deal":{"ref":"deal:1","title":"Поставка","status":"open","stage":"КП","stage_position":2,"stage_count":4,
	         "probability":40,"amount":1000,"currency":"USD","expected_close_date":null,"age_days":12},
	 "history":[{"ref":"activity:a","kind":"note","date":"2026-10-20","title":null,"text":"Просят доставку до конца месяца.","status":null}]}`
	answer := `{"situation":[{"text":"Сделка на этапе КП, вероятность 40%.","refs":["deal:1"]},
	                         {"text":"Клиент готов платить 1200 USD.","refs":["deal:1"]}],
	 "risks":[{"text":"Сжатые сроки доставки.","refs":["activity:a"]}],
	 "questions":[{"text":"Какой адрес доставки?","refs":["activity:a"]}],
	 "next_step":{"text":"Согласовать дату доставки.","refs":["activity:a"]}}`
	var seen map[string]any
	srv := fakeServer(t, 200, answer, &seen)
	resp, err := invoke(t, srv.URL, "deal_coach", input)
	if err != nil {
		t.Fatal(err)
	}
	schemas, _ := contracts.Load()
	if err := schemas.Validate("deal_coach.output.v1", resp.Output); err != nil {
		t.Fatalf("contract: %v\n%s", err, resp.Output)
	}
	out := string(resp.Output)
	if strings.Contains(out, "1200") || !strings.Contains(out, "вероятность 40%") {
		t.Fatalf("the point with an invented number must be dropped: %s", out)
	}
	schema, _ := json.Marshal(seen["response_format"])
	if !strings.Contains(string(schema), `"enum":["deal:1","activity:a"]`) {
		t.Fatalf("refs must be constrained to the input: %s", schema)
	}

	called := false
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv2.Close()
	empty := strings.Replace(input, input[strings.Index(input, `"history":[`):], `"history":[]}`, 1)
	resp, err = invoke(t, srv2.URL, "deal_coach", empty)
	if err != nil || called || !strings.Contains(string(resp.Output), `"insufficient_data":true`) {
		t.Fatalf("a deal without history must not call the model: called=%v err=%v %s", called, err, resp.Output)
	}
}

func TestRiskReviewDropsAccusationsAndReplacesHeadline(t *testing.T) {
	input := `{"language":"ru","window_days":30,
	 "counts":{"won_without_activity":1,"reopened_after_close":0,"amount_outlier":0,"bulk_disqualification":1,"deals_lost_burst":0,"invoices_voided":0},
	 "signals":[{"ref":"deal:a","kind":"won_without_activity","value":0,"threshold":0},
	            {"ref":"user:u","kind":"bulk_disqualification","value":14,"threshold":10}]}`
	answer := `{"headline":"Обнаружено мошенничество!",
	 "items":[{"kind":"bulk_disqualification","text":"Сотрудник нарушает правила работы с лидами.","signal_refs":["user:u"]},
	          {"kind":"won_without_activity","text":"Сделка выиграна без записанных контактов.","signal_refs":["deal:a","user:u"]}],
	 "checks":[{"text":"Уточните, где записаны контакты.","signal_refs":["deal:a"]}]}`
	srv := fakeServer(t, 200, answer, nil)
	resp, err := invoke(t, srv.URL, "risk_review", input)
	if err != nil {
		t.Fatal(err)
	}
	schemas, _ := contracts.Load()
	if err := schemas.Validate("risk_review.output.v1", resp.Output); err != nil {
		t.Fatalf("contract: %v\n%s", err, resp.Output)
	}
	out := string(resp.Output)
	if strings.Contains(out, "мошенн") || strings.Contains(out, "наруш") {
		t.Fatalf("accusatory texts must not be kept: %s", out)
	}
	if !strings.Contains(out, `"signal_refs":["deal:a"]`) || !strings.Contains(out, "без записанных контактов") {
		t.Fatalf("the neutral item must stay, with refs of its kind only: %s", out)
	}
}

func TestKpiExplanationDropsInventedNumbers(t *testing.T) {
	input := `{"language":"en","period":{"type":"month","start":"2026-10-01","end":"2026-11-01"},
	 "previous_period":{"type":"month","start":"2026-09-01","end":"2026-10-01"},
	 "metrics":[{"code":"revenue_won","name":"Revenue","unit":"money","current":1250,"previous":1000,"change":250,"change_pct":25}]}`
	answer := `{"headline":"Revenue up 25%",
	 "observations":[{"metric_codes":["revenue_won"],"text":"Revenue rose to 1250 from 1000 (+25%)."},
	                 {"metric_codes":["revenue_won"],"text":"Average deal grew to 417."}],
	 "recommendations":[{"metric_codes":["revenue_won"],"text":"Keep the 3 best campaigns."}]}`
	var seen map[string]any
	srv := fakeServer(t, 200, answer, &seen)
	resp, err := invoke(t, srv.URL, "kpi_explanation", input)
	if err != nil {
		t.Fatal(err)
	}
	schemas, _ := contracts.Load()
	if err := schemas.Validate("kpi_explanation.output.v1", resp.Output); err != nil {
		t.Fatalf("contract: %v\n%s", err, resp.Output)
	}
	out := string(resp.Output)
	if strings.Contains(out, "417") || strings.Contains(out, "3 best") {
		t.Fatalf("items with invented numbers must be dropped: %s", out)
	}
	if !strings.Contains(out, "1250 from 1000") {
		t.Fatalf("the grounded observation must stay: %s", out)
	}
	schema, _ := json.Marshal(seen["response_format"])
	if !strings.Contains(string(schema), `"enum":["revenue_won"]`) {
		t.Fatalf("metric codes must be constrained to the input: %s", schema)
	}
}
