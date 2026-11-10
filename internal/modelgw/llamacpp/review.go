package llamacpp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"crmai/internal/callreview"
)

// TaskCallReview reviews a manager's call against the company's checklist.
const TaskCallReview = "call_review"

const reviewSystem = `You review a sales manager's phone call against the company's checklist.
Judge ONLY the manager's words in the transcript; the customer's words are context.
For each criterion give status: met (done well), partial (done incompletely), missed (not done
although the call needed it), not_applicable (the call gave no occasion for it).
For met and partial, segment_id is the turn that shows it and quote copies words from that turn
exactly; otherwise segment_id is null and quote is "". comment says briefly why.
strengths: up to 3 things the manager did well. improvements: up to 3 concrete pieces of advice.
Do not invent words that are not in the transcript. Write comment, strengths and improvements
in %s. Answer with JSON only.`

func (p Provider) callReview(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	in, err := callreview.Parse(raw)
	if err != nil {
		return nil, errors.New("llamacpp: input is not a call_review input")
	}
	out := struct {
		Schema           string               `json:"schema"`
		Criteria         []callreview.Verdict `json:"criteria"`
		Strengths        []callreview.Advice  `json:"strengths"`
		Improvements     []callreview.Advice  `json:"improvements"`
		InsufficientData bool                 `json:"insufficient_data"`
		Language         string               `json:"language"`
		Generator        generator            `json:"generator"`
	}{
		Schema: "call_review.output.v1", Strengths: []callreview.Advice{}, Improvements: []callreview.Advice{},
		Language: in.Language, Generator: p.generator(),
	}
	if in.Insufficient() {
		// Nothing to judge: no model call.
		out.InsufficientData = true
		for _, c := range in.Criteria {
			out.Criteria = append(out.Criteria, callreview.Verdict{Code: c.Code, Status: "not_applicable", Comment: emptyReview[in.Language]})
		}
		return json.Marshal(out)
	}
	user, _ := json.Marshal(map[string]any{"checklist": in.Criteria, "transcript": in.Segments})
	if utf8.RuneCount(user) > maxTranscriptRunes {
		return nil, fmt.Errorf("llamacpp: a transcript longer than %d characters does not fit the model context", maxTranscriptRunes)
	}
	// One property per criterion, in the checklist's order: the model cannot skip,
	// repeat or reorder a criterion, and "status" comes before the evidence.
	segment := map[string]any{"anyOf": []any{map[string]any{"type": "null"}, map[string]any{"type": "string", "enum": in.SegmentIDs()}}}
	verdicts := ordered{}
	for _, c := range in.Criteria {
		verdicts = append(verdicts, field{c.Code, map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"status", "segment_id", "quote", "comment"},
			"properties": ordered{
				{"status", map[string]any{"type": "string", "enum": callreview.Statuses}},
				{"segment_id", segment},
				{"quote", map[string]any{"type": "string", "maxLength": 300}},
				{"comment", map[string]any{"type": "string", "minLength": 1, "maxLength": 300}},
			},
		}})
	}
	codes := make([]string, 0, len(in.Criteria))
	for _, c := range in.Criteria {
		codes = append(codes, c.Code)
	}
	advice := map[string]any{"type": "array", "maxItems": 3, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 300}}
	schema := map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"criteria", "strengths", "improvements"},
		"properties": ordered{
			{"criteria", map[string]any{"type": "object", "additionalProperties": false, "required": codes, "properties": verdicts}},
			{"strengths", advice},
			{"improvements", advice},
		},
	}
	var ans struct {
		Criteria map[string]struct {
			Status    string  `json:"status"`
			SegmentID *string `json:"segment_id"`
			Quote     string  `json:"quote"`
			Comment   string  `json:"comment"`
		} `json:"criteria"`
		Strengths    []string `json:"strengths"`
		Improvements []string `json:"improvements"`
	}
	if err := p.complete(ctx, fmt.Sprintf(reviewSystem, languageName(in.Language)), "Call:\n"+string(user), schema, 1200, &ans); err != nil {
		return nil, err
	}
	repaired := 0
	for _, c := range in.Criteria {
		a := ans.Criteria[c.Code]
		v := callreview.Verdict{Code: c.Code, Status: a.Status, Comment: strings.TrimSpace(a.Comment)}
		if v.Comment == "" {
			v.Comment = "—"
		}
		if a.SegmentID != nil {
			if s, ok := in.Segment(*a.SegmentID); ok {
				v.Evidence = &callreview.Evidence{SegmentID: s.ID, Quote: strings.TrimSpace(a.Quote)}
				if !callreview.Quotes(s, v.Evidence.Quote) {
					// The model named the right turn but paraphrased it: quote the turn itself.
					v.Evidence.Quote = truncateRunes(strings.TrimSpace(s.Text), 300)
					repaired++
				}
			}
		}
		if v.Evidence == nil && (v.Status == "met" || v.Status == "partial") {
			// A positive verdict nobody can check is not kept as positive.
			v.Status = "missed"
			repaired++
		}
		out.Criteria = append(out.Criteria, v)
	}
	for _, s := range ans.Strengths {
		if s = strings.TrimSpace(s); s != "" {
			out.Strengths = append(out.Strengths, callreview.Advice{Text: s})
		}
	}
	for _, s := range ans.Improvements {
		if s = strings.TrimSpace(s); s != "" {
			out.Improvements = append(out.Improvements, callreview.Advice{Text: s})
		}
	}
	if repaired > 0 && p.Log != nil {
		p.Log.Info("call review verdicts repaired", "count", repaired)
	}
	return json.Marshal(out)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

var emptyReview = map[string]string{
	"ru": "Звонок слишком короткий для оценки.",
	"ky": "Чалуу баалоо үчүн өтө кыска.",
	"en": "The call is too short to review.",
}
