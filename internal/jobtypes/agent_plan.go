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
