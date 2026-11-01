package embedding_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"crmai/internal/embedding"
)

func TestPrefixesBatchesAndNormalises(t *testing.T) {
	var inputs [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		inputs = append(inputs, body.Input)
		var data []map[string]any
		// Out of order on purpose: the client must place vectors by index.
		for i := len(body.Input) - 1; i >= 0; i-- {
			data = append(data, map[string]any{"index": i, "embedding": []float32{3, 4, float32(i)}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()
	c := embedding.Client{URL: srv.URL}

	texts := make([]string, 20)
	for i := range texts {
		texts[i] = "абзац"
	}
	vs, err := c.Passages(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 20 || len(inputs) != 2 || len(inputs[0]) != 16 || len(inputs[1]) != 4 {
		t.Fatalf("20 passages go in batches of 16: %d vectors, batches %d", len(vs), len(inputs))
	}
	if !strings.HasPrefix(inputs[0][0], "passage: ") {
		t.Fatalf("e5 needs the passage prefix: %q", inputs[0][0])
	}
	for _, v := range vs {
		if n := math.Sqrt(float64(embedding.Dot(v, v))); math.Abs(n-1) > 1e-5 {
			t.Fatalf("vectors must be normalised, |v| = %f", n)
		}
	}
	if vs[1][2] == vs[2][2] {
		t.Fatal("vectors must be placed by their index")
	}
	if _, err := c.Query(context.Background(), "вопрос"); err != nil || !strings.HasPrefix(inputs[2][0], "query: ") {
		t.Fatalf("e5 needs the query prefix: %v %v", err, inputs[2])
	}
	if embedding.Dot([]float32{1}, []float32{1, 0}) != 0 {
		t.Fatal("vectors of different length are not similar")
	}
}

func TestServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if _, err := (embedding.Client{URL: srv.URL}).Query(context.Background(), "x"); err == nil {
		t.Fatal("a server error must be reported")
	}
}
