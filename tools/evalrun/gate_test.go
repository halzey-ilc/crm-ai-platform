package main

import (
	"strings"
	"testing"
)

func TestGate(t *testing.T) {
	ok := caseResult{ID: "a", Type: "t", Status: "succeeded"}
	checks := caseResult{ID: "b", Type: "t", Status: "succeeded", ChecksFailed: []string{"tool:x"}}
	broken := caseResult{ID: "c", Type: "t", Status: "failed", ErrorCode: "output_contract_violation"}

	if p := gate([]caseResult{ok, checks}, true, nil); len(p) != 0 {
		t.Fatalf("require-success looks at contract and grounding only: %v", p)
	}
	if p := gate([]caseResult{ok, broken}, true, nil); len(p) != 1 || !strings.Contains(p[0], "c (t)") {
		t.Fatalf("a failed case must fail the gate: %v", p)
	}

	baseline := []caseResult{ok, {ID: "b", Status: "succeeded"}, broken}
	now := []caseResult{
		{ID: "a", Type: "t", Status: "failed", ErrorCode: "output_not_grounded"}, // regressed
		checks, // passed before, now a check fails: regressed
		{ID: "c", Type: "t", Status: "succeeded"}, // improved
		{ID: "new", Type: "t", Status: "failed"},  // not in the baseline
	}
	p := gate(now, false, baseline)
	if len(p) != 2 || !strings.Contains(p[0], "a (t)") || !strings.Contains(p[1], "checks [tool:x]") {
		t.Fatalf("only cases that passed before and fail now are regressions: %v", p)
	}
}
