// Package callsummary holds the rules shared by every provider of
// call.summary.v2: when a text is too thin to summarise, and how a v1-shaped
// summary becomes a v2 result.
package callsummary

import (
	"encoding/json"
	"strings"
	"unicode"
)

// Minimum content before a summary is attempted.
const (
	MinWords   = 3
	MinLetters = 20
)

// TooShort reports whether text has too little content to summarise.
func TooShort(text string) bool {
	letters := 0
	for _, r := range text {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	return len(strings.Fields(text)) < MinWords || letters < MinLetters
}

// Notices replace the summary when there is nothing to summarise, so no
// free text is written about a call without content.
var Notices = map[string]string{
	"ru":      "Недостаточно данных: в заметках звонка нет фактов для резюме.",
	"ky":      "Маалымат жетишсиз: чалуунун жазууларында корутунду үчүн фактылар жок.",
	"en":      "Not enough data: the call notes contain no facts to summarise.",
	"unknown": "Недостаточно данных / Not enough data.",
}

// Notice returns the notice for a language (falls back to "unknown").
func Notice(language string) string {
	if n, ok := Notices[language]; ok {
		return n
	}
	return Notices["unknown"]
}

// Insufficient builds the v2 result for a text that cannot be summarised.
func Insufficient(language string, generator any) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"schema": "call_summary.output.v2", "summary": Notice(language), "key_points": []any{},
		"next_step": nil, "insufficient_data": true, "language": language, "generator": generator,
	})
}

// FromV1 turns a call_summary.output.v1 document into v2. A summary that
// produced neither a grounded key point nor a next step is reported as
// insufficient, and its free-text summary is replaced by the notice.
func FromV1(v1 json.RawMessage) (json.RawMessage, error) {
	var doc map[string]any
	if err := json.Unmarshal(v1, &doc); err != nil {
		return nil, err
	}
	points, _ := doc["key_points"].([]any)
	empty := len(points) == 0 && doc["next_step"] == nil
	doc["schema"] = "call_summary.output.v2"
	doc["insufficient_data"] = empty
	if empty {
		language, _ := doc["language"].(string)
		doc["summary"] = Notice(language)
	}
	return json.Marshal(doc)
}
