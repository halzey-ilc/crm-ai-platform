// Package replycheck holds the rules a reply draft for a customer conversation
// must follow (capability: communication assistant). A draft may restate what
// the conversation says, but it must not introduce numbers (prices, dates,
// quantities), links or e-mail addresses: those are facts only a person or the
// CRM can supply.
package replycheck

import (
	"encoding/json"
	"regexp"
	"strings"

	"crmai/internal/kpinum"
)

// Message is one message of message_reply.input.v1.
type Message struct {
	Ref       string `json:"ref"`
	Direction string `json:"direction"`
	Text      string `json:"text"`
}

// Input is message_reply.input.v1.
type Input struct {
	Language string    `json:"language"`
	Channel  string    `json:"channel"`
	Messages []Message `json:"messages"`
}

// Parse decodes an input document.
func Parse(raw json.RawMessage) (Input, error) {
	var in Input
	err := json.Unmarshal(raw, &in)
	return in, err
}

// Inbound returns the customer's message with the given ref.
func (in Input) Inbound(ref string) (Message, bool) {
	for _, m := range in.Messages {
		if m.Ref == ref && m.Direction == "inbound" {
			return m, true
		}
	}
	return Message{}, false
}

// InboundRefs lists the refs of the customer's messages, in order.
func (in Input) InboundRefs() []string {
	var out []string
	for _, m := range in.Messages {
		if m.Direction == "inbound" {
			out = append(out, m.Ref)
		}
	}
	return out
}

// EndsWithCustomer reports whether the last message is the customer's.
func (in Input) EndsWithCustomer() bool {
	return len(in.Messages) > 0 && in.Messages[len(in.Messages)-1].Direction == "inbound"
}

// Latest returns the customer's messages after the company's last reply: the
// messages a reply has to answer.
func (in Input) Latest() []Message {
	start := 0
	for i, m := range in.Messages {
		if m.Direction == "outbound" {
			start = i + 1
		}
	}
	return in.Messages[start:]
}

func (in Input) allText() string {
	parts := make([]string, 0, len(in.Messages))
	for _, m := range in.Messages {
		parts = append(parts, m.Text)
	}
	return strings.Join(parts, "\n")
}

var contactRE = regexp.MustCompile(`(?i)https?://\S+|www\.\S+|[\w.+-]+@[\w-]+\.[\w.-]+`)

// Unsupported returns the numbers, links and e-mail addresses in draft that the
// conversation does not contain (nil if there are none).
func (in Input) Unsupported(draft string) []string {
	text := in.allText()
	allowed := kpinum.FromValues(kpinum.NumbersIn(text)...)
	var bad []string
	for _, x := range kpinum.NumbersIn(draft) {
		if !allowed.Contains(x) {
			bad = append(bad, kpinum.Format(x))
		}
	}
	lower := strings.ToLower(text)
	for _, c := range contactRE.FindAllString(draft, -1) {
		c = strings.TrimRight(c, ".,;:!?)")
		if !strings.Contains(lower, strings.ToLower(c)) {
			bad = append(bad, c)
		}
	}
	return bad
}

// NeedsHuman lists intents a person must handle even when a draft exists.
var NeedsHuman = map[string]bool{"complaint": true, "cancellation": true}
