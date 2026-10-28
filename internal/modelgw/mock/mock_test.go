package mock_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"crmai/contracts"
	"crmai/internal/modelgw"
	"crmai/internal/modelgw/mock"
)

const transcript = "Здравствуйте, это Айбек из компании. Клиенту нужна интеграция с 1С! " +
	"Бюджет пока не утверждён.\nДоговорились, что я перезвоню в пятницу."

func invoke(t *testing.T, in string) modelgw.Response {
	t.Helper()
	resp, err := mock.Provider{}.Invoke(context.Background(), modelgw.Request{Task: mock.TaskCallSummary, Input: []byte(in)})
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestOutputSatisfiesContractAndIsGrounded(t *testing.T) {
	in, _ := json.Marshal(map[string]string{"transcript": transcript, "language": "ru"})
	resp := invoke(t, string(in))

	s, _ := contracts.Load()
	if err := s.Validate("call_summary.output.v1", resp.Output); err != nil {
		t.Fatalf("output violates contract: %v", err)
	}
	var out struct {
		KeyPoints []struct {
			EvidenceQuote string `json:"evidence_quote"`
		} `json:"key_points"`
		NextStep *struct {
			EvidenceQuote string `json:"evidence_quote"`
		} `json:"next_step"`
		Generator struct {
			IsMock bool `json:"is_mock"`
		} `json:"generator"`
	}
	_ = json.Unmarshal(resp.Output, &out)
	if !out.Generator.IsMock {
		t.Error("mock output must be labelled is_mock")
	}
	if len(out.KeyPoints) != 3 {
		t.Errorf("want 3 key points, got %d", len(out.KeyPoints))
	}
	for _, k := range out.KeyPoints {
		if !strings.Contains(transcript, k.EvidenceQuote) {
			t.Errorf("quote not in transcript: %q", k.EvidenceQuote)
		}
	}
	if out.NextStep == nil || !strings.Contains(out.NextStep.EvidenceQuote, "перезвоню") {
		t.Errorf("next step not detected: %+v", out.NextStep)
	}
}

func TestDeterministic(t *testing.T) {
	in := `{"transcript":"Первое. Второе.","language":"ru"}`
	a, b := invoke(t, in), invoke(t, in)
	if !bytes.Equal(a.Output, b.Output) {
		t.Fatal("same input must give identical output")
	}
}

func TestSentencesAreVerbatimSubstrings(t *testing.T) {
	text := "  Один…два?! три\n\nчетыре.  "
	for _, s := range mock.Sentences(text) {
		if s == "" || !strings.Contains(text, s) {
			t.Errorf("bad sentence %q", s)
		}
	}
}

func TestRejectsEmptyTranscript(t *testing.T) {
	_, err := mock.Provider{}.Invoke(context.Background(),
		modelgw.Request{Task: mock.TaskCallSummary, Input: []byte(`{"transcript":"  \n ","language":"ru"}`)})
	if err == nil {
		t.Fatal("whitespace-only transcript must fail")
	}
}
