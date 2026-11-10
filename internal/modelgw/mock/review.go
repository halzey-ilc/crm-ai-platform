package mock

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"crmai/internal/callreview"
)

// TaskCallReview reviews a call against a checklist. The mock marks a
// criterion met when a manager turn contains a word of its title and quotes
// the start of that turn; it is a fixture, not a review.
const TaskCallReview = "call_review"

var reviewTexts = map[string]struct{ met, missed, empty, strength, improve string }{
	"ru": {"Есть в разговоре.", "В разговоре не найдено.", "Звонок слишком короткий для оценки.", "Менеджер затронул пункты чек-листа.", "Пройдите пропущенные пункты чек-листа."},
	"ky": {"Сүйлөшүүдө бар.", "Сүйлөшүүдө табылган жок.", "Чалуу баалоо үчүн өтө кыска.", "Менеджер чек-листтин пункттарына токтолду.", "Калтырылган пункттарды аткарыңыз."},
	"en": {"Found in the call.", "Not found in the call.", "The call is too short to review.", "The manager covered checklist points.", "Cover the missed checklist points."},
}

type reviewOut struct {
	Schema           string               `json:"schema"`
	Criteria         []callreview.Verdict `json:"criteria"`
	Strengths        []callreview.Advice  `json:"strengths"`
	Improvements     []callreview.Advice  `json:"improvements"`
	InsufficientData bool                 `json:"insufficient_data"`
	Language         string               `json:"language"`
	Generator        generator            `json:"generator"`
}

func callReview(raw json.RawMessage) (json.RawMessage, error) {
	in, err := callreview.Parse(raw)
	if err != nil {
		return nil, errors.New("mock: input is not a call_review input")
	}
	t, ok := reviewTexts[in.Language]
	if !ok {
		t = reviewTexts["ru"]
	}
	out := reviewOut{
		Schema: "call_review.output.v1", Strengths: []callreview.Advice{}, Improvements: []callreview.Advice{},
		Language: in.Language, Generator: generator{Provider: Name, Model: Model, IsMock: true},
		InsufficientData: in.Insufficient(),
	}
	found, missed := 0, 0
	for _, c := range in.Criteria {
		v := callreview.Verdict{Code: c.Code, Status: "not_applicable", Comment: t.empty}
		if !out.InsufficientData {
			v.Status, v.Comment = "missed", t.missed
			if s, ok := managerTurnAbout(in, c.Title); ok {
				v.Status, v.Comment = "met", t.met
				v.Evidence = &callreview.Evidence{SegmentID: s.ID, Quote: truncate(s.Text, 80)}
				found++
			} else {
				missed++
			}
		}
		out.Criteria = append(out.Criteria, v)
	}
	if found > 0 {
		out.Strengths = append(out.Strengths, callreview.Advice{Text: t.strength})
	}
	if missed > 0 {
		out.Improvements = append(out.Improvements, callreview.Advice{Text: t.improve})
	}
	return json.Marshal(out)
}

// managerTurnAbout finds a manager turn sharing a word stem (5+ letters) with the title.
func managerTurnAbout(in callreview.Input, title string) (callreview.Segment, bool) {
	for _, word := range strings.Fields(strings.ToLower(title)) {
		if utf8.RuneCountInString(word) < 5 {
			continue
		}
		stem := string([]rune(word)[:5])
		for _, s := range in.Segments {
			if s.Speaker != "customer" && strings.Contains(strings.ToLower(s.Text), stem) {
				return s, true
			}
		}
	}
	return callreview.Segment{}, false
}
