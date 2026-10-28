// Package llamacpp is a Model Gateway provider backed by a local llama.cpp
// server (OpenAI-compatible /v1/chat/completions). It runs open-weight models
// on our own hardware: no data leaves the deployment.
//
// Every task asks the model for JSON constrained by a JSON Schema (llama.cpp
// turns it into a sampling grammar), then this package assembles the public
// output contract and adds provenance. Grounding is enforced twice: here
// (ungrounded items are dropped, finding refs are restricted to the input by
// the grammar) and again by the job type's Ground check before storage.
package llamacpp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"crmai/internal/modelgw"
)

const (
	Name = "llamacpp"

	TaskCallSummary      = "call_summary"
	TaskOperationsDigest = "operations_digest"

	// Transcripts longer than this do not fit the context window we run with
	// (8k tokens) together with the prompt and the answer.
	maxTranscriptRunes = 12000
)

// Provider calls a llama.cpp server.
type Provider struct {
	BaseURL string // e.g. http://llm:8080
	Model   string // label recorded as the result's model (the GGUF in use)
	HTTP    *http.Client
	Log     *slog.Logger
}

func (Provider) Name() string { return Name }

func (Provider) Supports(task string) bool {
	return task == TaskCallSummary || task == TaskOperationsDigest
}

func (p Provider) Invoke(ctx context.Context, req modelgw.Request) (modelgw.Response, error) {
	var out json.RawMessage
	var err error
	switch req.Task {
	case TaskCallSummary:
		out, err = p.callSummary(ctx, req.Input)
	case TaskOperationsDigest:
		out, err = p.operationsDigest(ctx, req.Input)
	default:
		return modelgw.Response{}, errors.New("llamacpp: unsupported task")
	}
	if err != nil {
		return modelgw.Response{}, err
	}
	return modelgw.Response{Output: out, Provider: Name, Model: p.Model}, nil
}

type generator struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	IsMock   bool   `json:"is_mock"`
}

func (p Provider) generator() generator {
	return generator{Provider: Name, Model: p.Model, IsMock: false}
}

// --- chat completion call ------------------------------------------------------

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Messages           []chatMessage  `json:"messages"`
	Temperature        float64        `json:"temperature"`
	TopP               float64        `json:"top_p"`
	MaxTokens          int            `json:"max_tokens"`
	ResponseFormat     map[string]any `json:"response_format"`
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// complete sends one chat request whose answer must match schema and decodes
// the answer into dst. Network failures, overload and unparsable answers are
// transient (the next attempt samples again); a rejected request is permanent.
func (p Provider) complete(ctx context.Context, system, user string, schema map[string]any, maxTokens int, dst any) error {
	body, err := json.Marshal(chatRequest{
		Messages:       []chatMessage{{Role: "system", Content: system}, {Role: "user", Content: user}},
		Temperature:    0.2,
		TopP:           0.9,
		MaxTokens:      maxTokens,
		ResponseFormat: map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "answer", "schema": schema}},
		// Qwen3.5 thinks by default; we want the answer only.
		ChatTemplateKwargs: map[string]any{"enable_thinking": false},
	})
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := p.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	started := time.Now()
	resp, err := client.Do(httpReq)
	if err != nil {
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) || errors.Is(err, io.EOF) {
			return modelgw.Transient(fmt.Errorf("llamacpp: %w", err))
		}
		return modelgw.Transient(fmt.Errorf("llamacpp: request failed: %w", err))
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	switch {
	case resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return modelgw.Transient(fmt.Errorf("llamacpp: server returned %d", resp.StatusCode))
	case resp.StatusCode >= 400:
		return fmt.Errorf("llamacpp: server rejected the request (%d)", resp.StatusCode)
	}
	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil || len(cr.Choices) == 0 {
		return modelgw.Transient(errors.New("llamacpp: unreadable completion"))
	}
	if p.Log != nil {
		p.Log.Info("llm completion", "prompt_tokens", cr.Usage.PromptTokens,
			"completion_tokens", cr.Usage.CompletionTokens, "finish_reason", cr.Choices[0].FinishReason,
			"duration_ms", time.Since(started).Milliseconds())
	}
	if cr.Choices[0].FinishReason == "length" {
		return modelgw.Transient(errors.New("llamacpp: answer cut off at max_tokens"))
	}
	if err := json.Unmarshal([]byte(cr.Choices[0].Message.Content), dst); err != nil {
		return modelgw.Transient(errors.New("llamacpp: answer is not the requested JSON"))
	}
	return nil
}

func languageName(code string) string {
	switch code {
	case "ru":
		return "Russian"
	case "ky":
		return "Kyrgyz"
	case "en":
		return "English"
	default:
		return "the same language as the input text"
	}
}
