// Package dealcoach holds the input model and the grounding rules of a deal
// brief (capability: sales coach). Every point of a brief cites records of the
// input, and its text may only mention numbers the input contains: the deal's
// values, the dates, and numbers written in the history itself.
package dealcoach

import (
	"encoding/json"
	"strconv"
	"strings"

	"crmai/internal/kpinum"
)

// Deal is the deal part of deal_coach.input.v1.
type Deal struct {
	Ref               string  `json:"ref"`
	Title             string  `json:"title"`
	Status            string  `json:"status"`
	Stage             string  `json:"stage"`
	StagePosition     int     `json:"stage_position"`
	StageCount        int     `json:"stage_count"`
	Probability       int     `json:"probability"`
	Amount            float64 `json:"amount"`
	Currency          string  `json:"currency"`
	ExpectedCloseDate *string `json:"expected_close_date"`
	AgeDays           int     `json:"age_days"`
}

// Item is one history record.
type Item struct {
	Ref    string  `json:"ref"`
	Kind   string  `json:"kind"`
	Date   string  `json:"date"`
	Title  *string `json:"title"`
	Text   *string `json:"text"`
	Status *string `json:"status"`
}

// Input is deal_coach.input.v1.
type Input struct {
	Language string `json:"language"`
	Today    string `json:"today"`
	Deal     Deal   `json:"deal"`
	History  []Item `json:"history"`
}

// Point is one cited statement of a brief.
type Point struct {
	Text string   `json:"text"`
	Refs []string `json:"refs"`
}

// Parse decodes an input document.
func Parse(raw json.RawMessage) (Input, error) {
	var in Input
	err := json.Unmarshal(raw, &in)
	return in, err
}

// Insufficient reports whether there is nothing to coach on: no history.
func (in Input) Insufficient() bool { return len(in.History) == 0 }

// Refs returns the deal ref followed by the history refs.
func (in Input) Refs() []string {
	out := []string{in.Deal.Ref}
	for _, it := range in.History {
		out = append(out, it.Ref)
	}
	return out
}

// Allowed is the set of numbers a brief may mention.
func (in Input) Allowed() kpinum.Allowed {
	d := in.Deal
	values := []float64{d.Amount, float64(d.Probability), float64(d.StagePosition), float64(d.StageCount), float64(d.AgeDays)}
	dates := []string{in.Today}
	if d.ExpectedCloseDate != nil {
		dates = append(dates, *d.ExpectedCloseDate)
	}
	texts := []string{d.Title, d.Stage}
	for _, it := range in.History {
		dates = append(dates, it.Date)
		for _, s := range []*string{it.Title, it.Text} {
			if s != nil {
				texts = append(texts, *s)
			}
		}
	}
	for _, dt := range dates {
		for _, part := range strings.Split(dt, "-") {
			if v, err := strconv.ParseFloat(part, 64); err == nil {
				values = append(values, v)
			}
		}
	}
	values = append(values, kpinum.NumbersIn(strings.Join(texts, "\n"))...)
	return kpinum.FromValues(values...)
}

// Check returns a description of the first rule a point breaks, or "".
func Check(p Point, known map[string]bool, allowed kpinum.Allowed) string {
	if strings.TrimSpace(p.Text) == "" {
		return "empty text"
	}
	for _, r := range p.Refs {
		if !known[r] {
			return "cites a record that is not in the input"
		}
	}
	if allowed.Unknown(p.Text) != nil {
		return "contains numbers that are not in the input"
	}
	return ""
}

// Known returns the input refs as a set.
func (in Input) Known() map[string]bool {
	out := map[string]bool{}
	for _, r := range in.Refs() {
		out[r] = true
	}
	return out
}
