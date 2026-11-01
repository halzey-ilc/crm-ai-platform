// Package embedding turns text into vectors for semantic search, through a
// llama.cpp server started with --embeddings (OpenAI-compatible /v1/embeddings).
//
// The model is multilingual-e5-small (MIT): XLM-R based, so Russian, Kyrgyz and
// English share one vector space. e5 expects "query: " and "passage: " prefixes;
// without them retrieval quality drops. Vectors are L2-normalised, so cosine
// similarity is a dot product.
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
)

// Client calls the embeddings server.
type Client struct {
	URL   string
	Model string // label sent as "model"; llama.cpp serves one model and ignores it
	HTTP  *http.Client
}

const batch = 16

// Query embeds a search question.
func (c Client) Query(ctx context.Context, text string) ([]float32, error) {
	v, err := c.embed(ctx, []string{"query: " + text})
	if err != nil {
		return nil, err
	}
	return v[0], nil
}

// Passages embeds document passages, in order.
func (c Client) Passages(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for i := 0; i < len(texts); i += batch {
		end := min(i+batch, len(texts))
		in := make([]string, 0, end-i)
		for _, t := range texts[i:end] {
			in = append(in, "passage: "+t)
		}
		v, err := c.embed(ctx, in)
		if err != nil {
			return nil, err
		}
		out = append(out, v...)
	}
	return out, nil
}

func (c Client) embed(ctx context.Context, input []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"input": input, "model": c.Model})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedding: server returned %d", resp.StatusCode)
	}
	var r struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || len(r.Data) != len(input) {
		return nil, errors.New("embedding: unreadable response")
	}
	out := make([][]float32, len(input))
	for _, d := range r.Data {
		if d.Index < 0 || d.Index >= len(out) || len(d.Embedding) == 0 {
			return nil, errors.New("embedding: bad index or empty vector")
		}
		out[d.Index] = normalise(d.Embedding)
	}
	return out, nil
}

func normalise(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	n := float32(math.Sqrt(sum))
	if n == 0 {
		return v
	}
	for i := range v {
		v[i] /= n
	}
	return v
}

// Dot is the cosine similarity of two normalised vectors; 0 if lengths differ.
func Dot(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
