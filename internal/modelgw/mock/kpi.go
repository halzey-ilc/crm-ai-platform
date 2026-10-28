package mock

import (
	"encoding/json"
	"errors"
	"math"
	"sort"

	"crmai/internal/kpinum"
)

// TaskKpiExplanation explains KPI changes. The mock restates the CRM's numbers
// in fixed templates (Kyrgyz falls back to Russian; it is a fixture).
const TaskKpiExplanation = "kpi_explanation"

const maxObservations = 3

var kpiTexts = map[string]struct {
	headline, empty, up, down, flat, recommend string
}{
	"ru": {
		headline:  "Главные изменения за период.",
		empty:     "Недостаточно данных: за период нет значений показателей.",
		up:        "%s: %s против %s в прошлом периоде (рост на %s).",
		down:      "%s: %s против %s в прошлом периоде (снижение на %s).",
		flat:      "%s: без изменений, %s.",
		recommend: "Разберите причины снижения показателя «%s».",
	},
	"en": {
		headline:  "Main changes this period.",
		empty:     "Not enough data: no metric values for the period.",
		up:        "%s: %s vs %s in the previous period (up %s).",
		down:      "%s: %s vs %s in the previous period (down %s).",
		flat:      "%s: unchanged at %s.",
		recommend: "Investigate why “%s” went down.",
	},
}

type kpiItem struct {
	MetricCodes []string `json:"metric_codes"`
	Text        string   `json:"text"`
}

type kpiOut struct {
	Schema           string    `json:"schema"`
	Headline         string    `json:"headline"`
	Observations     []kpiItem `json:"observations"`
	Recommendations  []kpiItem `json:"recommendations"`
	InsufficientData bool      `json:"insufficient_data"`
	Language         string    `json:"language"`
	Generator        generator `json:"generator"`
}

func kpiExplanation(raw json.RawMessage) (json.RawMessage, error) {
	in, err := kpinum.Parse(raw)
	if err != nil {
		return nil, errors.New("mock: input is not a kpi_explanation input")
	}
	t, ok := kpiTexts[in.Language]
	if !ok {
		t = kpiTexts["ru"]
	}
	out := kpiOut{
		Schema: "kpi_explanation.output.v1", Observations: []kpiItem{}, Recommendations: []kpiItem{},
		Language: in.Language, Generator: generator{Provider: Name, Model: Model, IsMock: true},
	}
	if in.Insufficient() {
		out.Headline = t.empty
		out.InsufficientData = true
		return json.Marshal(out)
	}
	// Biggest relative moves first; metrics without both values are skipped.
	var ms []kpinum.Metric
	for _, m := range in.Metrics {
		if m.Current != nil && m.Previous != nil && m.Change != nil {
			ms = append(ms, m)
		}
	}
	weight := func(m kpinum.Metric) float64 {
		if m.ChangePct != nil {
			return math.Abs(*m.ChangePct)
		}
		return math.Abs(*m.Change)
	}
	sort.SliceStable(ms, func(i, j int) bool { return weight(ms[i]) > weight(ms[j]) })
	for _, m := range ms[:min(maxObservations, len(ms))] {
		cur, prev, ch := kpinum.Format(*m.Current), kpinum.Format(*m.Previous), kpinum.Format(math.Abs(*m.Change))
		var text string
		switch {
		case *m.Change > 0:
			text = sprintf(t.up, m.Name, cur, prev, ch)
		case *m.Change < 0:
			text = sprintf(t.down, m.Name, cur, prev, ch)
			out.Recommendations = append(out.Recommendations, kpiItem{MetricCodes: []string{m.Code}, Text: sprintf(t.recommend, m.Name)})
		default:
			text = sprintf(t.flat, m.Name, cur)
		}
		out.Observations = append(out.Observations, kpiItem{MetricCodes: []string{m.Code}, Text: text})
	}
	// No counts in the headline: every number in a text must come from the input.
	out.Headline = t.headline
	if len(out.Observations) == 0 {
		// Values exist but no period-over-period comparison is possible.
		m := in.Metrics[0]
		for _, x := range in.Metrics {
			if x.Current != nil && *x.Current != 0 {
				m = x
				break
			}
		}
		out.Observations = append(out.Observations, kpiItem{MetricCodes: []string{m.Code}, Text: sprintf(t.flat, m.Name, kpinum.Format(*m.Current))})
	}
	return json.Marshal(out)
}
