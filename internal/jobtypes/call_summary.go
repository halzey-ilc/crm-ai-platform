package jobtypes

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CallSummaryV1 summarises a call transcript into grounded key points and a
// next step (capability: call intelligence).
func CallSummaryV1() Definition {
	return Definition{
		Type:         "call.summary.v1",
		Capability:   "call-intelligence",
		Task:         "call_summary",
		InputSchema:  "call_summary.input.v1",
		OutputSchema: "call_summary.output.v1",
		MaxAttempts:  3,
		Timeout:      60 * time.Second,
		Ground:       groundCallSummary,
	}
}

type quoted struct {
	EvidenceQuote string `json:"evidence_quote"`
}

func groundCallSummary(input, output json.RawMessage) error {
	var in struct {
		Transcript string `json:"transcript"`
		Language   string `json:"language"`
	}
	var out struct {
		KeyPoints []quoted `json:"key_points"`
		NextStep  *quoted  `json:"next_step"`
		Language  string   `json:"language"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return errors.New("input unreadable")
	}
	if err := json.Unmarshal(output, &out); err != nil {
		return errors.New("output unreadable")
	}
	if out.Language != in.Language {
		return errors.New("output language differs from input language")
	}
	items := out.KeyPoints
	if out.NextStep != nil {
		items = append(items, *out.NextStep)
	}
	for i, q := range items {
		if !strings.Contains(in.Transcript, q.EvidenceQuote) {
			return fmt.Errorf("evidence quote #%d does not occur in the transcript", i)
		}
	}
	return nil
}
