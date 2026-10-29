package jobtypes_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"crmai/internal/jobs"
	"crmai/internal/jobtypes"
	"crmai/internal/modelgw"
	"crmai/internal/testutil"
)

const replyInput = `{"language":"ru","channel":"whatsapp","messages":[
 {"ref":"m1","direction":"inbound","text":"Здравствуйте! Интересует тариф Бизнес на 10 пользователей."},
 {"ref":"m2","direction":"outbound","text":"Добрый день! Тариф Бизнес стоит 4500 сом в месяц."},
 {"ref":"m3","direction":"inbound","text":"Хорошо. Хочу оформить заказ, что нужно сделать?"}]}`

func runReply(t *testing.T, gw *modelgw.Gateway, input string) (jobs.Result, error) {
	t.Helper()
	return testutil.Registry(t).Execute(context.Background(), gw, jobs.Job{
		ID: "r1", TenantID: "acme", Type: "message.reply.v1", Input: json.RawMessage(input),
	})
}

func TestMockReplyClassifiesAndDraftsWithoutFacts(t *testing.T) {
	cases := map[string]struct{ input, intent, urgency string }{
		"order": {replyInput, "order", "normal"},
		"complaint": {`{"language":"ru","channel":"telegram","messages":[
		  {"ref":"a","direction":"inbound","text":"Ваш сервис не работает уже второй день. Срочно решите!"}]}`, "complaint", "high"},
		"thanks": {`{"language":"en","channel":"email","messages":[
		  {"ref":"a","direction":"inbound","text":"Thank you for the quick help"}]}`, "gratitude", "low"},
		"kyrgyz price": {`{"language":"ky","channel":"whatsapp","messages":[
		  {"ref":"a","direction":"inbound","text":"Саламатсызбы. Бул тарифтин баасы канча?"}]}`, "price_request", "normal"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := runReply(t, gateway(t), tc.input)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			var out struct {
				Intent     string  `json:"intent"`
				Urgency    string  `json:"urgency"`
				DraftReply *string `json:"draft_reply"`
				NeedsHuman bool    `json:"needs_human"`
			}
			_ = json.Unmarshal(res.Output, &out)
			if out.Intent != tc.intent || out.Urgency != tc.urgency {
				t.Fatalf("want %s/%s, got %s", tc.intent, tc.urgency, res.Output)
			}
			if out.DraftReply == nil || out.NeedsHuman == (tc.intent == "gratitude") {
				t.Fatalf("draft and needs_human wrong: %s", res.Output)
			}
		})
	}
}

func TestReplyGroundingRejectsInventedFacts(t *testing.T) {
	gen := `"language":"ru","generator":{"provider":"fixed","model":"m","is_mock":false}`
	ev := `"evidence":{"message_ref":"m3","quote":"Хочу оформить заказ"}`
	cases := map[string]string{
		"invented price":      `{"schema":"message_reply.output.v1","intent":"order","urgency":"normal",` + ev + `,"draft_reply":"Итого 5000 сом.","needs_human":false,` + gen + `}`,
		"invented link":       `{"schema":"message_reply.output.v1","intent":"order","urgency":"normal",` + ev + `,"draft_reply":"Оплатите на https://pay.example.com","needs_human":false,` + gen + `}`,
		"quote not in text":   `{"schema":"message_reply.output.v1","intent":"order","urgency":"normal","evidence":{"message_ref":"m3","quote":"Беру сразу три"},"draft_reply":"Хорошо","needs_human":false,` + gen + `}`,
		"company message ref": `{"schema":"message_reply.output.v1","intent":"price_request","urgency":"normal","evidence":{"message_ref":"m2","quote":"Тариф Бизнес"},"draft_reply":"Хорошо","needs_human":false,` + gen + `}`,
		"intent without ev":   `{"schema":"message_reply.output.v1","intent":"order","urgency":"normal","evidence":null,"draft_reply":"Хорошо","needs_human":false,` + gen + `}`,
		"complaint unhandled": `{"schema":"message_reply.output.v1","intent":"complaint","urgency":"high",` + ev + `,"draft_reply":"Хорошо","needs_human":false,` + gen + `}`,
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			gw, _ := modelgw.New(map[string]modelgw.Provider{"message_reply": fixed{out}})
			_, err := runReply(t, gw, replyInput)
			var p *jobtypes.PermanentError
			if !jobtypesIsPermanent(err, &p) || p.Code != "output_not_grounded" {
				t.Fatalf("want output_not_grounded, got %v", err)
			}
		})
	}
	// Restating a price the company already quoted is allowed.
	ok := `{"schema":"message_reply.output.v1","intent":"order","urgency":"normal",` + ev + `,"draft_reply":"Тариф Бизнес — 4500 сом в месяц, менеджер пришлёт счёт.","needs_human":true,` + gen + `}`
	gw, _ := modelgw.New(map[string]modelgw.Provider{"message_reply": fixed{ok}})
	if _, err := runReply(t, gw, replyInput); err != nil {
		t.Fatalf("a price from the conversation must be accepted: %v", err)
	}
}

func TestReplyInputExcludesContactDetails(t *testing.T) {
	reg := testutil.Registry(t)
	bad := strings.Replace(replyInput, `"channel":"whatsapp"`, `"channel":"whatsapp","contact":"+996555123456"`, 1)
	if err := reg.ValidateInput("message.reply.v1", json.RawMessage(bad)); err == nil {
		t.Fatal("contact identifiers are not part of the contract")
	}
}
