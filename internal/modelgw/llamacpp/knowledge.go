package llamacpp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"crmai/internal/knowledge"
	"crmai/internal/modelgw"
)

// TaskKnowledgeAnswer answers a question from company document passages.
const TaskKnowledgeAnswer = "knowledge_answer"

const knowledgeSystem = `You answer an employee's question using ONLY the company document passages given.
Give up to 4 statements. Each statement: "text" (a short answer in %s), "passage_ref" (the ref of the
passage it comes from) and "quote" (a fragment copied EXACTLY from that passage that supports it).
Do not add facts, numbers or advice that are not in the passages. If the passages do not answer the
question, return an empty list. Answer with JSON only.`

func (p Provider) knowledgeAnswer(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var in knowledge.Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errors.New("llamacpp: input is not a knowledge_answer input")
	}
	out := map[string]any{
		"schema": "knowledge_answer.output.v1", "statements": []knowledge.Statement{},
		"insufficient_data": true, "language": in.Language, "generator": p.generator(),
	}
	if len(in.Passages) == 0 {
		// Nothing the asker may see matched: no model call.
		return json.Marshal(out)
	}
	var b strings.Builder
	refs := make([]string, 0, len(in.Passages))
	for _, ps := range in.Passages {
		refs = append(refs, ps.Ref)
		fmt.Fprintf(&b, "[%s] %s\n%s\n\n", ps.Ref, ps.Title, ps.Text)
	}
	if utf8.RuneCountInString(b.String()) > maxTranscriptRunes {
		return nil, fmt.Errorf("llamacpp: passages longer than %d characters do not fit the model context", maxTranscriptRunes)
	}
	schema := map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"statements"},
		"properties": map[string]any{
			"statements": map[string]any{
				"type": "array", "maxItems": 4,
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{"text", "passage_ref", "quote"},
					"properties": map[string]any{
						"text":        map[string]any{"type": "string", "minLength": 1, "maxLength": 400},
						"passage_ref": map[string]any{"type": "string", "enum": refs},
						"quote":       map[string]any{"type": "string", "minLength": 3, "maxLength": 400},
					},
				},
			},
		},
	}
	var ans struct {
		Statements []knowledge.Statement `json:"statements"`
	}
	user := "Question: " + in.Question + "\n\nPassages:\n" + b.String()
	if err := p.complete(ctx, fmt.Sprintf(knowledgeSystem, languageName(in.Language)), user, schema, 700, &ans); err != nil {
		return nil, err
	}
	kept := []knowledge.Statement{}
	for _, s := range ans.Statements {
		ps, ok := in.Passage(s.PassageRef)
		if !ok {
			continue
		}
		if q, ok := anchor(ps.Text, s.Quote); ok && strings.TrimSpace(s.Text) != "" {
			kept = append(kept, knowledge.Statement{Text: strings.TrimSpace(s.Text), PassageRef: ps.Ref, Quote: q})
		}
	}
	if dropped := len(ans.Statements) - len(kept); dropped > 0 && p.Log != nil {
		p.Log.Info("llm statements dropped as ungrounded", "count", dropped)
	}
	if len(ans.Statements) > 0 && len(kept) == 0 {
		// The model answered but nothing was quoted correctly: sample again.
		return nil, modelgw.Transient(errors.New("llamacpp: no statement quoted its passage"))
	}
	out["statements"], out["insufficient_data"] = kept, len(kept) == 0
	return json.Marshal(out)
}
