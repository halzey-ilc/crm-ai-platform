package jobtypes

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"crmai/internal/callreview"
)

// CallReviewV1 reviews a manager's call against the company's checklist
// (capability: call intelligence). Positive verdicts quote the transcript.
func CallReviewV1() Definition {
	return Definition{
		Type:         "call.review.v1",
		Capability:   "call-intelligence",
		Task:         "call_review",
		InputSchema:  "call_review.input.v1",
		OutputSchema: "call_review.output.v1",
		MaxAttempts:  3,
		Timeout:      10 * time.Minute, // see CallSummaryV1 for the measured speed
		Ground:       groundCallReview,
	}
}

func groundCallReview(input, output json.RawMessage) error {
	in, err := callreview.Parse(input)
	if err != nil {
		return errors.New("input unreadable")
	}
	var out struct {
		Criteria         []callreview.Verdict `json:"criteria"`
		Strengths        []callreview.Advice  `json:"strengths"`
		Improvements     []callreview.Advice  `json:"improvements"`
		InsufficientData bool                 `json:"insufficient_data"`
		Language         string               `json:"language"`
	}
	if err := json.Unmarshal(output, &out); err != nil {
		return errors.New("output unreadable")
	}
	if out.Language != in.Language {
		return errors.New("output language differs from input language")
	}
	if len(out.Criteria) != len(in.Criteria) {
		return errors.New("every criterion needs exactly one verdict")
	}
	if out.InsufficientData != in.Insufficient() {
		return errors.New("insufficient_data does not match the transcript")
	}
	for i, v := range out.Criteria {
		if v.Code != in.Criteria[i].Code {
			return fmt.Errorf("verdict #%d is for %q, not %q: keep the input's order", i, v.Code, in.Criteria[i].Code)
		}
		if out.InsufficientData && v.Status != "not_applicable" {
			return fmt.Errorf("verdict #%d judges a call too short to review", i)
		}
		if problem := in.Check(v); problem != "" {
			return fmt.Errorf("verdict #%d %s", i, problem)
		}
	}
	if out.InsufficientData && (len(out.Strengths) > 0 || len(out.Improvements) > 0) {
		return errors.New("a call too short to review has no advice")
	}
	return nil
}
