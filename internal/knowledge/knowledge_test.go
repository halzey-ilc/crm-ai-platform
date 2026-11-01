package knowledge_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"crmai/internal/knowledge"
)

func TestChunkKeepsVerbatimSpansWithinLimit(t *testing.T) {
	long := strings.Repeat("Клиент должен оплатить счёт в течение пяти дней. ", 40)
	text := "Короткий абзац.\n\n" + long + "\n\nПоследний абзац без точки"
	chunks := knowledge.Chunk(text)
	if len(chunks) < 3 {
		t.Fatalf("a long paragraph must be split: %d chunks", len(chunks))
	}
	for _, c := range chunks {
		if utf8.RuneCountInString(c) > knowledge.MaxChunkRunes {
			t.Fatalf("chunk longer than the limit: %d", utf8.RuneCountInString(c))
		}
		for _, line := range strings.Split(c, "\n") {
			if !strings.Contains(text, line) {
				t.Fatalf("chunk text must come from the document: %q", line)
			}
		}
	}
	if knowledge.Chunk("  \n\n ") != nil {
		t.Fatal("blank text has no chunks")
	}
}

func TestQueryUsesPrefixesAndIgnoresOperators(t *testing.T) {
	q := knowledge.Query("Какая скидка для новых клиентов? & | !(")
	// "скидка" -> "скидк" also matches "скидку", "скидки".
	for _, want := range []string{"какая:*", "скидк:*", "для:*", "новых:*", "клиен:*"} {
		if !strings.Contains(q, want) {
			t.Fatalf("missing %s in %q", want, q)
		}
	}
	if strings.ContainsAny(strings.ReplaceAll(strings.ReplaceAll(q, " | ", ""), ":*", ""), "&!()|") {
		t.Fatalf("operators from the question must not reach the query: %q", q)
	}
	if knowledge.Query("а и?") != "" {
		t.Fatal("no searchable word, no query")
	}
}

func TestTags(t *testing.T) {
	ok := []string{"all", "role:sales_rep", "dept:3f2a-01", "user:42"}
	if !knowledge.ValidTags(ok) {
		t.Fatal("valid tags rejected")
	}
	for _, bad := range [][]string{nil, {"admin"}, {"role:Manager"}, {"user:"}, {"dept:a b"}} {
		if knowledge.ValidTags(bad) {
			t.Fatalf("%v must be rejected", bad)
		}
	}
}

func TestFuseRewardsAgreementAndLimits(t *testing.T) {
	p := func(ref string) knowledge.Passage { return knowledge.Passage{Ref: ref} }
	text := []knowledge.Passage{p("a#0"), p("b#0"), p("c#0")}
	semantic := []knowledge.Passage{p("d#0"), p("b#0")}
	got := knowledge.Fuse(3, text, semantic)
	var refs []string
	for _, x := range got {
		refs = append(refs, x.Ref)
	}
	// b is in both lists and comes first; a and d tie (rank 1 each), a was seen first.
	if strings.Join(refs, ",") != "b#0,a#0,d#0" {
		t.Fatalf("fused order: %v", refs)
	}
	if len(knowledge.Fuse(8, nil, nil)) != 0 {
		t.Fatal("nothing in, nothing out")
	}
}
