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

const riskInput = `{"language":"ru","window_days":30,
 "counts":{"won_without_activity":1,"reopened_after_close":0,"amount_outlier":1,"bulk_disqualification":1,"deals_lost_burst":0,"invoices_voided":0},
 "signals":[{"ref":"deal:a","kind":"won_without_activity","value":0,"threshold":0},
            {"ref":"deal:b","kind":"amount_outlier","value":7.5,"threshold":5},
            {"ref":"user:u","kind":"bulk_disqualification","value":14,"threshold":10}]}`

func runRisk(t *testing.T, gw *modelgw.Gateway, input string) (jobs.Result, error) {
	t.Helper()
	return testutil.Registry(t).Execute(context.Background(), gw, jobs.Job{
		ID: "k1", TenantID: "acme", Type: "risk.review.v1", Input: json.RawMessage(input),
	})
}

func TestMockRiskReviewIsNeutralAndGrounded(t *testing.T) {
	gw, _ := modelgw.New(map[string]modelgw.Provider{"risk_review": mock.Provider{}})
	res, err := runRisk(t, gw, riskInput)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := string(res.Output)
	if !strings.Contains(out, `"is_evidence":false`) || !strings.Contains(out, `"signal_refs":["user:u"]`) {
		t.Fatalf("unexpected output: %s", out)
	}
	empty := `{"language":"en","window_days":30,"counts":{"won_without_activity":0,"reopened_after_close":0,"amount_outlier":0,"bulk_disqualification":0,"deals_lost_burst":0,"invoices_voided":0},"signals":[]}`
	res, err = runRisk(t, gw, empty)
	if err != nil || !strings.Contains(string(res.Output), `"insufficient_data":true`) {
		t.Fatalf("no signals must be reported as such: %v %s", err, res.Output)
	}
}

func TestRiskGroundingRejectsAccusationsAndInventions(t *testing.T) {
	tail := `"insufficient_data":false,"is_evidence":false,"language":"ru","generator":{"provider":"fixed","model":"m","is_mock":false}}`
	item := func(kind, text, ref string) string {
		return `{"schema":"risk_review.output.v1","headline":"Сигналы для проверки.","items":[{"kind":"` + kind + `","text":"` + text + `","signal_refs":["` + ref + `"]}],"checks":[],` + tail
	}
	cases := map[string]string{
		"accusation":        item("bulk_disqualification", "Сотрудник, вероятно, мошенничает с лидами.", "user:u"),
		"english verdict":   item("bulk_disqualification", "This looks like fraud.", "user:u"),
		"invented number":   item("amount_outlier", "Сумма в 12 раз выше обычной.", "deal:b"),
		"ref of other kind": item("amount_outlier", "Сумма намного выше обычной.", "deal:a"),
		"evidence claim":    strings.Replace(item("amount_outlier", "Сумма в 7.5 раза выше медианы.", "deal:b"), `"is_evidence":false`, `"is_evidence":true`, 1),
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			gw, _ := modelgw.New(map[string]modelgw.Provider{"risk_review": fixed{out}})
			_, err := runRisk(t, gw, riskInput)
			var p *jobtypes.PermanentError
			if !jobtypesIsPermanent(err, &p) || (p.Code != "output_not_grounded" && p.Code != "output_contract_violation") {
				t.Fatalf("want rejection, got %v", err)
			}
		})
	}
	ok := item("amount_outlier", "Открытая сделка в 7.5 раза выше медианы (порог 5).", "deal:b")
	gw, _ := modelgw.New(map[string]modelgw.Provider{"risk_review": fixed{ok}})
	if _, err := runRisk(t, gw, riskInput); err != nil {
		t.Fatalf("a neutral grounded item must pass: %v", err)
	}
}
