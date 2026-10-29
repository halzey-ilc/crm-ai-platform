package llamacpp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"crmai/internal/kpinum"
	"crmai/internal/modelgw"
)

const TaskKpiExplanation = "kpi_explanation"

const kpiSystem = `You are a business analyst explaining a CRM's KPI report to a sales director.
All numbers were calculated by the CRM and are correct. Rules:
- Do NOT calculate anything. Use only numbers that appear in the input (current, previous, change, change_pct).
- "percent" metrics are already in %%; their "change" is in percentage points.
- Write up to 3 short observations about the most important changes, each naming its metric_codes.
- Write up to 2 short practical recommendations, each naming the metric_codes it addresses.
- If a metric has no previous value, do not claim a trend for it.
- Never write metric codes inside text; use the metric names.
Write all texts in %s. Answer with JSON only.`

var emptyKpiHeadline = map[string]string{
	"ru": "Недостаточно данных: за период нет значений показателей.",
	"ky": "Маалымат жетишсиз: мезгил ичинде көрсөткүчтөрдүн маанилери жок.",
	"en": "Not enough data: no metric values for the period.",
}

type kpiItem struct {
	MetricCodes []string `json:"metric_codes"`
	Text        string   `json:"text"`
}

type kpiOutput struct {
	Schema           string    `json:"schema"`
	Headline         string    `json:"headline"`
	Observations     []kpiItem `json:"observations"`
	Recommendations  []kpiItem `json:"recommendations"`
	InsufficientData bool      `json:"insufficient_data"`
	Language         string    `json:"language"`
	Generator        generator `json:"generator"`
}

func (p Provider) kpiExplanation(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	in, err := kpinum.Parse(raw)
	if err != nil {
		return nil, errors.New("llamacpp: input is not a kpi_explanation input")
	}
	out := kpiOutput{
		Schema: "kpi_explanation.output.v1", Observations: []kpiItem{}, Recommendations: []kpiItem{},
		Language: in.Language, Generator: p.generator(),
	}
	if in.Insufficient() {
		// Nothing to explain: say so without asking the model (principle 7, ADR-005).
		out.Headline = emptyKpiHeadline[in.Language]
		out.InsufficientData = true
		return json.Marshal(out)
	}

	codes := map[string]any{"type": "array", "minItems": 1, "maxItems": 4, "uniqueItems": true,
		"items": map[string]any{"type": "string", "enum": in.Codes()}}
	item := map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"metric_codes", "text"},
		"properties": map[string]any{
			"metric_codes": codes,
			"text":         map[string]any{"type": "string", "minLength": 1, "maxLength": 400},
		},
	}
	schema := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"headline", "observations", "recommendations"},
		"properties": map[string]any{
			"headline":        map[string]any{"type": "string", "minLength": 1, "maxLength": 250},
			"observations":    map[string]any{"type": "array", "minItems": 1, "maxItems": 3, "items": item},
			"recommendations": map[string]any{"type": "array", "maxItems": 2, "items": item},
		},
	}
	// Only metrics with a value reach the model: on a CPU every prompt token costs time,
	// and an empty metric has nothing to explain.
	var withValues []kpinum.Metric
	for _, m := range in.Metrics {
		if (m.Current != nil && *m.Current != 0) || (m.Previous != nil && *m.Previous != 0) {
			withValues = append(withValues, m)
		}
	}
	user, _ := json.Marshal(map[string]any{"period": in.Period, "previous_period": in.PreviousPeriod, "metrics": withValues})
	var ans struct {
		Headline        string    `json:"headline"`
		Observations    []kpiItem `json:"observations"`
		Recommendations []kpiItem `json:"recommendations"`
	}
	if err := p.complete(ctx, fmt.Sprintf(kpiSystem, languageName(in.Language)), "KPI report:\n"+string(user), schema, 600, &ans); err != nil {
		return nil, err
	}

	// The grammar restricts metric codes; numbers can only be checked afterwards.
	allowed := in.AllowedNumbers()
	keep := func(items []kpiItem) ([]kpiItem, int) {
		kept, dropped := []kpiItem{}, 0
		for _, it := range items {
			text := strings.TrimSpace(it.Text)
			if text == "" || len(it.MetricCodes) == 0 || allowed.Unknown(text) != nil {
				dropped++
				continue
			}
			kept = append(kept, kpiItem{MetricCodes: it.MetricCodes, Text: text})
		}
		return kept, dropped
	}
	var d1, d2 int
	out.Observations, d1 = keep(ans.Observations)
	out.Recommendations, d2 = keep(ans.Recommendations)
	if d1+d2 > 0 && p.Log != nil {
		p.Log.Info("llm kpi items dropped: numbers not in the input", "count", d1+d2)
	}
	out.Headline = strings.TrimSpace(ans.Headline)
	if out.Headline == "" || allowed.Unknown(out.Headline) != nil {
		// A headline with an invented number is replaced, not trusted.
		out.Headline = map[string]string{"ru": "Изменения показателей за период.", "ky": "Мезгил ичиндеги көрсөткүчтөрдүн өзгөрүүлөрү.", "en": "KPI changes for the period."}[in.Language]
	}
	if len(out.Observations) == 0 {
		return nil, modelgw.Transient(errors.New("llamacpp: every observation contained numbers not in the input"))
	}
	return json.Marshal(out)
}
