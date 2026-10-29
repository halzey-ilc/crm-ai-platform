package jobtypes

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"crmai/internal/dealcoach"
)

// DealCoachV1 prepares a manager for the next conversation about a deal
// (capability: sales coach): the situation, risks, questions to clarify and a
// next step, each citing the deal records it is based on.
func DealCoachV1() Definition {
	return Definition{
		Type:         "deal.coach.v1",
		Capability:   "sales-coach",
		Task:         "deal_coach",
		InputSchema:  "deal_coach.input.v1",
		OutputSchema: "deal_coach.output.v1",
		MaxAttempts:  3,
		Timeout:      10 * time.Minute, // see CallSummaryV1 for the measured speed
		Ground:       groundDealCoach,
	}
}

func groundDealCoach(input, output json.RawMessage) error {
	in, err := dealcoach.Parse(input)
	if err != nil {
		return errors.New("input unreadable")
	}
	var out struct {
		Situation        []dealcoach.Point `json:"situation"`
		Risks            []dealcoach.Point `json:"risks"`
		Questions        []dealcoach.Point `json:"questions"`
		NextStep         *dealcoach.Point  `json:"next_step"`
		InsufficientData bool              `json:"insufficient_data"`
		Language         string            `json:"language"`
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
	if out.InsufficientData && (len(out.Risks) > 0 || len(out.Questions) > 0 || out.NextStep != nil) {
		return errors.New("without history the brief may only describe the deal")
	}
	known, allowed := in.Known(), in.Allowed()
	sections := map[string][]dealcoach.Point{"situation": out.Situation, "risk": out.Risks, "question": out.Questions}
	if out.NextStep != nil {
		sections["next step"] = []dealcoach.Point{*out.NextStep}
	}
	for name, points := range sections {
		for i, p := range points {
			if problem := dealcoach.Check(p, known, allowed); problem != "" {
				return fmt.Errorf("%s #%d %s", name, i, problem)
			}
		}
	}
	return nil
}
