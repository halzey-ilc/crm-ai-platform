package llamacpp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"crmai/internal/dealcoach"
	"crmai/internal/modelgw"
)

// TaskDealCoach prepares a deal brief.
const TaskDealCoach = "deal_coach"

const coachSystem = `You prepare a sales manager for the next conversation with a client about a deal.
Use ONLY the deal data and history given. Do not invent facts, names, prices, dates or agreements,
and do not calculate new numbers. Every point cites in "refs" the records it is based on
(the deal ref or history refs); never write refs inside "text".
situation: up to 3 short points on where the deal stands.
risks: up to 3 concrete risks visible in the data (e.g. a passed close date, overdue tasks, no planned step, objections in notes).
questions: up to 4 questions the manager should clarify with the client, based on gaps or open points in the history.
next_step: one concrete next action, based on the history, or null.
Write all texts in %s. Answer with JSON only.`

func (p Provider) dealCoach(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	in, err := dealcoach.Parse(raw)
	if err != nil {
		return nil, errors.New("llamacpp: input is not a deal_coach input")
	}
	out := struct {
		Schema           string            `json:"schema"`
		Situation        []dealcoach.Point `json:"situation"`
		Risks            []dealcoach.Point `json:"risks"`
		Questions        []dealcoach.Point `json:"questions"`
		NextStep         *dealcoach.Point  `json:"next_step"`
		InsufficientData bool              `json:"insufficient_data"`
		Language         string            `json:"language"`
		Generator        generator         `json:"generator"`
	}{
		Schema: "deal_coach.output.v1", Situation: []dealcoach.Point{}, Risks: []dealcoach.Point{},
		Questions: []dealcoach.Point{}, Language: in.Language, Generator: p.generator(),
	}
	if in.Insufficient() {
		// Nothing to coach on: state the facts without asking the model (principle 7, ADR-005).
		out.Situation = []dealcoach.Point{{Text: emptyCoach[in.Language], Refs: []string{in.Deal.Ref}}}
		out.InsufficientData = true
		return json.Marshal(out)
	}
	user, _ := json.Marshal(map[string]any{"today": in.Today, "deal": in.Deal, "history": in.History})
	if utf8.RuneCount(user) > maxTranscriptRunes {
		return nil, fmt.Errorf("llamacpp: deal history longer than %d characters does not fit the model context", maxTranscriptRunes)
	}
	point := func(max int) map[string]any {
		return map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"text", "refs"},
			"properties": map[string]any{
				"text": map[string]any{"type": "string", "minLength": 1, "maxLength": max},
				"refs": map[string]any{
					"type": "array", "minItems": 1, "maxItems": 3, "uniqueItems": true,
					"items": map[string]any{"type": "string", "enum": in.Refs()},
				},
			},
		}
	}
	list := func(n int) map[string]any { return map[string]any{"type": "array", "maxItems": n, "items": point(300)} }
	schema := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"situation", "risks", "questions", "next_step"},
		"properties": map[string]any{
			"situation": map[string]any{"type": "array", "minItems": 1, "maxItems": 3, "items": point(300)},
			"risks":     list(3),
			"questions": list(4),
			"next_step": map[string]any{"anyOf": []any{map[string]any{"type": "null"}, point(300)}},
		},
	}
	var ans struct {
		Situation []dealcoach.Point `json:"situation"`
		Risks     []dealcoach.Point `json:"risks"`
		Questions []dealcoach.Point `json:"questions"`
		NextStep  *dealcoach.Point  `json:"next_step"`
	}
	if err := p.complete(ctx, fmt.Sprintf(coachSystem, languageName(in.Language)), "Deal:\n"+string(user), schema, 800, &ans); err != nil {
		return nil, err
	}
	known, allowed := in.Known(), in.Allowed()
	dropped := 0
	keep := func(points []dealcoach.Point) []dealcoach.Point {
		kept := []dealcoach.Point{}
		for _, pt := range points {
			pt.Text = strings.TrimSpace(pt.Text)
			if dealcoach.Check(pt, known, allowed) == "" {
				kept = append(kept, pt)
			} else {
				dropped++
			}
		}
		return kept
	}
	out.Situation, out.Risks, out.Questions = keep(ans.Situation), keep(ans.Risks), keep(ans.Questions)
	if ans.NextStep != nil {
		if k := keep([]dealcoach.Point{*ans.NextStep}); len(k) == 1 {
			out.NextStep = &k[0]
		}
	}
	if dropped > 0 && p.Log != nil {
		// Points with invented numbers are not evidence-based: only grounded ones are kept.
		p.Log.Info("llm points dropped as ungrounded", "count", dropped)
	}
	if len(out.Situation) == 0 {
		return nil, modelgw.Transient(errors.New("llamacpp: no grounded situation point"))
	}
	return json.Marshal(out)
}

var emptyCoach = map[string]string{
	"ru": "Недостаточно данных: по сделке нет истории.",
	"ky": "Маалымат жетишсиз: бүтүм боюнча тарых жок.",
	"en": "Not enough data: the deal has no history.",
}
