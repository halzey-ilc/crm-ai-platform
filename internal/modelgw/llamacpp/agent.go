package llamacpp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"crmai/internal/agentplan"
)

// TaskAgentPlan proposes CRM actions for a request.
const TaskAgentPlan = "agent_plan"

// TaskAgentPlanV2 also offers schedule_meeting and move_deal_stage.
const TaskAgentPlanV2 = "agent_plan_v2"

// TaskAgentPlanV3 is v2 with the earlier turns of the conversation.
const TaskAgentPlanV3 = "agent_plan_v3"

const agentSystem = `You are a CRM assistant. Turn the user's request into at most 3 proposed actions,
using ONLY the tools and records listed. You do not execute anything: the user confirms each action.
create_task: a task on a record (title; due_date as YYYY-MM-DD, not before today, or null).
log_note: a note on a record, only when the user asks to record something (body: what the user asked to record, without inventing facts).
request_deal_brief: prepare a brief for a deal.
schedule_meeting: a meeting on a record (title; start as YYYY-MM-DDTHH:MM local time, not before today; duration_minutes 15-480).
move_deal_stage: move a deal to another stage of its pipeline (stage: one of the deal's "stages", not its current "stage").
First answer can_do: true only if these tools do what the user asks; false if the user asks
for something no listed tool does (for example deleting, sending or paying) or a date or time
the user did not give is needed. With can_do false, actions is []. Never use log_note to record
a request you cannot carry out. Every action needs a short reason.
"earlier_requests" and "already_proposed", when present, are the earlier turns of this
conversation. Use them only to understand the latest request ("it", "the same time", "better on
Tuesday") and propose actions for the latest request alone. Never propose again anything listed
in "already_proposed" unless the latest request changes it.
"dates" gives the exact date of each day word in the request: copy those dates, never count
days yourself. Today is %s. Calendar:
%s
Write title, body and reason in %s. Answer with JSON only.`

// calendar lists the next 14 days with their weekdays. Models miscount weekdays
// ("до пятницы" became Saturday in 3 of 3 runs); a lookup table does not.
func calendar(today string) string {
	t, err := time.Parse("2006-01-02", today)
	if err != nil {
		return ""
	}
	lines := make([]string, 0, 15)
	for i := 0; i <= 14; i++ {
		d := t.AddDate(0, 0, i)
		lines = append(lines, d.Format("2006-01-02")+" "+d.Weekday().String())
	}
	return strings.Join(lines, "\n")
}

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
		// "tool" goes first: with keys sorted, every variant began with "reason" or
		// a tool-specific field, so the model committed to a tool before naming it
		// and always ended up in request_deal_brief (MODEL_EVALUATION §7).
		props := ordered{{"tool", map[string]any{"const": tool}}, {"subject_ref", ref}}
		switch tool {
		case "create_task":
			props = append(props,
				field{"title", map[string]any{"type": "string", "minLength": 1, "maxLength": 200}},
				field{"due_date", map[string]any{"anyOf": []any{map[string]any{"type": "null"}, map[string]any{"type": "string", "pattern": `^[0-9]{4}-[0-9]{2}-[0-9]{2}$`}}}})
		case "log_note":
			props = append(props, field{"body", map[string]any{"type": "string", "minLength": 1, "maxLength": 1000}})
		case "schedule_meeting":
			// [0-9], not \d: the llama.cpp grammar converter rejects \d and accepts any string.
			props = append(props,
				field{"title", map[string]any{"type": "string", "minLength": 1, "maxLength": 200}},
				field{"start", map[string]any{"type": "string", "pattern": `^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}$`}},
				field{"duration_minutes", map[string]any{"type": "integer", "minimum": agentplan.MinMeetingMinutes, "maximum": agentplan.MaxMeetingMinutes}})
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
			props = append(props, field{"stage", map[string]any{"type": "string", "enum": stages}})
		}
		props = append(props, field{"reason", reason})
		required := make([]string, len(props))
		for i, f := range props {
			required[i] = f.Key
		}
		variants = append(variants, map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": props})
	}
	// can_do comes first so the model decides whether the request is possible before writing
	// actions; a prompt rule alone still let it log an impossible request as a note (§7).
	schema := map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"can_do", "actions"},
		"properties": ordered{
			{"can_do", map[string]any{"type": "boolean"}},
			{"actions", map[string]any{"type": "array", "maxItems": 3, "items": map[string]any{"anyOf": variants}}},
		},
	}
	prompt := map[string]any{"request": in.Request, "tools": in.Tools, "records": in.Subjects}
	if hints := agentplan.DateHints(in.Today, in.Request); len(hints) > 0 {
		prompt["dates"] = hints
	}
	// Split by role: in one mixed list the model re-proposed what it had already
	// proposed (3 of 3 runs of dialog-en-01).
	var asked, proposed []string
	for _, t := range in.History {
		if t.Role == "assistant" {
			proposed = append(proposed, t.Text)
		} else {
			asked = append(asked, t.Text)
		}
	}
	if len(asked) > 0 {
		prompt["earlier_requests"] = asked
	}
	if len(proposed) > 0 {
		prompt["already_proposed"] = proposed
	}
	user, _ := json.Marshal(prompt)
	var ans struct {
		CanDo   bool               `json:"can_do"`
		Actions []agentplan.Action `json:"actions"`
	}
	system := fmt.Sprintf(agentSystem, in.Today, calendar(in.Today), languageName(in.Language))
	if err := p.complete(ctx, system, "Request:\n"+string(user), schema, 500, &ans); err != nil {
		return nil, err
	}
	if !ans.CanDo {
		// The grammar cannot tie actions to can_do; we do.
		ans.Actions = nil
	}
	dropped := 0
	for _, a := range ans.Actions {
		a.Title, a.Body, a.Reason = strings.TrimSpace(a.Title), strings.TrimSpace(a.Body), strings.TrimSpace(a.Reason)
		// A repeat of an earlier proposal: the prompt rule alone failed in 3 of 3 runs.
		if in.Check(a) != "" || (a.Tool == "create_task" && a.Title == "") || (a.Tool == "log_note" && a.Body == "") ||
			agentplan.AlreadyProposed(in.History, a) || !agentplan.Requested(a.Tool, in.Request) {
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
