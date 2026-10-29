package llamacpp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"crmai/internal/agentplan"
)

// TaskAgentPlan proposes CRM actions for a request.
const TaskAgentPlan = "agent_plan"

// TaskAgentPlanV2 also offers schedule_meeting and move_deal_stage.
const TaskAgentPlanV2 = "agent_plan_v2"

const agentSystem = `You are a CRM assistant. Turn the user's request into at most 3 proposed actions,
using ONLY the tools and records listed. You do not execute anything: the user confirms each action.
create_task: a task on a record (title; due_date as YYYY-MM-DD, not before today, or null).
log_note: a note on a record (body: what the user asked to record, without inventing facts).
request_deal_brief: prepare a brief for a deal.
schedule_meeting: a meeting on a record (title; start as YYYY-MM-DDTHH:MM local time, not before today; duration_minutes 15-480).
move_deal_stage: move a deal to another stage of its pipeline (stage: one of the deal's "stages", not its current "stage").
Every action needs a short reason. If the request asks for something these tools cannot do,
or a date or time the user did not give is needed, propose no action rather than guessing.
Today is %s. Write title, body and reason in %s. Answer with JSON only.`

func (p Provider) agentPlan(ctx context.Context, raw json.RawMessage, outputSchema string) (json.RawMessage, error) {
	in, err := agentplan.Parse(raw)
	if err != nil {
		return nil, errors.New("llamacpp: input is not an agent_plan input")
	}
	refs := make([]string, 0, len(in.Subjects))
	for _, s := range in.Subjects {
		refs = append(refs, s.Ref)
	}
	t, ok := agentplan.Summaries[in.Language]
	if !ok {
		t = agentplan.Summaries["ru"]
	}
	out := struct {
		Schema               string             `json:"schema"`
		Summary              string             `json:"summary"`
		Actions              []agentplan.Action `json:"actions"`
		InsufficientData     bool               `json:"insufficient_data"`
		RequiresConfirmation bool               `json:"requires_confirmation"`
		Language             string             `json:"language"`
		Generator            generator          `json:"generator"`
	}{Schema: outputSchema, Actions: []agentplan.Action{}, RequiresConfirmation: true, Language: in.Language, Generator: p.generator()}
	if len(refs) == 0 {
		// No record to act on: nothing to propose, no model call.
		out.Summary, out.InsufficientData = t.None, true
		return json.Marshal(out)
	}
	ref := map[string]any{"type": "string", "enum": refs}
	reason := map[string]any{"type": "string", "minLength": 1, "maxLength": 300}
	variants := []any{}
	for _, tool := range in.Tools {
		props := map[string]any{"tool": map[string]any{"const": tool}, "subject_ref": ref, "reason": reason}
		required := []string{"tool", "subject_ref", "reason"}
		switch tool {
		case "create_task":
			props["title"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 200}
			props["due_date"] = map[string]any{"anyOf": []any{map[string]any{"type": "null"}, map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`}}}
			required = append(required, "title", "due_date")
		case "log_note":
			props["body"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 1000}
			required = append(required, "body")
		case "schedule_meeting":
			props["title"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 200}
			props["start"] = map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$`}
			props["duration_minutes"] = map[string]any{"type": "integer", "minimum": agentplan.MinMeetingMinutes, "maximum": agentplan.MaxMeetingMinutes}
			required = append(required, "title", "start", "duration_minutes")
		case "move_deal_stage":
			// The grammar allows the stages of the listed deals; Check ties a stage to its deal.
			var stages []string
			seen := map[string]bool{}
			for _, s := range in.Subjects {
				for _, st := range s.Stages {
					if !seen[st] {
						seen[st] = true
						stages = append(stages, st)
					}
				}
			}
			if len(stages) == 0 {
				continue
			}
			props["stage"] = map[string]any{"type": "string", "enum": stages}
			required = append(required, "stage")
		}
		variants = append(variants, map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": props})
	}
	schema := map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"actions"},
		"properties": map[string]any{
			"actions": map[string]any{"type": "array", "maxItems": 3, "items": map[string]any{"anyOf": variants}},
		},
	}
	user, _ := json.Marshal(map[string]any{"request": in.Request, "tools": in.Tools, "records": in.Subjects})
	var ans struct {
		Actions []agentplan.Action `json:"actions"`
	}
	if err := p.complete(ctx, fmt.Sprintf(agentSystem, in.Today, languageName(in.Language)), "Request:\n"+string(user), schema, 500, &ans); err != nil {
		return nil, err
	}
	dropped := 0
	for _, a := range ans.Actions {
		a.Title, a.Body, a.Reason = strings.TrimSpace(a.Title), strings.TrimSpace(a.Body), strings.TrimSpace(a.Reason)
		if in.Check(a) != "" || (a.Tool == "create_task" && a.Title == "") || (a.Tool == "log_note" && a.Body == "") {
			dropped++
			continue
		}
		out.Actions = append(out.Actions, a)
	}
	if dropped > 0 && p.Log != nil {
		p.Log.Info("llm actions dropped", "count", dropped)
	}
	if len(out.Actions) == 0 {
		out.Summary, out.InsufficientData = t.None, true
	} else {
		out.Summary = fmt.Sprintf(t.Some, len(out.Actions))
	}
	return json.Marshal(out)
}
