package mock

import (
	"encoding/json"
	"errors"
	"strings"

	"crmai/internal/knowledge"
)

// TaskKnowledgeAnswer answers from passages. The mock quotes the first sentence
// of each passage that contains a word of the question; it is a fixture.
const TaskKnowledgeAnswer = "knowledge_answer"

func knowledgeAnswer(raw json.RawMessage) (json.RawMessage, error) {
	var in knowledge.Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errors.New("mock: input is not a knowledge_answer input")
	}
	stems := knowledge.Stems(in.Question)
	statements := []knowledge.Statement{}
	for _, p := range in.Passages {
		if len(statements) == 3 {
			break
		}
		for _, s := range Sentences(p.Text) {
			if containsAny(strings.ToLower(s), stems) {
				q := truncate(s, 500)
				statements = append(statements, knowledge.Statement{Text: q, PassageRef: p.Ref, Quote: q})
				break
			}
		}
	}
	return json.Marshal(map[string]any{
		"schema": "knowledge_answer.output.v1", "statements": statements,
		"insufficient_data": len(statements) == 0, "language": in.Language,
		"generator": generator{Provider: Name, Model: Model, IsMock: true},
	})
}
