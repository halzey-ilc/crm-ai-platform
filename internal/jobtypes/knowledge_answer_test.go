package jobtypes_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"crmai/internal/jobs"
	"crmai/internal/jobtypes"
	"crmai/internal/modelgw"
	"crmai/internal/modelgw/mock"
	"crmai/internal/testutil"
)

const kbInput = `{"language":"ru","question":"Какая скидка для новых клиентов?","passages":[
 {"ref":"sales-policy#0","title":"Скидки","text":"Скидка для новых клиентов составляет не более 10 процентов. Скидку утверждает руководитель."}]}`

func runKB(t *testing.T, gw *modelgw.Gateway, input string) (jobs.Result, error) {
	t.Helper()
	return testutil.Registry(t).Execute(context.Background(), gw, jobs.Job{
		ID: "kb1", TenantID: "acme", Type: "knowledge.answer.v1", Input: json.RawMessage(input),
	})
}

func TestMockKnowledgeAnswerQuotesPassages(t *testing.T) {
	gw, _ := modelgw.New(map[string]modelgw.Provider{"knowledge_answer": mock.Provider{}})
	res, err := runKB(t, gw, kbInput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Output), `"passage_ref":"sales-policy#0"`) {
		t.Fatalf("expected a cited statement: %s", res.Output)
	}
	res, err = runKB(t, gw, `{"language":"en","question":"discount?","passages":[]}`)
	if err != nil || !strings.Contains(string(res.Output), `"insufficient_data":true`) {
		t.Fatalf("no passages must give insufficient_data: %v %s", err, res.Output)
	}
}

func TestKnowledgeGroundingRejectsUnquotedStatements(t *testing.T) {
	gen := `"language":"ru","generator":{"provider":"fixed","model":"m","is_mock":false}}`
	cases := map[string]string{
		"unknown passage":    `{"schema":"knowledge_answer.output.v1","statements":[{"text":"10%","passage_ref":"hr#0","quote":"не более 10 процентов"}],"insufficient_data":false,` + gen,
		"invented quote":     `{"schema":"knowledge_answer.output.v1","statements":[{"text":"15%","passage_ref":"sales-policy#0","quote":"не более 15 процентов"}],"insufficient_data":false,` + gen,
		"empty but answered": `{"schema":"knowledge_answer.output.v1","statements":[],"insufficient_data":false,` + gen,
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			gw, _ := modelgw.New(map[string]modelgw.Provider{"knowledge_answer": fixed{out}})
			_, err := runKB(t, gw, kbInput)
			var p *jobtypes.PermanentError
			if !jobtypesIsPermanent(err, &p) || p.Code != "output_not_grounded" {
				t.Fatalf("want output_not_grounded, got %v", err)
			}
		})
	}
}
