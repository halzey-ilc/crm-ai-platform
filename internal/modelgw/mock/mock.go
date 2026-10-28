// Package mock is a deterministic, offline provider for development and
// tests. It does not use any model: it extracts sentences from the input so
// that its output is structurally valid, grounded in the input and
// reproducible. Its results are labelled is_mock=true and must never be
// presented as AI analysis.
package mock

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"crmai/internal/modelgw"
)

const (
	Name  = "mock"
	Model = "mock-extractive-v1"

	TaskCallSummary = "call_summary"

	maxQuoteRunes = 300
	maxKeyPoints  = 3
)

// Provider is the mock provider.
type Provider struct{}

func (Provider) Name() string { return Name }

func (Provider) Supports(task string) bool {
	return task == TaskCallSummary || task == TaskOperationsDigest
}

func (Provider) Invoke(ctx context.Context, req modelgw.Request) (modelgw.Response, error) {
	if err := ctx.Err(); err != nil {
		return modelgw.Response{}, err
	}
	switch req.Task {
	case TaskCallSummary:
		out, err := callSummary(req.Input)
		if err != nil {
			return modelgw.Response{}, err
		}
		return modelgw.Response{Output: out, Provider: Name, Model: Model}, nil
	case TaskOperationsDigest:
		out, err := operationsDigest(req.Input)
		if err != nil {
			return modelgw.Response{}, err
		}
		return modelgw.Response{Output: out, Provider: Name, Model: Model}, nil
	default:
		return modelgw.Response{}, errors.New("mock: unsupported task")
	}
}

type callInput struct {
	Transcript string `json:"transcript"`
	Language   string `json:"language"`
}

type grounded struct {
	Text          string `json:"text"`
	EvidenceQuote string `json:"evidence_quote"`
}

type callOutput struct {
	Schema    string     `json:"schema"`
	Summary   string     `json:"summary"`
	KeyPoints []grounded `json:"key_points"`
	NextStep  *grounded  `json:"next_step"`
	Language  string     `json:"language"`
	Generator generator  `json:"generator"`
}

type generator struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	IsMock   bool   `json:"is_mock"`
}

// nextStepMarkers are lowercase stems that suggest an agreed follow-up. This
// is a fixture heuristic, not a quality claim about Russian or Kyrgyz.
var nextStepMarkers = []string{
	"перезвон", "позвон", "встреч", "отправ", "пришл", "договорил",
	"чалам", "жолугуш", "жөнөт",
	"call back", "follow up", "send", "meeting", "agreed",
}

func callSummary(raw json.RawMessage) (json.RawMessage, error) {
	var in callInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errors.New("mock: input is not a call_summary input")
	}
	sentences := Sentences(in.Transcript)
	if len(sentences) == 0 {
		return nil, errors.New("mock: transcript has no text")
	}
	out := callOutput{
		Schema:    "call_summary.output.v1",
		Summary:   truncate(sentences[0], maxQuoteRunes),
		KeyPoints: []grounded{},
		Language:  in.Language,
		Generator: generator{Provider: Name, Model: Model, IsMock: true},
	}
	for _, s := range sentences {
		if len(out.KeyPoints) == maxKeyPoints {
			break
		}
		q := truncate(s, maxQuoteRunes)
		out.KeyPoints = append(out.KeyPoints, grounded{Text: q, EvidenceQuote: q})
	}
	for _, s := range sentences {
		lower := strings.ToLower(s)
		for _, m := range nextStepMarkers {
			if strings.Contains(lower, m) {
				q := truncate(s, maxQuoteRunes)
				out.NextStep = &grounded{Text: q, EvidenceQuote: q}
				break
			}
		}
		if out.NextStep != nil {
			break
		}
	}
	return json.Marshal(out)
}

// Sentences splits text at sentence punctuation and line breaks. Each
// returned sentence is a verbatim substring of text.
func Sentences(text string) []string {
	var out []string
	start := 0
	for i, r := range text {
		switch r {
		case '.', '!', '?', '…', '\n':
			end := i + utf8.RuneLen(r)
			if s := strings.TrimSpace(text[start:end]); s != "" && s != string(r) {
				out = append(out, s)
			}
			start = end
		}
	}
	if s := strings.TrimSpace(text[start:]); s != "" {
		out = append(out, s)
	}
	return out
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for pos := range s {
		if i == n {
			return strings.TrimSpace(s[:pos])
		}
		i++
	}
	return s
}

var _ modelgw.Provider = Provider{}
