package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

func passed(r caseResult) bool { return r.Status == "succeeded" && len(r.ChecksFailed) == 0 }

// gate returns why a run should fail CI or a release check:
//   - requireSuccess: any case that failed its contract or grounding (mock runs:
//     a broken dataset, contract or check shows up here without any model);
//   - baseline: any case that passed in the earlier report and does not now
//     (model runs: a prompt, grammar or model change made things worse).
//
// Cases missing from the baseline are new and are not compared.
func gate(results []caseResult, requireSuccess bool, baseline []caseResult) []string {
	var problems []string
	if requireSuccess {
		for _, r := range results {
			if r.Status != "succeeded" {
				problems = append(problems, fmt.Sprintf("%s (%s): %s %s", r.ID, r.Type, r.Status, r.ErrorCode))
			}
		}
	}
	before := map[string]caseResult{}
	for _, b := range baseline {
		before[b.ID] = b
	}
	for _, r := range results {
		b, ok := before[r.ID]
		if ok && passed(b) && !passed(r) {
			why := r.ErrorCode
			if len(r.ChecksFailed) > 0 {
				why = fmt.Sprint("checks ", r.ChecksFailed)
			}
			problems = append(problems, fmt.Sprintf("%s (%s): passed in the baseline, now %s %s", r.ID, r.Type, r.Status, why))
		}
	}
	sort.Strings(problems)
	return problems
}

func loadReport(path string) ([]caseResult, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rep struct {
		Results []caseResult `json:"results"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, fmt.Errorf("baseline %s: %w", path, err)
	}
	return rep.Results, nil
}
