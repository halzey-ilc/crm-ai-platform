package jobtypes

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"crmai/internal/riskreview"
)

// RiskReviewV1 explains the CRM's risk signals to an owner or admin in neutral
// terms and says what to check (capability: risk & anomaly detection).
func RiskReviewV1() Definition {
	return Definition{
		Type:         "risk.review.v1",
		Capability:   "risk-anomaly",
		Task:         "risk_review",
		InputSchema:  "risk_review.input.v1",
		OutputSchema: "risk_review.output.v1",
		MaxAttempts:  3,
		Timeout:      10 * time.Minute, // see CallSummaryV1 for the measured speed
		Ground:       groundRiskReview,
	}
}

func groundRiskReview(input, output json.RawMessage) error {
	in, err := riskreview.Parse(input)
	if err != nil {
		return errors.New("input unreadable")
	}
	var out struct {
		Headline         string             `json:"headline"`
		Items            []riskreview.Cited `json:"items"`
		Checks           []riskreview.Cited `json:"checks"`
		InsufficientData bool               `json:"insufficient_data"`
		Language         string             `json:"language"`
	}
	if err := json.Unmarshal(output, &out); err != nil {
		return errors.New("output unreadable")
	}
	if out.Language != in.Language {
		return errors.New("output language differs from input language")
	}
	if out.InsufficientData != in.Insufficient() {
		return errors.New("insufficient_data does not match the input")
	}
	if out.InsufficientData && (len(out.Items) > 0 || len(out.Checks) > 0) {
		return errors.New("with no signals the output must be empty")
	}
	allowed, kinds := in.Allowed(), in.KindsOf()
	if p := riskreview.Problem(out.Headline, allowed); p != "" {
		return fmt.Errorf("headline %s", p)
	}
	for i, it := range out.Items {
		for _, r := range it.SignalRefs {
			if !kinds[r][it.Kind] {
				return fmt.Errorf("item #%d cites a ref that has no %s signal", i, it.Kind)
			}
		}
		if p := riskreview.Problem(it.Text, allowed); p != "" {
			return fmt.Errorf("item #%d %s", i, p)
		}
	}
	for i, c := range out.Checks {
		for _, r := range c.SignalRefs {
			if kinds[r] == nil {
				return fmt.Errorf("check #%d cites a ref that is not in the input", i)
			}
		}
		if p := riskreview.Problem(c.Text, allowed); p != "" {
			return fmt.Errorf("check #%d %s", i, p)
		}
	}
	return nil
}
