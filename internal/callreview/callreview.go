// Package callreview holds the input model and the rules of a call review
// (capability: call intelligence): how a manager's call meets the company's
// criteria. Every positive verdict quotes the transcript, so a reviewer can
// check it; the score itself is computed by the CRM from the verdicts.
package callreview

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// Segment is one turn of the transcript.
type Segment struct {
	ID      string `json:"id"`
	Speaker string `json:"speaker"` // manager, customer or unknown
	Text    string `json:"text"`
}

// Criterion is one point of the company's call checklist.
type Criterion struct {
	Code        string `json:"code"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// Input is call_review.input.v1.
type Input struct {
	Language string      `json:"language"`
	CallRef  string      `json:"call_ref,omitempty"`
	Segments []Segment   `json:"segments"`
	Criteria []Criterion `json:"criteria"`
}

// Evidence points at the words in the transcript a verdict rests on.
type Evidence struct {
	SegmentID string `json:"segment_id"`
	Quote     string `json:"quote"`
}

// Verdict is the review of one criterion.
type Verdict struct {
	Code     string    `json:"code"`
	Status   string    `json:"status"` // met, partial, missed, not_applicable
	Evidence *Evidence `json:"evidence"`
	Comment  string    `json:"comment"`
}

// Advice is a strength or an improvement for the manager.
type Advice struct {
	Text string `json:"text"`
}

// Statuses a verdict may have; met and partial need evidence.
var Statuses = []string{"met", "partial", "missed", "not_applicable"}

// MinTalk is how much both sides must have said for a call to be reviewable.
const MinTalk = 40

// MinQuote is the shortest quote accepted as evidence.
const MinQuote = 3

// Parse decodes an input document.
func Parse(raw json.RawMessage) (Input, error) {
	var in Input
	err := json.Unmarshal(raw, &in)
	return in, err
}

// Segment returns the segment with this id.
func (in Input) Segment(id string) (Segment, bool) {
	for _, s := range in.Segments {
		if s.ID == id {
			return s, true
		}
	}
	return Segment{}, false
}

// SegmentIDs lists the segment ids in order.
func (in Input) SegmentIDs() []string {
	ids := make([]string, 0, len(in.Segments))
	for _, s := range in.Segments {
		ids = append(ids, s.ID)
	}
	return ids
}

// Insufficient reports a call too short to review: no manager turn, or both
// sides together said less than MinTalk characters.
func (in Input) Insufficient() bool {
	manager, talk := false, 0
	for _, s := range in.Segments {
		if s.Speaker == "manager" || s.Speaker == "unknown" {
			manager = true
		}
		talk += utf8.RuneCountInString(strings.TrimSpace(s.Text))
	}
	return !manager || talk < MinTalk
}

// normalize compares quotes without case or repeated spaces: speech
// recognition output varies in both, the words do not.
func normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// Quotes reports whether quote occurs in the segment's text.
func Quotes(segment Segment, quote string) bool {
	q := normalize(quote)
	return utf8.RuneCountInString(q) >= MinQuote && strings.Contains(normalize(segment.Text), q)
}

// Check returns why a verdict breaks the rules, or "".
func (in Input) Check(v Verdict) string {
	if !contains(Statuses, v.Status) {
		return "has an unknown status"
	}
	if strings.TrimSpace(v.Comment) == "" {
		return "has no comment"
	}
	if v.Status == "met" || v.Status == "partial" {
		if v.Evidence == nil {
			return "is positive without evidence"
		}
	}
	if v.Evidence != nil {
		segment, ok := in.Segment(v.Evidence.SegmentID)
		if !ok {
			return "cites a segment that is not in the transcript"
		}
		if !Quotes(segment, v.Evidence.Quote) {
			return "quotes words that are not in the cited segment"
		}
	}
	return ""
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
