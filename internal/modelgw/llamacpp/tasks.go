package llamacpp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"crmai/internal/callsummary"
	"crmai/internal/modelgw"
)

// --- call_summary ----------------------------------------------------------------

const callSummarySystem = `You summarise sales call notes and transcripts for a CRM.
Rules:
- Use ONLY facts stated in the text. Never invent names, numbers, prices, dates or agreements.
- key_points: up to 4 of the most important facts (client needs, objections, budget, decisions).
- Every key point and the next step must have evidence_quote: a fragment copied EXACTLY, character for character, from the text (a whole sentence or a clause).
- next_step: the follow-up that was agreed or scheduled: a call back, a meeting, a demo, sending a document. A scheduled meeting IS a next step. Use null only if nothing was agreed.
- Write summary and every "text" field in %s.
Answer with JSON only.`

var groundedSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"text", "evidence_quote"},
	"properties": map[string]any{
		"text":           map[string]any{"type": "string", "minLength": 1, "maxLength": 300},
		"evidence_quote": map[string]any{"type": "string", "minLength": 3, "maxLength": 300},
	},
}

var callSummaryModelSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"summary", "key_points", "next_step"},
	"properties": map[string]any{
		"summary":    map[string]any{"type": "string", "minLength": 1, "maxLength": 600},
		"key_points": map[string]any{"type": "array", "maxItems": 4, "items": groundedSchema},
		"next_step":  map[string]any{"anyOf": []any{map[string]any{"type": "null"}, groundedSchema}},
	},
}

type grounded struct {
	Text          string `json:"text"`
	EvidenceQuote string `json:"evidence_quote"`
}

type callModelAnswer struct {
	Summary   string     `json:"summary"`
	KeyPoints []grounded `json:"key_points"`
	NextStep  *grounded  `json:"next_step"`
}

type callOutput struct {
	Schema    string     `json:"schema"`
	Summary   string     `json:"summary"`
	KeyPoints []grounded `json:"key_points"`
	NextStep  *grounded  `json:"next_step"`
	Language  string     `json:"language"`
	Generator generator  `json:"generator"`
}

func (p Provider) callSummary(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var in struct {
		Transcript string `json:"transcript"`
		Language   string `json:"language"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errors.New("llamacpp: input is not a call_summary input")
	}
	if utf8.RuneCountInString(in.Transcript) > maxTranscriptRunes {
		return nil, fmt.Errorf("llamacpp: transcript longer than %d characters does not fit the model context", maxTranscriptRunes)
	}
	var ans callModelAnswer
	user := "Text of the call:\n<<<\n" + in.Transcript + "\n>>>"
	if err := p.complete(ctx, fmt.Sprintf(callSummarySystem, languageName(in.Language)), user, callSummaryModelSchema, 700, &ans); err != nil {
		return nil, err
	}
	out := callOutput{
		Schema:    "call_summary.output.v1",
		Summary:   strings.TrimSpace(ans.Summary),
		KeyPoints: []grounded{},
		Language:  in.Language,
		Generator: p.generator(),
	}
	if out.Summary == "" {
		return nil, modelgw.Transient(errors.New("llamacpp: empty summary"))
	}
	dropped := 0
	for _, kp := range ans.KeyPoints {
		if q, ok := anchor(in.Transcript, kp.EvidenceQuote); ok && strings.TrimSpace(kp.Text) != "" {
			out.KeyPoints = append(out.KeyPoints, grounded{Text: strings.TrimSpace(kp.Text), EvidenceQuote: q})
		} else {
			dropped++
		}
	}
	if ans.NextStep != nil {
		if q, ok := anchor(in.Transcript, ans.NextStep.EvidenceQuote); ok && strings.TrimSpace(ans.NextStep.Text) != "" {
			out.NextStep = &grounded{Text: strings.TrimSpace(ans.NextStep.Text), EvidenceQuote: q}
		} else {
			dropped++
		}
	}
	if dropped > 0 && p.Log != nil {
		// Items whose quote is not in the text are not evidence: we keep only grounded ones.
		p.Log.Info("llm items dropped as ungrounded", "count", dropped)
	}
	return json.Marshal(out)
}

// TaskCallSummaryV2 is call_summary with insufficient_data.
const TaskCallSummaryV2 = "call_summary_v2"

func (p Provider) callSummaryV2(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var in struct {
		Transcript string `json:"transcript"`
		Language   string `json:"language"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errors.New("llamacpp: input is not a call_summary input")
	}
	if callsummary.TooShort(in.Transcript) {
		// Nothing to summarise: say so without asking the model (principle 7, ADR-005).
		return callsummary.Insufficient(in.Language, p.generator())
	}
	v1, err := p.callSummary(ctx, raw)
	if err != nil {
		return nil, err
	}
	return callsummary.FromV1(v1)
}

// anchor returns the exact span of text that the model's quote refers to.
// Small models often change case or spacing when copying; we accept a
// case-insensitive match with collapsed whitespace and return the original
// span, so the stored quote is always a verbatim substring. Anything else
// (paraphrase, invention) is rejected.
func anchor(text, quote string) (string, bool) {
	quote = strings.TrimSpace(quote)
	if utf8.RuneCountInString(quote) < 3 {
		return "", false
	}
	if strings.Contains(text, quote) {
		return quote, true
	}
	// Map each rune of a normalised copy of text back to its byte offset.
	var norm []rune
	var offsets []int
	prevSpace := false
	for i, r := range text {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			if prevSpace {
				continue
			}
			r = ' '
			prevSpace = true
		} else {
			prevSpace = false
		}
		norm = append(norm, []rune(strings.ToLower(string(r)))[0])
		offsets = append(offsets, i)
	}
	q := []rune(strings.ToLower(strings.Join(strings.Fields(quote), " ")))
	for start := 0; start+len(q) <= len(norm); start++ {
		match := true
		for k := range q {
			if norm[start+k] != q[k] {
				match = false
				break
			}
		}
		if match {
			begin := offsets[start]
			endIdx := start + len(q) - 1
			_, size := utf8.DecodeRuneInString(text[offsets[endIdx]:])
			return text[begin : offsets[endIdx]+size], true
		}
	}
	return "", false
}

// --- operations_digest ---------------------------------------------------------

const digestSystem = `You explain a CRM operations report to a sales manager.
The findings were computed by fixed company rules; the numbers are correct and must not be changed.
Kinds: stale_lead = a lead with no activity; overdue_task = an open task past its due date;
deal_without_next_step = an open deal with no planned task; stalled_deal = an open deal with no changes.
For each kind present in the input write one short item (state the count and the oldest age in days)
and one practical recommendation. In finding_refs cite up to 3 refs copied from the input, oldest first,
only refs of that kind. Never write refs inside "text" or "headline": the CRM links them itself.
Do not invent refs, names, amounts or facts. Write all texts in %s.
Answer with JSON only.`

type digestFinding struct {
	Ref     string `json:"ref"`
	Kind    string `json:"kind"`
	AgeDays int    `json:"age_days"`
}

type digestInput struct {
	Language   string          `json:"language"`
	Thresholds map[string]int  `json:"thresholds"`
	Counts     map[string]int  `json:"counts"`
	Findings   []digestFinding `json:"findings"`
}

type digestCited struct {
	Kind        string   `json:"kind,omitempty"`
	Text        string   `json:"text"`
	FindingRefs []string `json:"finding_refs"`
}

type digestModelAnswer struct {
	Headline        string        `json:"headline"`
	Items           []digestCited `json:"items"`
	Recommendations []digestCited `json:"recommendations"`
}

type digestOutput struct {
	Schema           string        `json:"schema"`
	Headline         string        `json:"headline"`
	Items            []digestCited `json:"items"`
	Recommendations  []digestCited `json:"recommendations"`
	InsufficientData bool          `json:"insufficient_data"`
	Language         string        `json:"language"`
	Generator        generator     `json:"generator"`
}

var emptyDigestHeadline = map[string]string{
	"ru": "Нарушений по правилам компании не найдено.",
	"ky": "Компаниянын эрежелери боюнча бузуулар табылган жок.",
	"en": "No issues found under the company's rules.",
}

func (p Provider) operationsDigest(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var in digestInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errors.New("llamacpp: input is not an operations_digest input")
	}
	out := digestOutput{
		Schema:          "operations_digest.output.v1",
		Items:           []digestCited{},
		Recommendations: []digestCited{},
		Language:        in.Language,
		Generator:       p.generator(),
	}
	if len(in.Findings) == 0 {
		// Nothing to explain: say so without asking the model (principle 7, ADR-005).
		out.Headline = emptyDigestHeadline[in.Language]
		out.InsufficientData = true
		return json.Marshal(out)
	}

	// The grammar only allows kinds and refs that exist in the input.
	var kinds, refs []string
	seenKind := map[string]bool{}
	for _, f := range in.Findings {
		refs = append(refs, f.Ref)
		if !seenKind[f.Kind] && in.Counts[f.Kind] > 0 {
			seenKind[f.Kind] = true
			kinds = append(kinds, f.Kind)
		}
	}
	sort.Strings(kinds)
	refsSchema := map[string]any{
		"type": "array", "minItems": 1, "maxItems": 3, "uniqueItems": true,
		"items": map[string]any{"type": "string", "enum": refs},
	}
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"headline", "items", "recommendations"},
		"properties": map[string]any{
			"headline": map[string]any{"type": "string", "minLength": 1, "maxLength": 300},
			"items": map[string]any{
				"type": "array", "minItems": 1, "maxItems": len(kinds),
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{"kind", "text", "finding_refs"},
					"properties": map[string]any{
						"kind":         map[string]any{"type": "string", "enum": kinds},
						"text":         map[string]any{"type": "string", "minLength": 1, "maxLength": 400},
						"finding_refs": refsSchema,
					},
				},
			},
			"recommendations": map[string]any{
				"type": "array", "maxItems": 5,
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{"text", "finding_refs"},
					"properties": map[string]any{
						"text":         map[string]any{"type": "string", "minLength": 1, "maxLength": 400},
						"finding_refs": refsSchema,
					},
				},
			},
		},
	}
	user, _ := json.Marshal(map[string]any{"thresholds": in.Thresholds, "counts": in.Counts, "findings": in.Findings})
	var ans digestModelAnswer
	if err := p.complete(ctx, fmt.Sprintf(digestSystem, languageName(in.Language)), "Report:\n"+string(user), schema, 900, &ans); err != nil {
		return nil, err
	}

	kindOf := map[string]string{}
	for _, f := range in.Findings {
		kindOf[f.Ref] = f.Kind
	}
	out.Headline = strings.TrimSpace(ans.Headline)
	if out.Headline == "" {
		return nil, modelgw.Transient(errors.New("llamacpp: empty headline"))
	}
	usedKind := map[string]bool{}
	for _, it := range ans.Items {
		var keep []string
		for _, r := range it.FindingRefs {
			if kindOf[r] == it.Kind { // the grammar cannot tie refs to their kind; we do
				keep = append(keep, r)
			}
		}
		if len(keep) == 0 || usedKind[it.Kind] || strings.TrimSpace(it.Text) == "" {
			continue
		}
		usedKind[it.Kind] = true
		out.Items = append(out.Items, digestCited{Kind: it.Kind, Text: strings.TrimSpace(it.Text), FindingRefs: keep})
	}
	for _, rec := range ans.Recommendations {
		if strings.TrimSpace(rec.Text) != "" && len(rec.FindingRefs) > 0 {
			out.Recommendations = append(out.Recommendations, digestCited{Text: strings.TrimSpace(rec.Text), FindingRefs: rec.FindingRefs})
		}
	}
	if len(out.Items) == 0 {
		return nil, modelgw.Transient(errors.New("llamacpp: no item cited findings of its own kind"))
	}
	return json.Marshal(out)
}
