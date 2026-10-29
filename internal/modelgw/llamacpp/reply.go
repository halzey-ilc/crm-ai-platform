package llamacpp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"crmai/internal/modelgw"
	"crmai/internal/replycheck"
)

// TaskMessageReply classifies a customer message and drafts a reply.
const TaskMessageReply = "message_reply"

const replySystem = `You help a sales manager answer a customer in a CRM chat.
Classify the customer's latest message and draft a reply.
intent: question, price_request, order, meeting_request, complaint, cancellation, gratitude or other.
urgency: high if the customer is upset, cancels or says it is urgent; low for thanks or small talk; otherwise normal.
evidence: message_ref of the customer message that shows the intent, and quote: a fragment copied EXACTLY from that message.
draft_reply: a short, polite reply in %s on behalf of the company.
Use ONLY facts stated in the conversation. Never state prices, discounts, delivery dates, availability, links,
phone numbers or terms that are not in the conversation; when they are needed, write that the manager will clarify.
needs_human: true if the answer needs facts the conversation does not contain, or the customer complains or cancels.
Answer with JSON only.`

var replyIntents = []string{"question", "price_request", "order", "meeting_request", "complaint", "cancellation", "gratitude", "other"}

type replyEvidence struct {
	MessageRef string `json:"message_ref"`
	Quote      string `json:"quote"`
}

type replyModelAnswer struct {
	Intent     string        `json:"intent"`
	Urgency    string        `json:"urgency"`
	Evidence   replyEvidence `json:"evidence"`
	DraftReply string        `json:"draft_reply"`
	NeedsHuman bool          `json:"needs_human"`
}

type replyOutput struct {
	Schema     string         `json:"schema"`
	Intent     string         `json:"intent"`
	Urgency    string         `json:"urgency"`
	Evidence   *replyEvidence `json:"evidence"`
	DraftReply *string        `json:"draft_reply"`
	NeedsHuman bool           `json:"needs_human"`
	Language   string         `json:"language"`
	Generator  generator      `json:"generator"`
}

func (p Provider) messageReply(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	in, err := replycheck.Parse(raw)
	if err != nil || !in.EndsWithCustomer() {
		return nil, errors.New("llamacpp: input is not a message_reply input ending with a customer message")
	}
	var b strings.Builder
	for _, m := range in.Messages {
		who := "customer"
		if m.Direction == "outbound" {
			who = "company"
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", m.Ref, who, m.Text)
	}
	if utf8.RuneCountInString(b.String()) > maxTranscriptRunes {
		return nil, fmt.Errorf("llamacpp: conversation longer than %d characters does not fit the model context", maxTranscriptRunes)
	}
	// The grammar only allows refs of the customer's own messages.
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"intent", "urgency", "evidence", "draft_reply", "needs_human"},
		"properties": map[string]any{
			"intent":  map[string]any{"type": "string", "enum": replyIntents},
			"urgency": map[string]any{"type": "string", "enum": []string{"low", "normal", "high"}},
			"evidence": map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"message_ref", "quote"},
				"properties": map[string]any{
					"message_ref": map[string]any{"type": "string", "enum": in.InboundRefs()},
					"quote":       map[string]any{"type": "string", "minLength": 3, "maxLength": 300},
				},
			},
			"draft_reply": map[string]any{"type": "string", "minLength": 1, "maxLength": 800},
			"needs_human": map[string]any{"type": "boolean"},
		},
	}
	var ans replyModelAnswer
	if err := p.complete(ctx, fmt.Sprintf(replySystem, languageName(in.Language)), "Conversation, oldest first:\n"+b.String(), schema, 500, &ans); err != nil {
		return nil, err
	}
	out := replyOutput{
		Schema: "message_reply.output.v1", Intent: ans.Intent, Urgency: ans.Urgency,
		NeedsHuman: ans.NeedsHuman || replycheck.NeedsHuman[ans.Intent],
		Language:   in.Language, Generator: p.generator(),
	}
	msg, _ := in.Inbound(ans.Evidence.MessageRef)
	if q, ok := anchor(msg.Text, ans.Evidence.Quote); ok {
		out.Evidence = &replyEvidence{MessageRef: msg.Ref, Quote: q}
	} else if ans.Intent != "other" {
		// A classification without evidence is a guess; sample again.
		return nil, modelgw.Transient(errors.New("llamacpp: intent evidence is not in the cited message"))
	}
	draft := strings.TrimSpace(ans.DraftReply)
	switch bad := in.Unsupported(draft); {
	case draft == "":
		out.NeedsHuman = true
	case bad != nil:
		// A draft that states facts the conversation does not contain is not
		// offered at all: a person writes the answer.
		out.NeedsHuman = true
		if p.Log != nil {
			p.Log.Info("llm draft dropped: unsupported facts", "count", len(bad))
		}
	default:
		out.DraftReply = &draft
	}
	return json.Marshal(out)
}
