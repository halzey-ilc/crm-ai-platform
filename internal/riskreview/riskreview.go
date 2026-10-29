// Package riskreview holds the input model and the rules for explaining risk
// signals (capability: risk & anomaly detection). A model signal is not proof
// that an employee broke a rule (ADR-005), so besides citing only the input's
// signals and numbers, an explanation may not use accusatory words.
package riskreview

import (
	"encoding/json"
	"strings"

	"crmai/internal/kpinum"
)

// Signal is one signal of risk_review.input.v1.
type Signal struct {
	Ref       string  `json:"ref"`
	Kind      string  `json:"kind"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
}

// Input is risk_review.input.v1.
type Input struct {
	Language   string         `json:"language"`
	WindowDays int            `json:"window_days"`
	Counts     map[string]int `json:"counts"`
	Signals    []Signal       `json:"signals"`
}

// Cited is an item or check of the output.
type Cited struct {
	Kind       string   `json:"kind,omitempty"`
	Text       string   `json:"text"`
	SignalRefs []string `json:"signal_refs"`
}

// Parse decodes an input document.
func Parse(raw json.RawMessage) (Input, error) {
	var in Input
	err := json.Unmarshal(raw, &in)
	return in, err
}

// Insufficient reports whether there are no signals to explain.
func (in Input) Insufficient() bool { return len(in.Signals) == 0 }

// KindsOf maps each ref to the kinds of signals it has.
func (in Input) KindsOf() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, s := range in.Signals {
		if out[s.Ref] == nil {
			out[s.Ref] = map[string]bool{}
		}
		out[s.Ref][s.Kind] = true
	}
	return out
}

// Allowed is the set of numbers a text may mention.
func (in Input) Allowed() kpinum.Allowed {
	values := []float64{float64(in.WindowDays)}
	for _, n := range in.Counts {
		values = append(values, float64(n))
	}
	for _, s := range in.Signals {
		values = append(values, s.Value, s.Threshold)
	}
	return kpinum.FromValues(values...)
}

// Lowercase stems that turn a signal into an accusation. Kept short and
// explicit; a text needing them is rejected, not rewritten.
var accusatory = []string{
	"мошенн", "наруш", "обман", "краж", "украл", "ворует", "виновн", "злоупотреб", "фальсиф", "подделк",
	"алдамч", "уурда", "күнөөлүү", "бузган",
	"fraud", "violat", "steal", "stole", "theft", "guilty", "cheat", "abuse", "falsif", "misconduct",
}

// Accusatory reports whether text states or implies wrongdoing.
func Accusatory(text string) bool {
	lower := strings.ToLower(text)
	for _, stem := range accusatory {
		if strings.Contains(lower, stem) {
			return true
		}
	}
	return false
}

// Problem returns why a text breaks the rules, or "".
func Problem(text string, allowed kpinum.Allowed) string {
	if strings.TrimSpace(text) == "" {
		return "empty text"
	}
	if allowed.Unknown(text) != nil {
		return "contains numbers that are not in the input"
	}
	if Accusatory(text) {
		return "states or implies wrongdoing"
	}
	return ""
}
