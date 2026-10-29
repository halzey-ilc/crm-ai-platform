package mock

import (
	"encoding/json"
	"errors"
	"strings"

	"crmai/internal/replycheck"
)

// TaskMessageReply classifies a customer message and drafts a reply. The mock
// matches keyword stems and answers with fixed templates that contain no facts;
// it is a fixture, not a quality claim about any language.
const TaskMessageReply = "message_reply"

// Checked in this order: the first intent whose stem occurs wins.
var intentMarkers = []struct {
	intent string
	stems  []string
}{
	{"complaint", []string{"жалоб", "недовол", "не работает", "сломал", "верните", "даттан", "иштебей", "complain", "not working", "broken", "refund"}},
	{"cancellation", []string{"отмен", "откаж", "расторг", "жокко чыгар", "cancel"}},
	{"order", []string{"заказ", "оформ", "куплю", "купить", "буйрут", "сатып ал", "order", "buy"}},
	{"price_request", []string{"цен", "стоим", "сколько стоит", "прайс", "баасы", "канча турат", "price", "cost", "how much"}},
	{"meeting_request", []string{"встреч", "созвон", "демо", "перезвон", "жолугуш", "чалып", "meeting", "demo", "call me"}},
	{"gratitude", []string{"спасибо", "благодар", "рахмат", "thank"}},
}

var urgentStems = []string{"срочно", "немедленно", "тез арада", "шашылыш", "urgent", "asap"}

var replyTemplates = map[string]map[string]string{
	"ru": {
		"gratitude": "Спасибо и вам! Если появятся вопросы, пишите.",
		"complaint": "Здравствуйте! Нам очень жаль, что так получилось. Менеджер разберётся в ситуации и свяжется с вами.",
		"default":   "Здравствуйте! Спасибо за сообщение. Менеджер уточнит детали и ответит вам в ближайшее время.",
	},
	"ky": {
		"gratitude": "Сизге да рахмат! Суроолоруңуз болсо, жазыңыз.",
		"complaint": "Саламатсызбы! Ушундай болуп калганына өкүнөбүз. Менеджер маселени карап, сиз менен байланышат.",
		"default":   "Саламатсызбы! Билдирүүңүз үчүн рахмат. Менеджер маалыматты тактап, жакын арада жооп берет.",
	},
	"en": {
		"gratitude": "Thank you too! Write to us if you have any questions.",
		"complaint": "Hello! We are sorry this happened. A manager will look into it and contact you.",
		"default":   "Hello! Thank you for your message. A manager will check the details and reply shortly.",
	},
}

type replyEvidence struct {
	MessageRef string `json:"message_ref"`
	Quote      string `json:"quote"`
}

type replyOut struct {
	Schema     string         `json:"schema"`
	Intent     string         `json:"intent"`
	Urgency    string         `json:"urgency"`
	Evidence   *replyEvidence `json:"evidence"`
	DraftReply *string        `json:"draft_reply"`
	NeedsHuman bool           `json:"needs_human"`
	Language   string         `json:"language"`
	Generator  generator      `json:"generator"`
}

func messageReply(raw json.RawMessage) (json.RawMessage, error) {
	in, err := replycheck.Parse(raw)
	if err != nil || !in.EndsWithCustomer() {
		return nil, errors.New("mock: input is not a message_reply input ending with a customer message")
	}
	out := replyOut{
		Schema: "message_reply.output.v1", Intent: "other", Urgency: "normal",
		Language: in.Language, Generator: generator{Provider: Name, Model: Model, IsMock: true},
	}
	// Newest customer message first: it is the one the reply answers.
	latest := in.Latest()
	for i := len(latest) - 1; i >= 0 && out.Evidence == nil; i-- {
		for _, s := range Sentences(latest[i].Text) {
			if intent, ok := intentOf(s); ok {
				out.Intent = intent
				out.Evidence = &replyEvidence{MessageRef: latest[i].Ref, Quote: truncate(s, maxQuoteRunes)}
				break
			}
		}
	}
	all := strings.ToLower(joinTexts(latest))
	switch {
	case containsAny(all, urgentStems) || replycheck.NeedsHuman[out.Intent]:
		out.Urgency = "high"
	case out.Intent == "gratitude" || out.Intent == "other":
		out.Urgency = "low"
	}
	t, ok := replyTemplates[in.Language]
	if !ok {
		t = replyTemplates["ru"]
	}
	draft := t["default"]
	if s, ok := t[out.Intent]; ok {
		draft = s
	}
	out.DraftReply = &draft
	// Only thanks can be answered without facts the mock does not have.
	out.NeedsHuman = out.Intent != "gratitude"
	return json.Marshal(out)
}

func intentOf(sentence string) (string, bool) {
	lower := strings.ToLower(sentence)
	for _, m := range intentMarkers {
		if containsAny(lower, m.stems) {
			return m.intent, true
		}
	}
	if strings.HasSuffix(strings.TrimSpace(sentence), "?") {
		return "question", true
	}
	return "", false
}

func containsAny(s string, stems []string) bool {
	for _, st := range stems {
		if strings.Contains(s, st) {
			return true
		}
	}
	return false
}

func joinTexts(ms []replycheck.Message) string {
	parts := make([]string, 0, len(ms))
	for _, m := range ms {
		parts = append(parts, m.Text)
	}
	return strings.Join(parts, "\n")
}
