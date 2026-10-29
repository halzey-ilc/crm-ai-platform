package jobtypes

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"crmai/internal/replycheck"
)

// MessageReplyV1 classifies a customer's latest message and drafts a reply for
// a person to review (capability: communication assistant). Nothing is sent:
// the CRM shows the draft and a person decides.
func MessageReplyV1() Definition {
	return Definition{
		Type:         "message.reply.v1",
		Capability:   "communication-assistant",
		Task:         "message_reply",
		InputSchema:  "message_reply.input.v1",
		OutputSchema: "message_reply.output.v1",
		MaxAttempts:  3,
		Timeout:      10 * time.Minute, // see CallSummaryV1 for the measured speed
		Ground:       groundMessageReply,
	}
}

func groundMessageReply(input, output json.RawMessage) error {
	in, err := replycheck.Parse(input)
	if err != nil {
		return errors.New("input unreadable")
	}
	var out struct {
		Intent   string `json:"intent"`
		Evidence *struct {
			MessageRef string `json:"message_ref"`
			Quote      string `json:"quote"`
		} `json:"evidence"`
		DraftReply *string `json:"draft_reply"`
		NeedsHuman bool    `json:"needs_human"`
		Language   string  `json:"language"`
	}
	if err := json.Unmarshal(output, &out); err != nil {
		return errors.New("output unreadable")
	}
	if !in.EndsWithCustomer() {
		return errors.New("the conversation must end with a customer message")
	}
	if out.Language != in.Language {
		return errors.New("output language differs from input language")
	}
	if out.Evidence == nil {
		if out.Intent != "other" {
			return errors.New("an intent other than \"other\" needs evidence")
		}
	} else {
		msg, ok := in.Inbound(out.Evidence.MessageRef)
		if !ok {
			return errors.New("evidence cites a message that is not a customer message of the input")
		}
		if !strings.Contains(msg.Text, out.Evidence.Quote) {
			return errors.New("evidence quote does not occur in the cited message")
		}
	}
	if replycheck.NeedsHuman[out.Intent] && !out.NeedsHuman {
		return fmt.Errorf("intent %q must be handled by a person", out.Intent)
	}
	if out.DraftReply != nil {
		if bad := in.Unsupported(*out.DraftReply); bad != nil {
			return fmt.Errorf("draft contains facts not in the conversation: %d item(s)", len(bad))
		}
	}
	return nil
}
