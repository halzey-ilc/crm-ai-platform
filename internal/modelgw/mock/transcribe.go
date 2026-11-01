package mock

import (
	"encoding/json"
	"errors"
)

// TaskTranscribe is speech recognition (call.transcribe.v1).
const TaskTranscribe = "transcribe"

var mockTranscript = map[string]string{
	"ru": "Тестовая расшифровка: аудио не распознавалось.",
	"ky": "Сыноо расшифровкасы: аудио таанылган жок.",
	"en": "Test transcript: the audio was not recognised.",
}

// transcribe does not read the audio: it returns one fixed segment so that the
// job pipeline can be exercised without a speech model.
func transcribe(raw json.RawMessage) (json.RawMessage, error) {
	var in struct {
		Language string `json:"language"`
		Channels string `json:"channels"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errors.New("mock: input is not a call_transcribe input")
	}
	text, ok := mockTranscript[in.Language]
	if !ok {
		return nil, errors.New("mock: unsupported language")
	}
	speaker := "manager"
	if in.Channels == "mono" {
		speaker = "unknown"
	}
	return json.Marshal(map[string]any{
		"schema":     "call_transcript.output.v1",
		"language":   in.Language,
		"duration_s": 2.0,
		"segments": []map[string]any{
			{"start_s": 0.0, "end_s": 2.0, "speaker": speaker, "text": text},
		},
		"text":              text,
		"insufficient_data": false,
		"generator":         map[string]any{"provider": Name, "model": Model, "is_mock": true},
	})
}
