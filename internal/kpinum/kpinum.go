// Package kpinum checks that numbers in a model's KPI explanation come from the
// CRM's input. The CRM computes every value and change (principle 2, ADR-005);
// a text may restate those numbers, rounded, but never introduce new ones.
package kpinum

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Metric is one KPI row of kpi_explanation.input.v1.
type Metric struct {
	Code      string   `json:"code"`
	Name      string   `json:"name"`
	Unit      string   `json:"unit"`
	Current   *float64 `json:"current"`
	Previous  *float64 `json:"previous"`
	Change    *float64 `json:"change"`
	ChangePct *float64 `json:"change_pct"`
}

// Period is a reporting period (end exclusive).
type Period struct {
	Type  string `json:"type"`
	Start string `json:"start"`
	End   string `json:"end"`
}

// Input is kpi_explanation.input.v1.
type Input struct {
	Language       string   `json:"language"`
	Period         Period   `json:"period"`
	PreviousPeriod Period   `json:"previous_period"`
	Metrics        []Metric `json:"metrics"`
}

// Parse decodes an input document.
func Parse(raw json.RawMessage) (Input, error) {
	var in Input
	err := json.Unmarshal(raw, &in)
	return in, err
}

// Codes returns the metric codes in input order.
func (in Input) Codes() []string {
	out := make([]string, 0, len(in.Metrics))
	for _, m := range in.Metrics {
		out = append(out, m.Code)
	}
	return out
}

// Insufficient reports whether there is nothing to explain: no metric has a
// non-zero current value.
func (in Input) Insufficient() bool {
	for _, m := range in.Metrics {
		if m.Current != nil && *m.Current != 0 {
			return false
		}
	}
	return true
}

// Allowed is the set of numbers a text may mention.
type Allowed struct{ values []float64 }

// AllowedNumbers collects every value, change and their magnitudes, plus the
// numbers that make up the period dates (so "October 2026" is fine).
func (in Input) AllowedNumbers() Allowed {
	var a Allowed
	add := func(p *float64) {
		if p != nil {
			a.values = append(a.values, *p, math.Abs(*p))
		}
	}
	for _, m := range in.Metrics {
		add(m.Current)
		add(m.Previous)
		add(m.Change)
		add(m.ChangePct)
	}
	for _, p := range []Period{in.Period, in.PreviousPeriod} {
		for _, d := range []string{p.Start, p.End} {
			for _, part := range strings.Split(d, "-") {
				if v, err := strconv.ParseFloat(part, 64); err == nil {
					a.values = append(a.values, v)
				}
			}
		}
	}
	return a
}

// Contains reports whether x equals an allowed number, possibly rounded to 0,
// 1 or 2 decimals (the model may write 12.5 for 12.47).
func (a Allowed) Contains(x float64) bool {
	for _, v := range a.values {
		for _, r := range []float64{v, round(v, 2), round(v, 1), round(v, 0)} {
			if math.Abs(x-r) < 1e-6 {
				return true
			}
		}
	}
	return false
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// Numbers like 12, 12.5, 12,5, 1 250 000 and 1 250 000,50 (space, NBSP or
// narrow NBSP as thousands separators).
var numberRE = regexp.MustCompile(`\d{1,3}(?:[ \x{00A0}\x{202F}]\d{3})+(?:[.,]\d+)?|\d+(?:[.,]\d+)?`)

// NumbersIn extracts the numbers mentioned in text.
func NumbersIn(text string) []float64 {
	var out []float64
	for _, m := range numberRE.FindAllString(text, -1) {
		s := strings.NewReplacer(" ", "", " ", "", " ", "", ",", ".").Replace(m)
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// Unknown returns the numbers in text that are not allowed (nil if none).
func (a Allowed) Unknown(text string) []float64 {
	var bad []float64
	for _, x := range NumbersIn(text) {
		if !a.Contains(x) {
			bad = append(bad, x)
		}
	}
	return bad
}

// Format writes a number the way the mock and prompts present it: integers
// without decimals, others with up to two decimals.
func Format(v float64) string {
	if v == math.Trunc(v) {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(round(v, 2), 'f', -1, 64)
}
