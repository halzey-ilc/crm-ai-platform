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

const (
	dealRef     = "deal:11111111-1111-4111-8111-111111111111"
	customerRef = "customer:22222222-2222-4222-8222-222222222222"
)

func agentInput(request string, tools ...string) string {
	b, _ := json.Marshal(map[string]any{
		"language": "ru", "today": "2026-10-29", "request": request, "tools": tools,
		"subjects": []map[string]string{
			{"ref": dealRef, "kind": "deal", "title": "Внедрение CRM"},
			{"ref": customerRef, "kind": "customer", "title": "ОсОО Арча"},
		},
	})
	return string(b)
}

func runAgent(t *testing.T, gw *modelgw.Gateway, input string) (jobs.Result, error) {
	t.Helper()
	return testutil.Registry(t).Execute(context.Background(), gw, jobs.Job{
		ID: "a1", TenantID: "acme", Type: "agent.plan.v1", Input: json.RawMessage(input),
	})
}

func TestMockAgentProposesOnlyOfferedTools(t *testing.T) {
	gw, _ := modelgw.New(map[string]modelgw.Provider{"agent_plan": mock.Provider{}})
	res, err := runAgent(t, gw, agentInput("Поставь задачу перезвонить клиенту завтра и подготовь бриф", "create_task", "request_deal_brief"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := string(res.Output)
	for _, want := range []string{`"tool":"create_task"`, `"due_date":"2026-10-30"`, `"tool":"request_deal_brief"`, `"requires_confirmation":true`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s: %s", want, out)
		}
	}
	// A note was asked for, but log_note was not offered to this user.
	res, err = runAgent(t, gw, agentInput("Запиши заметку: клиент просит скидку", "create_task"))
	if err != nil || !strings.Contains(string(res.Output), `"insufficient_data":true`) {
		t.Fatalf("a tool that was not offered must not be proposed: %v %s", err, res.Output)
	}
}

func TestAgentGroundingRejectsUnofferedToolsAndRecords(t *testing.T) {
	tail := `"insufficient_data":false,"requires_confirmation":true,"language":"ru","generator":{"provider":"fixed","model":"m","is_mock":false}}`
	plan := func(action string) string {
		return `{"schema":"agent_plan.output.v1","summary":"s","actions":[` + action + `],` + tail
	}
	cases := map[string]string{
		"tool not offered":   plan(`{"tool":"log_note","subject_ref":"` + dealRef + `","body":"b","reason":"r"}`),
		"unknown record":     plan(`{"tool":"create_task","subject_ref":"deal:99999999-9999-4999-8999-999999999999","title":"t","due_date":null,"reason":"r"}`),
		"brief for customer": plan(`{"tool":"request_deal_brief","subject_ref":"` + customerRef + `","reason":"r"}`),
		"due in the past":    plan(`{"tool":"create_task","subject_ref":"` + dealRef + `","title":"t","due_date":"2026-01-01","reason":"r"}`),
		"due in 2 years":     plan(`{"tool":"create_task","subject_ref":"` + dealRef + `","title":"t","due_date":"2028-11-01","reason":"r"}`),
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			gw, _ := modelgw.New(map[string]modelgw.Provider{"agent_plan": fixed{out}})
			_, err := runAgent(t, gw, agentInput("x", "create_task", "request_deal_brief"))
			var p *jobtypes.PermanentError
			if !jobtypesIsPermanent(err, &p) || p.Code != "output_not_grounded" {
				t.Fatalf("want output_not_grounded, got %v", err)
			}
		})
	}
}
