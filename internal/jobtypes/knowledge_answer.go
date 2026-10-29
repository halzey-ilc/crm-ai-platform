package jobtypes

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"crmai/internal/knowledge"
)

// KnowledgeAnswerV1 answers a question from company documents (capability:
// knowledge assistant). The gateway builds the input from the passages the
// asker may see; the answer consists only of statements that quote a passage.
func KnowledgeAnswerV1() Definition {
	return Definition{
		Type:         "knowledge.answer.v1",
		Capability:   "knowledge-assistant",
		Task:         "knowledge_answer",
		InputSchema:  "knowledge_answer.input.v1",
		OutputSchema: "knowledge_answer.output.v1",
		MaxAttempts:  3,
		Timeout:      10 * time.Minute, // see CallSummaryV1 for the measured speed
		Ground:       groundKnowledgeAnswer,
	}
}

func groundKnowledgeAnswer(input, output json.RawMessage) error {
	var in knowledge.Input
	if err := json.Unmarshal(input, &in); err != nil {
		return errors.New("input unreadable")
	}
	var out struct {
		Statements       []knowledge.Statement `json:"statements"`
		InsufficientData bool                  `json:"insufficient_data"`
		Language         string                `json:"language"`
	}
	if err := json.Unmarshal(output, &out); err != nil {
		return errors.New("output unreadable")
	}
	if out.Language != in.Language {
		return errors.New("output language differs from input language")
	}
	if out.InsufficientData != (len(out.Statements) == 0) {
		return errors.New("insufficient_data must be set exactly when there is no statement")
	}
	for i, s := range out.Statements {
		if problem := in.Check(s); problem != "" {
			return fmt.Errorf("statement #%d %s", i, problem)
		}
	}
	return nil
}
