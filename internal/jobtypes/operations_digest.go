package jobtypes

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// OperationsDigestV1 explains the CRM's operations findings (capability: AI
// Operations). The CRM computes the findings with deterministic rules; the
// model only explains them and must cite the findings it talks about.
func OperationsDigestV1() Definition {
	return Definition{
		Type:         "operations.digest.v1",
		Capability:   "operations",
		Task:         "operations_digest",
		InputSchema:  "operations_digest.input.v1",
		OutputSchema: "operations_digest.output.v1",
		MaxAttempts:  3,
		Timeout:      10 * time.Minute, // see CallSummaryV1 for the measured speed
		Ground:       groundOperationsDigest,
	}
}

type digestFinding struct {
	Ref  string `json:"ref"`
	Kind string `json:"kind"`
}

type digestCited struct {
	Kind        string   `json:"kind"`
	FindingRefs []string `json:"finding_refs"`
}

func groundOperationsDigest(input, output json.RawMessage) error {
	var in struct {
		Language string          `json:"language"`
		Counts   map[string]int  `json:"counts"`
		Findings []digestFinding `json:"findings"`
	}
	var out struct {
		Items            []digestCited `json:"items"`
		Recommendations  []digestCited `json:"recommendations"`
		InsufficientData bool          `json:"insufficient_data"`
		Language         string        `json:"language"`
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
	kindOf := make(map[string]string, len(in.Findings))
	for _, f := range in.Findings {
		kindOf[f.Ref] = f.Kind
	}
	if len(in.Findings) == 0 {
		if !out.InsufficientData || len(out.Items) > 0 || len(out.Recommendations) > 0 {
			return errors.New("with no findings the output must be empty and flag insufficient_data")
		}
		return nil
	}
	if out.InsufficientData {
		return errors.New("insufficient_data set although findings were provided")
	}
	for i, item := range out.Items {
		if in.Counts[item.Kind] == 0 {
			return fmt.Errorf("item #%d describes a kind with no findings", i)
		}
		for _, ref := range item.FindingRefs {
			if kind, ok := kindOf[ref]; !ok || kind != item.Kind {
				return fmt.Errorf("item #%d cites a finding that is not in the input under its kind", i)
			}
		}
	}
	for i, rec := range out.Recommendations {
		for _, ref := range rec.FindingRefs {
			if _, ok := kindOf[ref]; !ok {
				return fmt.Errorf("recommendation #%d cites a finding that is not in the input", i)
			}
		}
	}
	return nil
}
