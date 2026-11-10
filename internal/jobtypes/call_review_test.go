package jobtypes_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"crmai/internal/jobs"
	"crmai/internal/modelgw"
	"crmai/internal/modelgw/mock"
	"crmai/internal/testutil"
)

const reviewInput = `{"language":"ru","call_ref":"call:1",
 "segments":[
  {"id":"s1","speaker":"manager","text":"Здравствуйте, меня зовут Айбек, компания Техносфера."},
  {"id":"s2","speaker":"customer","text":"Добрый день, нам нужна интеграция с 1С."},
  {"id":"s3","speaker":"manager","text":"Расскажите, какие задачи вы хотите решить и сколько у вас сотрудников?"},
  {"id":"s4","speaker":"customer","text":"Двадцать человек, хотим видеть продажи."}],
 "criteria":[
  {"code":"greeting","title":"Здравствуйте и имя"},
  {"code":"needs","title":"Вопросы о задачах клиента","description":"Выявляет потребности"},
  {"code":"next_step","title":"Договорённость о следующем шаге"}]}`

func runReview(t *testing.T, input string) (jobs.Result, error) {
	t.Helper()
	gw, err := modelgw.New(map[string]modelgw.Provider{"call_review": mock.Provider{}})
	if err != nil {
		t.Fatal(err)
	}
	return testutil.Registry(t).Execute(context.Background(), gw, jobs.Job{
		ID: "r1", TenantID: "acme", Type: "call.review.v1", Input: json.RawMessage(input),
	})
}

func TestMockReviewQuotesTheTranscript(t *testing.T) {
	res, err := runReview(t, reviewInput)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var out struct {
		Criteria []struct {
			Code, Status string
			Evidence     *struct{ SegmentID, Quote string } `json:"evidence"`
		}
		InsufficientData bool `json:"insufficient_data"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out.InsufficientData || len(out.Criteria) != 3 || out.Criteria[0].Code != "greeting" {
		t.Fatalf("unexpected review: %s", res.Output)
	}
	if out.Criteria[0].Status != "met" || out.Criteria[0].Evidence == nil {
		t.Fatalf("greeting is in s1: %s", res.Output)
	}
	if out.Criteria[2].Status != "missed" || out.Criteria[2].Evidence != nil {
		t.Fatalf("no next step was agreed: %s", res.Output)
	}
}

func TestReviewOfATooShortCallJudgesNothing(t *testing.T) {
	short := `{"language":"ru","segments":[{"id":"s1","speaker":"customer","text":"Алло?"}],"criteria":[{"code":"greeting","title":"Приветствие"}]}`
	res, err := runReview(t, short)
	if err != nil || !strings.Contains(string(res.Output), `"insufficient_data":true`) || !strings.Contains(string(res.Output), `"not_applicable"`) {
		t.Fatalf("a call without a manager turn must not be judged: %v %s", err, res.Output)
	}
}

func TestReviewGroundingRejectsUncheckableVerdicts(t *testing.T) {
	gen := `"insufficient_data":false,"language":"ru","generator":{"provider":"x","model":"y","is_mock":false}}`
	head := `{"schema":"call_review.output.v1","strengths":[],"improvements":[],`
	needs := `{"code":"needs","status":"missed","evidence":null,"comment":"c"}`
	next := `{"code":"next_step","status":"missed","evidence":null,"comment":"c"}`
	cases := map[string]string{
		"invented quote": head + `"criteria":[{"code":"greeting","status":"met","evidence":{"segment_id":"s1","quote":"рады вас слышать"},"comment":"c"},` + needs + `,` + next + `],` + gen,
		"no evidence":    head + `"criteria":[{"code":"greeting","status":"met","evidence":null,"comment":"c"},` + needs + `,` + next + `],` + gen,
		"wrong order":    head + `"criteria":[` + needs + `,{"code":"greeting","status":"missed","evidence":null,"comment":"c"},` + next + `],` + gen,
		"missing one":    head + `"criteria":[` + needs + `,` + next + `],` + gen,
	}
	ground := testutil.Registry(t)
	for name, output := range cases {
		def, _ := ground.Get("call.review.v1")
		if err := def.Ground(json.RawMessage(reviewInput), json.RawMessage(output)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ok := head + `"criteria":[{"code":"greeting","status":"met","evidence":{"segment_id":"s1","quote":"МЕНЯ  зовут Айбек"},"comment":"c"},` + needs + `,` + next + `],` + gen
	def, _ := ground.Get("call.review.v1")
	if err := def.Ground(json.RawMessage(reviewInput), json.RawMessage(ok)); err != nil {
		t.Errorf("a quote differing only in case and spaces must pass: %v", err)
	}
}
