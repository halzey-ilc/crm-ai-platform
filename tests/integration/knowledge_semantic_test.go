//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"crmai/internal/knowledge"
	"crmai/internal/store/postgres"
)

// vectors: a fixed 3-dimensional "embedding" per passage, normalised.
func TestSemanticSearchKeepsTheAccessFilter(t *testing.T) {
	ctx := context.Background()
	tenant := newID("vec")
	// Each paragraph is long enough (over half of MaxChunkRunes) to be its own passage.
	text := strings.Repeat("Возврат товара возможен в течение 14 дней. ", 12) + "\n\n" +
		strings.Repeat("Отпуск согласуется за две недели. ", 15)
	if n := len(knowledge.Chunk(text)); n != 2 {
		t.Fatalf("expected 2 passages, got %d", n)
	}
	put := func(ref string, tags []string, v [][]float32) {
		if _, _, err := store.PutDocument(ctx, tenant, postgres.Document{Ref: ref, Title: ref, Language: "ru",
			Tags: tags, Text: text, CreatedBy: "user:it", Embeddings: v}); err != nil {
			t.Fatal(err)
		}
	}
	put("sales", []string{"role:sales_rep"}, [][]float32{{1, 0, 0}, {0, 1, 0}})
	put("hr", []string{"role:hr"}, [][]float32{{1, 0, 0}, {0, 1, 0}})
	put("plain", []string{"role:sales_rep"}, nil) // no vectors: only full-text search finds it

	got, err := store.SearchVector(ctx, tenant, []string{"role:sales_rep"}, []float32{0.9, 0.1, 0}, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Ref != "sales#0" || got[1].Ref != "sales#1" {
		t.Fatalf("a sales rep must get the sales passages, closest first, never HR's: %+v", got)
	}
	// Re-sending the same text adds vectors to a document stored without them.
	put("plain", []string{"role:sales_rep"}, [][]float32{{0, 0, 1}, {0, 1, 0}})
	got, _ = store.SearchVector(ctx, tenant, []string{"role:sales_rep"}, []float32{0, 0, 1}, 1)
	if len(got) != 1 || got[0].Ref != "plain#0" {
		t.Fatalf("vectors must be filled in for unchanged text: %+v", got)
	}
}
