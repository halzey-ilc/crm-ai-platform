package jobtypes

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"crmai/internal/kpinum"
)

// KpiExplanationV1 explains KPI changes between two periods (capability: AI
// Business Analyst). The CRM computes all values and changes; the model only
// explains them, citing metric codes, and may not introduce numbers.
func KpiExplanationV1() Definition {
	return Definition{
		Type:         "kpi.explanation.v1",
		Capability:   "business-analyst",
		Task:         "kpi_explanation",
		InputSchema:  "kpi_explanation.input.v1",
		OutputSchema: "kpi_explanation.output.v1",
		MaxAttempts:  3,
		Timeout:      10 * time.Minute, // see CallSummaryV1 for the measured speed
		Ground:       groundKpiExplanation,
	}
}

type kpiCited struct {
	MetricCodes []string `json:"metric_codes"`
	Text        string   `json:"text"`
}

func groundKpiExplanation(input, output json.RawMessage) error {
	in, err := kpinum.Parse(input)
	if err != nil {
		return errors.New("input unreadable")
	}
	var out struct {
		Headline         string     `json:"headline"`
		Observations     []kpiCited `json:"observations"`
		Recommendations  []kpiCited `json:"recommendations"`
		InsufficientData bool       `json:"insufficient_data"`
		Language         string     `json:"language"`
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
	if out.InsufficientData && (len(out.Observations) > 0 || len(out.Recommendations) > 0) {
		return errors.New("with insufficient data the output must be empty")
	}
	known := map[string]bool{}
	for _, c := range in.Codes() {
		known[c] = true
	}
	allowed := in.AllowedNumbers()
	if bad := allowed.Unknown(out.Headline); bad != nil {
		return fmt.Errorf("headline contains numbers not in the input: %v", bad)
	}
	for section, items := range map[string][]kpiCited{"observation": out.Observations, "recommendation": out.Recommendations} {
		for i, it := range items {
			for _, c := range it.MetricCodes {
				if !known[c] {
					return fmt.Errorf("%s #%d cites an unknown metric", section, i)
				}
			}
			if bad := allowed.Unknown(it.Text); bad != nil {
				return fmt.Errorf("%s #%d contains numbers not in the input: %v", section, i, bad)
			}
		}
	}
	return nil
}
