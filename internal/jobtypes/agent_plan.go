package jobtypes

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"crmai/internal/agentplan"
)

// AgentPlanV1 turns a user's request into proposed CRM actions (capability:
// agent runtime). Nothing is executed here; the CRM asks the user to confirm
// each action and then runs it with the user's own rights.
func AgentPlanV1() Definition {
	return Definition{
		Type:         "agent.plan.v1",
		Capability:   "agent-runtime",
		Task:         "agent_plan",
		InputSchema:  "agent_plan.input.v1",
		OutputSchema: "agent_plan.output.v1",
		MaxAttempts:  3,
		Timeout:      10 * time.Minute, // see CallSummaryV1 for the measured speed
		Ground:       groundAgentPlan,
	}
}

// AgentPlanV2 adds the schedule_meeting and move_deal_stage tools. v1 stays
// unchanged for clients that do not offer them.
func AgentPlanV2() Definition {
	d := AgentPlanV1()
	d.Type, d.Task = "agent.plan.v2", "agent_plan_v2"
	d.InputSchema, d.OutputSchema = "agent_plan.input.v2", "agent_plan.output.v2"
	return d
}

// AgentPlanV3 is v2 with the earlier turns of the conversation, so the user can
// refine a proposal ("better on Tuesday"). The output contract is v2's.
func AgentPlanV3() Definition {
	d := AgentPlanV2()
	d.Type, d.Task, d.InputSchema = "agent.plan.v3", "agent_plan_v3", "agent_plan.input.v3"
	return d
}

func groundAgentPlan(input, output json.RawMessage) error {
	in, err := agentplan.Parse(input)
	if err != nil {
		return errors.New("input unreadable")
	}
	var out struct {
		Actions          []agentplan.Action `json:"actions"`
		InsufficientData bool               `json:"insufficient_data"`
		Language         string             `json:"language"`
	}
	if err := json.Unmarshal(output, &out); err != nil {
		return errors.New("output unreadable")
	}
	if out.Language != in.Language {
		return errors.New("output language differs from input language")
	}
	if out.InsufficientData != (len(out.Actions) == 0) {
		return errors.New("insufficient_data must be set exactly when no action is proposed")
	}
	for i, a := range out.Actions {
		if problem := in.Check(a); problem != "" {
			return fmt.Errorf("action #%d %s", i, problem)
		}
	}
	return nil
}
