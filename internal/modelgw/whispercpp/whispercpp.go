// Package whispercpp is the speech-recognition provider: it sends an uploaded
// call recording to a local whisper.cpp server (whisper-server, MIT) and turns
// its timed segments into a call_transcript.output.v1.
//
// Whisper has no Kyrgyz: Kyrgyz recordings go to a second server running a
// model fine-tuned for Kyrgyz (KyURL). Without one, Kyrgyz jobs fail at once
// instead of returning a transcript in the wrong language.
package whispercpp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"crmai/internal/modelgw"
)

const (
	Name           = "whispercpp"
	TaskTranscribe = "transcribe"

	maxSegments     = 5000
	maxSegmentRunes = 2000
)

// Audio is one stored recording.
type Audio struct {
	Bytes       []byte
	ContentType string
}

// AudioSource reads recordings uploaded through the gateway, within one tenant.
type AudioSource interface {
	GetAudio(ctx context.Context, tenantID, ref string) (Audio, bool, error)
}

// Provider calls whisper-server.
type Provider struct {
	URL string // whisper-server for ru and en
	// KyURL is a whisper-server with a model fine-tuned for Kyrgyz; empty means
	// Kyrgyz is not supported. KyLanguage is the language passed to it (the
	// fine-tuned model may have been trained under another language token).
	KyURL      string
	KyLanguage string
	Model      string // label reported in the generator block
	Audio      AudioSource
	HTTP       *http.Client
	Log        *slog.Logger
}

func (Provider) Name() string { return Name }

func (Provider) Supports(task string) bool { return task == TaskTranscribe }

type input struct {
	AudioRef string `json:"audio_ref"`
	Language string `json:"language"`
	Channels string `json:"channels"`
}

// Segment is one recognised utterance.
type Segment struct {
	Start   float64 `json:"start_s"`
	End     float64 `json:"end_s"`
	Speaker string  `json:"speaker"`
	Text    string  `json:"text"`
}

func (p Provider) Invoke(ctx context.Context, req modelgw.Request) (modelgw.Response, error) {
	var in input
	if err := json.Unmarshal(req.Input, &in); err != nil {
		return modelgw.Response{}, errors.New("whispercpp: input is not a call_transcribe input")
	}
	url, lang := p.URL, in.Language
	if in.Language == "ky" {
		if p.KyURL == "" {
			return modelgw.Response{}, errors.New("whispercpp: no Kyrgyz speech model is configured (AI_WHISPER_KY_URL); Whisper itself has no Kyrgyz")
		}
		url, lang = p.KyURL, p.KyLanguage
		if lang == "" {
			lang = "auto"
		}
	}
	if url == "" {
		return modelgw.Response{}, errors.New("whispercpp: no speech server configured")
	}
	if p.Audio == nil {
		return modelgw.Response{}, errors.New("whispercpp: no audio store")
	}
	audio, found, err := p.Audio.GetAudio(ctx, req.TenantID, in.AudioRef)
	if err != nil {
		return modelgw.Response{}, modelgw.Transient(fmt.Errorf("whispercpp: read audio: %w", err))
	}
	if !found {
		return modelgw.Response{}, errors.New("whispercpp: audio not found (never uploaded, deleted or expired)")
	}

	body, contentType, err := form(audio, lang, in.Channels != "mono")
	if err != nil {
		return modelgw.Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(url, "/")+"/inference", body)
	if err != nil {
		return modelgw.Response{}, err
	}
	httpReq.Header.Set("Content-Type", contentType)
	client := p.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	started := time.Now()
	resp, err := client.Do(httpReq)
	if err != nil {
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) || errors.Is(err, io.EOF) {
			return modelgw.Response{}, modelgw.Transient(fmt.Errorf("whispercpp: %w", err))
		}
		return modelgw.Response{}, modelgw.Transient(fmt.Errorf("whispercpp: request failed: %w", err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	switch {
	case resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return modelgw.Response{}, modelgw.Transient(fmt.Errorf("whispercpp: server returned %d", resp.StatusCode))
	case resp.StatusCode >= 400:
		return modelgw.Response{}, fmt.Errorf("whispercpp: server rejected the audio (%d): unsupported format?", resp.StatusCode)
	}
	segments, duration, err := parse(raw, in.Channels)
	if err != nil {
		return modelgw.Response{}, err
	}
	if p.Log != nil {
		p.Log.Info("speech recognised", "segments", len(segments), "audio_seconds", duration,
			"duration_ms", time.Since(started).Milliseconds())
	}
	return modelgw.Response{Output: output(in.Language, duration, segments, p.Model), Provider: Name, Model: p.Model}, nil
}

// form builds the multipart request: the recording (converted by the server's
// ffmpeg, so mp3/ogg/wav all work), the language, timed JSON output and, for a
// stereo recording, channel-based speaker separation.
func form(a Audio, lang string, diarize bool) (*bytes.Buffer, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", "recording"+extension(a.ContentType))
	if err != nil {
		return nil, "", err
	}
	if _, err := fw.Write(a.Bytes); err != nil {
		return nil, "", err
	}
	fields := map[string]string{"language": lang, "response_format": "verbose_json", "temperature": "0.0"}
	if diarize {
		fields["diarize"] = "true"
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return &buf, w.FormDataContentType(), nil
}

func extension(contentType string) string {
	switch contentType {
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/ogg", "audio/opus":
		return ".ogg"
	case "audio/mp4", "audio/m4a", "audio/x-m4a", "audio/aac":
		return ".m4a"
	case "audio/flac", "audio/x-flac":
		return ".flac"
	case "audio/webm":
		return ".webm"
	default:
		return ".wav"
	}
}

// Whisper writes non-speech as bracketed tags: "[BLANK_AUDIO]", "(музыка)".
var nonSpeech = regexp.MustCompile(`^\s*[\[(][^\])]*[\])]\s*$`)

// parse reads whisper-server's verbose_json. Speaker labels come from its
// stereo diarization ("0" = left channel, "1" = right, "?" = unclear).
func parse(raw []byte, channels string) ([]Segment, float64, error) {
	var r struct {
		Duration float64 `json:"duration"`
		Segments []struct {
			Start   float64         `json:"start"`
			End     float64         `json:"end"`
			Text    string          `json:"text"`
			Speaker json.RawMessage `json:"speaker"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, 0, modelgw.Transient(errors.New("whispercpp: unreadable response"))
	}
	out := make([]Segment, 0, len(r.Segments))
	for _, s := range r.Segments {
		text := strings.Join(strings.Fields(s.Text), " ")
		if text == "" || nonSpeech.MatchString(text) {
			continue
		}
		if utf8.RuneCountInString(text) > maxSegmentRunes {
			text = string([]rune(text)[:maxSegmentRunes])
		}
		out = append(out, Segment{Start: s.Start, End: max(s.End, s.Start), Speaker: speaker(s.Speaker, channels), Text: text})
	}
	if len(out) > maxSegments {
		return nil, 0, errors.New("whispercpp: recording too long (more than 5000 segments)")
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	duration := r.Duration
	for _, s := range out {
		duration = max(duration, s.End)
	}
	return out, duration, nil
}

func speaker(raw json.RawMessage, channels string) string {
	var label string
	if err := json.Unmarshal(raw, &label); err != nil {
		var n int
		if json.Unmarshal(raw, &n) != nil {
			return "unknown"
		}
		label = fmt.Sprint(n)
	}
	left, right := "manager", "customer"
	switch channels {
	case "manager_left":
	case "manager_right":
		left, right = right, left
	default:
		return "unknown"
	}
	switch strings.TrimSpace(label) {
	case "0":
		return left
	case "1":
		return right
	default:
		return "unknown"
	}
}

func output(language string, duration float64, segments []Segment, model string) json.RawMessage {
	texts := make([]string, len(segments))
	for i, s := range segments {
		texts[i] = s.Text
	}
	if model == "" {
		model = "whisper"
	}
	b, _ := json.Marshal(map[string]any{
		"schema":            "call_transcript.output.v1",
		"language":          language,
		"duration_s":        duration,
		"segments":          segments,
		"text":              strings.Join(texts, " "),
		"insufficient_data": len(segments) == 0,
		"generator":         map[string]any{"provider": Name, "model": model, "is_mock": false},
	})
	return b
}
