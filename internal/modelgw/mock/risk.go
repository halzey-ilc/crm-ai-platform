package mock

import (
	"encoding/json"
	"errors"

	"crmai/internal/riskreview"
)

// TaskRiskReview explains risk signals. The mock fills neutral fixed templates
// per kind (Kyrgyz falls back to Russian; it is a fixture).
const TaskRiskReview = "risk_review"

var riskKinds = []string{"won_without_activity", "reopened_after_close", "amount_outlier", "bulk_disqualification", "deals_lost_burst", "invoices_voided"}

var riskTexts = map[string]struct {
	headline, empty string
	item, check     map[string]string
}{
	"ru": {
		headline: "Сигналы для проверки. Это не доказательство ошибок сотрудников.",
		empty:    "Сигналов по правилам компании нет.",
		item: map[string]string{
			"won_without_activity":  "Сделки выиграны без записанных контактов: %d.",
			"reopened_after_close":  "Закрытые сделки снова открыты: %d.",
			"amount_outlier":        "Открытые сделки с суммой намного выше обычной: %d.",
			"bulk_disqualification": "Много лидов дисквалифицировано за один день: %d случаев.",
			"deals_lost_burst":      "Много сделок отмечено проигранными за один день: %d случаев.",
			"invoices_voided":       "Сотрудники с несколькими аннулированными счетами: %d.",
		},
		check: map[string]string{
			"won_without_activity":  "Уточните у владельца сделки, где записаны контакты с клиентом.",
			"reopened_after_close":  "Посмотрите причину повторного открытия в истории сделки.",
			"amount_outlier":        "Проверьте, правильно ли указана сумма сделки.",
			"bulk_disqualification": "Уточните, была ли это плановая чистка базы лидов.",
			"deals_lost_burst":      "Уточните причины проигрыша этих сделок.",
			"invoices_voided":       "Сверьте причины аннулирования счетов.",
		},
	},
	"en": {
		headline: "Signals to check. They are not proof of anyone's mistake.",
		empty:    "No signals under the company's rules.",
		item: map[string]string{
			"won_without_activity":  "Deals won with no recorded contact: %d.",
			"reopened_after_close":  "Closed deals reopened: %d.",
			"amount_outlier":        "Open deals far above the usual amount: %d.",
			"bulk_disqualification": "Many leads disqualified on one day: %d cases.",
			"deals_lost_burst":      "Many deals marked lost on one day: %d cases.",
			"invoices_voided":       "People with several voided invoices: %d.",
		},
		check: map[string]string{
			"won_without_activity":  "Ask the deal owner where the client contacts were recorded.",
			"reopened_after_close":  "Look at why the deal was reopened in its history.",
			"amount_outlier":        "Check that the deal amount was entered correctly.",
			"bulk_disqualification": "Ask whether this was a planned clean-up of the lead base.",
			"deals_lost_burst":      "Ask for the reasons these deals were lost.",
			"invoices_voided":       "Compare the reasons the invoices were voided.",
		},
	},
}

type riskOut struct {
	Schema           string             `json:"schema"`
	Headline         string             `json:"headline"`
	Items            []riskreview.Cited `json:"items"`
	Checks           []riskreview.Cited `json:"checks"`
	InsufficientData bool               `json:"insufficient_data"`
	IsEvidence       bool               `json:"is_evidence"`
	Language         string             `json:"language"`
	Generator        generator          `json:"generator"`
}

func riskReview(raw json.RawMessage) (json.RawMessage, error) {
	in, err := riskreview.Parse(raw)
	if err != nil {
		return nil, errors.New("mock: input is not a risk_review input")
	}
	t, ok := riskTexts[in.Language]
	if !ok {
		t = riskTexts["ru"]
	}
	out := riskOut{
		Schema: "risk_review.output.v1", Items: []riskreview.Cited{}, Checks: []riskreview.Cited{},
		Language: in.Language, Generator: generator{Provider: Name, Model: Model, IsMock: true},
	}
	if in.Insufficient() {
		out.Headline, out.InsufficientData = t.empty, true
		return json.Marshal(out)
	}
	out.Headline = t.headline
	for _, kind := range riskKinds {
		var refs []string
		for _, s := range in.Signals {
			if s.Kind == kind && len(refs) < 10 && !contains(refs, s.Ref) {
				refs = append(refs, s.Ref)
			}
		}
		if len(refs) == 0 {
			continue
		}
		out.Items = append(out.Items, riskreview.Cited{Kind: kind, Text: sprintf(t.item[kind], in.Counts[kind]), SignalRefs: refs})
		out.Checks = append(out.Checks, riskreview.Cited{Text: t.check[kind], SignalRefs: refs[:1]})
	}
	return json.Marshal(out)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
