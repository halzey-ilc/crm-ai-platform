package llamacpp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"crmai/internal/modelgw"
	"crmai/internal/riskreview"
)

// TaskRiskReview explains risk signals.
const TaskRiskReview = "risk_review"

const riskSystem = `You explain CRM risk signals to a company owner.
The signals were computed by fixed company rules; the numbers are correct and must not be changed.
A signal is only a reason to check. NEVER say or imply that an employee cheated, stole, committed fraud
or broke rules, and never judge people: describe what the data shows and what to verify.
Kinds: won_without_activity = a deal won with no recorded note, task, call or meeting;
reopened_after_close = a closed deal was reopened; amount_outlier = an open deal far above the median
(value = times the median); bulk_disqualification = one person disqualified many leads on one day;
deals_lost_burst = one person marked many deals lost on one day; invoices_voided = one person voided
several invoices. For each kind present write one short neutral item and one concrete check.
Cite up to 3 refs from the input in signal_refs; never write refs inside "text" or "headline".
Write all texts in %s. Answer with JSON only.`

var emptyRiskHeadline = map[string]string{
	"ru": "Сигналов по правилам компании нет.",
	"ky": "Компаниянын эрежелери боюнча сигналдар жок.",
	"en": "No signals under the company's rules.",
}

func (p Provider) riskReview(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	in, err := riskreview.Parse(raw)
	if err != nil {
		return nil, errors.New("llamacpp: input is not a risk_review input")
	}
	out := struct {
		Schema           string             `json:"schema"`
		Headline         string             `json:"headline"`
		Items            []riskreview.Cited `json:"items"`
		Checks           []riskreview.Cited `json:"checks"`
		InsufficientData bool               `json:"insufficient_data"`
		IsEvidence       bool               `json:"is_evidence"`
		Language         string             `json:"language"`
		Generator        generator          `json:"generator"`
	}{
		Schema: "risk_review.output.v1", Items: []riskreview.Cited{}, Checks: []riskreview.Cited{},
		Language: in.Language, Generator: p.generator(),
	}
	if in.Insufficient() {
		out.Headline, out.InsufficientData = emptyRiskHeadline[in.Language], true
		return json.Marshal(out)
	}
	var kinds, refs []string
	seenKind, seenRef := map[string]bool{}, map[string]bool{}
	for _, s := range in.Signals {
		if !seenKind[s.Kind] {
			seenKind[s.Kind] = true
			kinds = append(kinds, s.Kind)
		}
		if !seenRef[s.Ref] {
			seenRef[s.Ref] = true
			refs = append(refs, s.Ref)
		}
	}
	sort.Strings(kinds)
	refsSchema := map[string]any{
		"type": "array", "minItems": 1, "maxItems": 3, "uniqueItems": true,
		"items": map[string]any{"type": "string", "enum": refs},
	}
	text := map[string]any{"type": "string", "minLength": 1, "maxLength": 400}
	schema := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"headline", "items", "checks"},
		"properties": map[string]any{
			"headline": map[string]any{"type": "string", "minLength": 1, "maxLength": 300},
			"items": map[string]any{
				"type": "array", "minItems": 1, "maxItems": len(kinds),
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{"kind", "text", "signal_refs"},
					"properties": map[string]any{
						"kind": map[string]any{"type": "string", "enum": kinds}, "text": text, "signal_refs": refsSchema,
					},
				},
			},
			"checks": map[string]any{
				"type": "array", "maxItems": len(kinds),
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required":   []string{"text", "signal_refs"},
					"properties": map[string]any{"text": text, "signal_refs": refsSchema},
				},
			},
		},
	}
	user, _ := json.Marshal(map[string]any{"window_days": in.WindowDays, "counts": in.Counts, "signals": in.Signals})
	var ans struct {
		Headline string             `json:"headline"`
		Items    []riskreview.Cited `json:"items"`
		Checks   []riskreview.Cited `json:"checks"`
	}
	if err := p.complete(ctx, fmt.Sprintf(riskSystem, languageName(in.Language)), "Signals:\n"+string(user), schema, 800, &ans); err != nil {
		return nil, err
	}
	allowed, kindsOf := in.Allowed(), in.KindsOf()
	out.Headline = strings.TrimSpace(ans.Headline)
	if riskreview.Problem(out.Headline, allowed) != "" {
		// A headline with an invented number or a judgement is replaced, not kept.
		out.Headline = neutralRiskHeadline[in.Language]
	}
	dropped := 0
	used := map[string]bool{}
	for _, it := range ans.Items {
		var keep []string
		for _, r := range it.SignalRefs {
			if kindsOf[r][it.Kind] { // the grammar cannot tie refs to their kind; we do
				keep = append(keep, r)
			}
		}
		it.Text = strings.TrimSpace(it.Text)
		if len(keep) == 0 || used[it.Kind] || riskreview.Problem(it.Text, allowed) != "" {
			dropped++
			continue
		}
		used[it.Kind] = true
		out.Items = append(out.Items, riskreview.Cited{Kind: it.Kind, Text: it.Text, SignalRefs: keep})
	}
	for _, c := range ans.Checks {
		c.Text = strings.TrimSpace(c.Text)
		if riskreview.Problem(c.Text, allowed) != "" {
			dropped++
			continue
		}
		out.Checks = append(out.Checks, riskreview.Cited{Text: c.Text, SignalRefs: c.SignalRefs})
	}
	if dropped > 0 && p.Log != nil {
		p.Log.Info("llm risk texts dropped", "count", dropped)
	}
	if len(out.Items) == 0 {
		return nil, modelgw.Transient(errors.New("llamacpp: no acceptable risk item"))
	}
	return json.Marshal(out)
}

var neutralRiskHeadline = map[string]string{
	"ru": "Сигналы для проверки. Это не доказательство ошибок сотрудников.",
	"ky": "Текшерүү үчүн сигналдар. Бул кызматкерлердин катасынын далили эмес.",
	"en": "Signals to check. They are not proof of anyone's mistake.",
}
