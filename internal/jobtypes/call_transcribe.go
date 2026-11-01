package jobtypes

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TaskTranscribe is the model task of call.transcribe.v1 (speech recognition).
const TaskTranscribe = "transcribe"

// CallTranscribeV1 turns an uploaded call recording into a timed transcript with
// speakers (capability: call intelligence). Its text can then be summarised with
// call.summary.v2.
func CallTranscribeV1() Definition {
	return Definition{
		Type:         "call.transcribe.v1",
		Capability:   "call-intelligence",
		Task:         TaskTranscribe,
		InputSchema:  "call_transcribe.input.v1",
		OutputSchema: "call_transcript.output.v1",
		MaxAttempts:  3,
		// Measured on the dev laptop CPU: Whisper large-v3-turbo q5_0 needs about 3x
		// the audio length (67 s for 21 s), so ~4 minutes of call fit; small is real
		// time but far less accurate. Longer calls need the GPU server.
		Timeout: 12 * time.Minute,
		// A deployment without a speech model still starts; these jobs then fail
		// with provider_error instead of blocking every other job type.
		OptionalTask: true,
		Ground:       groundTranscript,
	}
}

// groundTranscript checks what a schema cannot: segments are in time order, each
// ends after it starts, the joined text matches the segments, the language is the
// requested one, and speakers are only named when the input declared the channels.
func groundTranscript(input, output json.RawMessage) error {
	var in struct {
		Language string `json:"language"`
		Channels string `json:"channels"`
	}
	var out struct {
		Language string  `json:"language"`
		Duration float64 `json:"duration_s"`
		Segments []struct {
			Start   float64 `json:"start_s"`
			End     float64 `json:"end_s"`
			Speaker string  `json:"speaker"`
			Text    string  `json:"text"`
		} `json:"segments"`
		Text         string `json:"text"`
		Insufficient bool   `json:"insufficient_data"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return err
	}
	if err := json.Unmarshal(output, &out); err != nil {
		return err
	}
	if out.Language != in.Language {
		return fmt.Errorf("transcript language %q differs from the requested %q", out.Language, in.Language)
	}
	if out.Insufficient != (len(out.Segments) == 0) {
		return errors.New("insufficient_data must be true exactly when there are no segments")
	}
	parts := make([]string, 0, len(out.Segments))
	prev := 0.0
	for i, s := range out.Segments {
		if s.End < s.Start || s.Start+0.5 < prev || s.End > out.Duration+1 {
			return fmt.Errorf("segment %d has inconsistent times", i)
		}
		if in.Channels == "mono" && s.Speaker != "unknown" {
			return fmt.Errorf("segment %d names a speaker in a mono recording", i)
		}
		prev = s.Start
		parts = append(parts, s.Text)
	}
	if strings.Join(parts, " ") != out.Text {
		return errors.New("text must be the segments joined by spaces")
	}
	return nil
}
