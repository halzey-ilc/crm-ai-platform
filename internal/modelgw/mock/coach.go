package mock

import (
	"encoding/json"
	"errors"

	"crmai/internal/dealcoach"
)

// TaskDealCoach prepares a deal brief. The mock applies fixed rules to the
// CRM's fields (overdue close date, no open task) and fills templates; it is a
// fixture, not coaching.
const TaskDealCoach = "deal_coach"

var coachTexts = map[string]struct {
	stage, status, closePassed, noStep, overdue, history, qDecision, qBudget, empty string
}{
	"ru": {
		stage:       "Сделка «%s» на этапе «%s».",
		status:      "Сделка уже закрыта.",
		closePassed: "Ожидаемая дата закрытия прошла, а сделка ещё открыта.",
		noStep:      "Нет запланированной задачи по сделке.",
		overdue:     "Задача просрочена: %s",
		history:     "Последняя запись в истории: %s",
		qDecision:   "Кто принимает решение и когда оно будет принято?",
		qBudget:     "Подтверждён ли бюджет на сделку?",
		empty:       "Недостаточно данных: по сделке нет истории.",
	},
	"en": {
		stage:       "Deal “%s” is at stage “%s”.",
		status:      "The deal is already closed.",
		closePassed: "The expected close date has passed but the deal is still open.",
		noStep:      "There is no planned task for the deal.",
		overdue:     "Overdue task: %s",
		history:     "Latest history entry: %s",
		qDecision:   "Who makes the decision, and when?",
		qBudget:     "Is the budget for the deal confirmed?",
		empty:       "Not enough data: the deal has no history.",
	},
}

type coachOut struct {
	Schema           string            `json:"schema"`
	Situation        []dealcoach.Point `json:"situation"`
	Risks            []dealcoach.Point `json:"risks"`
	Questions        []dealcoach.Point `json:"questions"`
	NextStep         *dealcoach.Point  `json:"next_step"`
	InsufficientData bool              `json:"insufficient_data"`
	Language         string            `json:"language"`
	Generator        generator         `json:"generator"`
}

func dealCoach(raw json.RawMessage) (json.RawMessage, error) {
	in, err := dealcoach.Parse(raw)
	if err != nil {
		return nil, errors.New("mock: input is not a deal_coach input")
	}
	t, ok := coachTexts[in.Language]
	if !ok {
		t = coachTexts["ru"] // Kyrgyz falls back to Russian; it is a fixture
	}
	d := in.Deal
	cite := func(text string, refs ...string) dealcoach.Point { return dealcoach.Point{Text: text, Refs: refs} }
	out := coachOut{
		Schema:    "deal_coach.output.v1",
		Situation: []dealcoach.Point{cite(sprintf(t.stage, d.Title, d.Stage), d.Ref)},
		Risks:     []dealcoach.Point{}, Questions: []dealcoach.Point{},
		Language: in.Language, Generator: generator{Provider: Name, Model: Model, IsMock: true},
	}
	if d.Status != "open" {
		out.Situation = append(out.Situation, cite(t.status, d.Ref))
	}
	if in.Insufficient() {
		out.Situation = append(out.Situation, cite(t.empty, d.Ref))
		out.InsufficientData = true
		return json.Marshal(out)
	}
	last := in.History[len(in.History)-1]
	out.Situation = append(out.Situation, cite(sprintf(t.history, label(last)), last.Ref))
	if d.Status == "open" && d.ExpectedCloseDate != nil && *d.ExpectedCloseDate < in.Today {
		out.Risks = append(out.Risks, cite(t.closePassed, d.Ref))
	}
	for _, it := range in.History {
		if it.Kind != "task" || it.Status == nil {
			continue
		}
		switch *it.Status {
		case "overdue":
			if len(out.Risks) < 4 {
				out.Risks = append(out.Risks, cite(sprintf(t.overdue, label(it)), it.Ref))
			}
		case "open":
			if out.NextStep == nil {
				p := cite(label(it), it.Ref)
				out.NextStep = &p
			}
		}
	}
	if d.Status == "open" && out.NextStep == nil && len(out.Risks) < 4 {
		out.Risks = append(out.Risks, cite(t.noStep, d.Ref))
	}
	out.Questions = append(out.Questions, cite(t.qDecision, d.Ref), cite(t.qBudget, d.Ref))
	return json.Marshal(out)
}

// label is the item's title, or else the start of its text.
func label(it dealcoach.Item) string {
	if it.Title != nil && *it.Title != "" {
		return truncate(*it.Title, 200)
	}
	if it.Text != nil {
		return truncate(*it.Text, 200)
	}
	return it.Kind
}
