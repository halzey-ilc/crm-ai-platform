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

func agentInputV2(request string, tools ...string) string {
	b, _ := json.Marshal(map[string]any{
		"language": "ru", "today": "2026-10-29", "request": request, "tools": tools,
		"subjects": []map[string]any{{
			"ref": dealRef, "kind": "deal", "title": "Внедрение CRM",
			"stage": "Переговоры", "stages": []string{"Новая", "Переговоры", "Договор", "Выиграна"},
		}},
	})
	return string(b)
}

func runAgentV2(t *testing.T, gw *modelgw.Gateway, input string) (jobs.Result, error) {
	t.Helper()
	return testutil.Registry(t).Execute(context.Background(), gw, jobs.Job{
		ID: "a2", TenantID: "acme", Type: "agent.plan.v2", Input: json.RawMessage(input),
	})
}

func TestMockAgentV2ProposesMeetingAndStage(t *testing.T) {
	gw, _ := modelgw.New(map[string]modelgw.Provider{"agent_plan_v2": mock.Provider{}})
	res, err := runAgentV2(t, gw, agentInputV2("Назначь встречу с клиентом и переведи сделку на следующий этап",
		"schedule_meeting", "move_deal_stage"))
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Output)
	for _, want := range []string{`"tool":"schedule_meeting"`, `"start":"2026-10-30T10:00"`, `"duration_minutes":60`,
		`"tool":"move_deal_stage"`, `"stage":"Договор"`, `"schema":"agent_plan.output.v2"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s: %s", want, out)
		}
	}
}

func TestAgentV2GroundingRejectsBadMeetingsAndStages(t *testing.T) {
	tail := `"insufficient_data":false,"requires_confirmation":true,"language":"ru","generator":{"provider":"fixed","model":"m","is_mock":false}}`
	plan := func(action string) string {
		return `{"schema":"agent_plan.output.v2","summary":"s","actions":[` + action + `],` + tail
	}
	meeting := func(start string, minutes int) string {
		b, _ := json.Marshal(map[string]any{"tool": "schedule_meeting", "subject_ref": dealRef, "title": "Демо",
			"start": start, "duration_minutes": minutes, "reason": "r"})
		return string(b)
	}
	cases := map[string]string{
		"meeting in the past":   plan(meeting("2026-10-01T10:00", 60)),
		"meeting in two years":  plan(meeting("2028-11-01T10:00", 60)),
		"stage not in pipeline": plan(`{"tool":"move_deal_stage","subject_ref":"` + dealRef + `","stage":"Архив","reason":"r"}`),
		"same stage":            plan(`{"tool":"move_deal_stage","subject_ref":"` + dealRef + `","stage":"Переговоры","reason":"r"}`),
		"tool not offered":      plan(`{"tool":"log_note","subject_ref":"` + dealRef + `","body":"b","reason":"r"}`),
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			gw, _ := modelgw.New(map[string]modelgw.Provider{"agent_plan_v2": fixed{out}})
			_, err := runAgentV2(t, gw, agentInputV2("x", "schedule_meeting", "move_deal_stage"))
			var p *jobtypes.PermanentError
			if !jobtypesIsPermanent(err, &p) || p.Code != "output_not_grounded" {
				t.Fatalf("want output_not_grounded, got %v", err)
			}
		})
	}
	// A meeting of 10 minutes violates the output contract itself.
	gw, _ := modelgw.New(map[string]modelgw.Provider{"agent_plan_v2": fixed{plan(meeting("2026-10-30T10:00", 10))}})
	if _, err := runAgentV2(t, gw, agentInputV2("x", "schedule_meeting")); !jobtypes.IsPermanent(err) {
		t.Fatalf("a 10-minute meeting must be rejected, got %v", err)
	}
	// v1 inputs cannot offer the new tools.
	if err := testutil.Registry(t).ValidateInput("agent.plan.v1", json.RawMessage(agentInput("x", "schedule_meeting"))); err == nil {
		t.Fatal("agent.plan.v1 must not accept the v2 tools")
	}
}
