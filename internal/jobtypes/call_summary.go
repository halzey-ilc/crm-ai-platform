package jobtypes

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"crmai/internal/callsummary"
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
		// Measured: a 4B model on a 4-core laptop CPU generates ~2 tokens/s, so a
		// summary takes 2-4 minutes. The job is asynchronous; the CRM polls for it.
		Timeout: 10 * time.Minute,
		Ground:  groundCallSummary,
	}
}

// CallSummaryV2 is CallSummaryV1 plus an explicit insufficient_data flag: a text
// with nothing to summarise yields a fixed notice instead of free text.
func CallSummaryV2() Definition {
	d := CallSummaryV1()
	d.Type, d.Task, d.OutputSchema = "call.summary.v2", "call_summary_v2", "call_summary.output.v2"
	d.Ground = groundCallSummaryV2
	return d
}

func groundCallSummaryV2(input, output json.RawMessage) error {
	if err := groundCallSummary(input, output); err != nil {
		return err
	}
	var in struct {
		Transcript string `json:"transcript"`
	}
	var out struct {
		Summary          string   `json:"summary"`
		KeyPoints        []quoted `json:"key_points"`
		NextStep         *quoted  `json:"next_step"`
		InsufficientData bool     `json:"insufficient_data"`
		Language         string   `json:"language"`
	}
	_ = json.Unmarshal(input, &in)
	_ = json.Unmarshal(output, &out)
	empty := len(out.KeyPoints) == 0 && out.NextStep == nil
	switch {
	case callsummary.TooShort(in.Transcript) && !out.InsufficientData:
		return errors.New("the text is too short to summarise but insufficient_data is false")
	case out.InsufficientData != empty:
		return errors.New("insufficient_data must be set exactly when nothing grounded was extracted")
	case out.InsufficientData && out.Summary != callsummary.Notice(out.Language):
		return errors.New("with insufficient data the summary must be the fixed notice")
	}
	return nil
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
