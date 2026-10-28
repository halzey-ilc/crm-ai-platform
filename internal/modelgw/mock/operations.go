package mock

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// TaskOperationsDigest explains operations findings. The mock fills fixed
// templates with counts and the oldest findings' refs; Kyrgyz falls back to
// the Russian templates (it is a fixture, not a translation).
const TaskOperationsDigest = "operations_digest"

const refsPerItem = 3

// Fixed presentation order of finding kinds.
var digestKinds = []string{"overdue_task", "deal_without_next_step", "stalled_deal", "stale_lead"}

type digestTemplate struct{ item, recommendation string }

var digestTemplates = map[string]map[string]digestTemplate{
	"ru": {
		"stale_lead":             {"Лиды без движения: %d (дольше всех — %d дн.).", "Свяжитесь в первую очередь с лидами, которые ждут дольше всего."},
		"overdue_task":           {"Просроченные задачи: %d (наибольшая просрочка — %d дн.).", "Закройте или перенесите просроченные задачи."},
		"deal_without_next_step": {"Сделки без следующего шага: %d (дольше всех — %d дн. без изменений).", "Запланируйте следующий шаг по каждой такой сделке."},
		"stalled_deal":           {"Сделки без изменений: %d (дольше всех — %d дн.).", "Проверьте, актуальны ли застоявшиеся сделки."},
	},
	"en": {
		"stale_lead":             {"Leads without activity: %d (longest: %d days).", "Contact the leads that have waited longest first."},
		"overdue_task":           {"Overdue tasks: %d (most overdue: %d days).", "Close or reschedule overdue tasks."},
		"deal_without_next_step": {"Deals without a next step: %d (longest: %d days unchanged).", "Schedule a next step for each of these deals."},
		"stalled_deal":           {"Deals without changes: %d (longest: %d days).", "Check whether the stalled deals are still relevant."},
	},
}

var digestHeadlines = map[string][2]string{
	"ru": {"Требуют внимания: %d.", "Нарушений по правилам компании не найдено."},
	"en": {"Need attention: %d.", "No issues found under the company's rules."},
}

type digestIn struct {
	Language string         `json:"language"`
	Counts   map[string]int `json:"counts"`
	Findings []struct {
		Ref     string `json:"ref"`
		Kind    string `json:"kind"`
		AgeDays int    `json:"age_days"`
	} `json:"findings"`
}

type digestItem struct {
	Kind        string   `json:"kind"`
	Text        string   `json:"text"`
	FindingRefs []string `json:"finding_refs"`
}

type digestRec struct {
	Text        string   `json:"text"`
	FindingRefs []string `json:"finding_refs"`
}

type digestOut struct {
	Schema           string       `json:"schema"`
	Headline         string       `json:"headline"`
	Items            []digestItem `json:"items"`
	Recommendations  []digestRec  `json:"recommendations"`
	InsufficientData bool         `json:"insufficient_data"`
	Language         string       `json:"language"`
	Generator        generator    `json:"generator"`
}

func operationsDigest(raw json.RawMessage) (json.RawMessage, error) {
	var in digestIn
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errors.New("mock: input is not an operations_digest input")
	}
	lang := in.Language
	if _, ok := digestTemplates[lang]; !ok {
		lang = "ru"
	}
	out := digestOut{
		Schema:          "operations_digest.output.v1",
		Items:           []digestItem{},
		Recommendations: []digestRec{},
		Language:        in.Language,
		Generator:       generator{Provider: Name, Model: Model, IsMock: true},
	}
	if len(in.Findings) == 0 {
		out.Headline = digestHeadlines[lang][1]
		out.InsufficientData = true
		return json.Marshal(out)
	}

	byKind := map[string][]int{} // kind -> indices into in.Findings
	for i, f := range in.Findings {
		byKind[f.Kind] = append(byKind[f.Kind], i)
	}
	total := 0
	for _, kind := range digestKinds {
		idx := byKind[kind]
		if len(idx) == 0 || in.Counts[kind] == 0 {
			continue
		}
		total += in.Counts[kind]
		sort.SliceStable(idx, func(a, b int) bool { return in.Findings[idx[a]].AgeDays > in.Findings[idx[b]].AgeDays })
		refs := []string{}
		for _, i := range idx[:min(refsPerItem, len(idx))] {
			refs = append(refs, in.Findings[i].Ref)
		}
		t := digestTemplates[lang][kind]
		out.Items = append(out.Items, digestItem{
			Kind:        kind,
			Text:        fmt.Sprintf(t.item, in.Counts[kind], in.Findings[idx[0]].AgeDays),
			FindingRefs: refs,
		})
		out.Recommendations = append(out.Recommendations, digestRec{Text: t.recommendation, FindingRefs: refs})
	}
	out.Headline = fmt.Sprintf(digestHeadlines[lang][0], total)
	return json.Marshal(out)
}
