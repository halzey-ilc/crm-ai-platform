package mock

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"crmai/internal/agentplan"
)

// TaskAgentPlan proposes CRM actions for a request. The mock matches keyword
// stems and targets the first record the user referred to; it is a fixture.
const TaskAgentPlan = "agent_plan"

var (
	taskStems     = []string{"задач", "напомн", "перезвон", "позвон", "тапшырма", "эскерт", "task", "remind", "call back"}
	noteStems     = []string{"заметк", "запиши", "запис", "жазып", "note", "write down"}
	briefStems    = []string{"бриф", "подготов", "даярда", "brief", "prepare"}
	tomorrowStems = []string{"завтра", "эртең", "tomorrow"}
)

type agentOut struct {
	Schema               string             `json:"schema"`
	Summary              string             `json:"summary"`
	Actions              []agentplan.Action `json:"actions"`
	InsufficientData     bool               `json:"insufficient_data"`
	RequiresConfirmation bool               `json:"requires_confirmation"`
	Language             string             `json:"language"`
	Generator            generator          `json:"generator"`
}

func agentPlan(raw json.RawMessage) (json.RawMessage, error) {
	in, err := agentplan.Parse(raw)
	if err != nil {
		return nil, errors.New("mock: input is not an agent_plan input")
	}
	out := agentOut{
		Schema: "agent_plan.output.v1", Actions: []agentplan.Action{}, RequiresConfirmation: true,
		Language: in.Language, Generator: generator{Provider: Name, Model: Model, IsMock: true},
	}
	request := strings.TrimSpace(in.Request)
	lower := strings.ToLower(request)
	if len(in.Subjects) > 0 {
		s := in.Subjects[0]
		if in.Allows("create_task") && containsAny(lower, taskStems) {
			a := agentplan.Action{Tool: "create_task", SubjectRef: s.Ref, Title: truncate(request, 200), Reason: truncate(request, 300)}
			if today, err := time.Parse(time.DateOnly, in.Today); err == nil && containsAny(lower, tomorrowStems) {
				due := today.AddDate(0, 0, 1).Format(time.DateOnly)
				a.DueDate = &due
			}
			out.Actions = append(out.Actions, a)
		}
		if in.Allows("log_note") && containsAny(lower, noteStems) {
			out.Actions = append(out.Actions, agentplan.Action{Tool: "log_note", SubjectRef: s.Ref, Body: truncate(request, 2000), Reason: truncate(request, 300)})
		}
		if in.Allows("request_deal_brief") && s.Kind == "deal" && containsAny(lower, briefStems) {
			out.Actions = append(out.Actions, agentplan.Action{Tool: "request_deal_brief", SubjectRef: s.Ref, Reason: truncate(request, 300)})
		}
	}
	t, ok := agentplan.Summaries[in.Language]
	if !ok {
		t = agentplan.Summaries["ru"]
	}
	if len(out.Actions) == 0 {
		out.Summary, out.InsufficientData = t.None, true
	} else {
		out.Summary = sprintf(t.Some, len(out.Actions))
	}
	return json.Marshal(out)
}
