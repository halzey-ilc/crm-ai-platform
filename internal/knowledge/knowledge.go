// Package knowledge is the document side of the Knowledge Assistant: splitting
// documents into passages, access tags and search queries.
//
// Access control happens before search: every document carries access tags
// chosen by the CRM (e.g. "all", "role:manager", "dept:<id>", "user:<id>"), and
// a question is asked with the tags of the person asking. Only documents that
// share at least one tag are searched, so the model never sees a passage the
// person could not open in the CRM.
//
// Search is PostgreSQL full-text search with the "simple" configuration and
// prefix matching as a crude stemmer for Russian and Kyrgyz. Semantic search
// (embeddings) needs a GPU and comes later.
package knowledge

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits for documents and passages.
const (
	MaxDocumentRunes = 200_000
	MaxChunkRunes    = 800
	MaxTags          = 50
	MaxPassages      = 8
)

// Passage is one searchable piece of a document.
type Passage struct {
	Ref   string `json:"ref"` // "<document ref>#<n>"
	Title string `json:"title"`
	Text  string `json:"text"`
}

// Input is knowledge_answer.input.v1.
type Input struct {
	Language string    `json:"language"`
	Question string    `json:"question"`
	Passages []Passage `json:"passages"`
}

// Statement is one cited sentence of an answer.
type Statement struct {
	Text       string `json:"text"`
	PassageRef string `json:"passage_ref"`
	Quote      string `json:"quote"`
}

// Passage returns the input passage with this ref.
func (in Input) Passage(ref string) (Passage, bool) {
	for _, p := range in.Passages {
		if p.Ref == ref {
			return p, true
		}
	}
	return Passage{}, false
}

// Check returns why a statement is not grounded in the input, or "".
func (in Input) Check(s Statement) string {
	p, ok := in.Passage(s.PassageRef)
	switch {
	case !ok:
		return "cites a passage that is not in the input"
	case !strings.Contains(p.Text, s.Quote):
		return "quote does not occur in the cited passage"
	case strings.TrimSpace(s.Text) == "":
		return "has no text"
	}
	return ""
}

// NotFound is the fixed reply when nothing relevant was found.
var NotFound = map[string]string{
	"ru": "В доступных вам документах ответа не найдено.",
	"ky": "Сизге жеткиликтүү документтерден жооп табылган жок.",
	"en": "No answer was found in the documents available to you.",
}

// Stems returns the search stems of a question (Query without the operators).
func Stems(question string) []string {
	q := Query(question)
	if q == "" {
		return nil
	}
	var out []string
	for _, t := range strings.Split(q, " | ") {
		out = append(out, strings.TrimSuffix(t, ":*"))
	}
	return out
}

var (
	tagRE = regexp.MustCompile(`^(all|role:[a-z_]{1,32}|dept:[A-Za-z0-9-]{1,64}|user:[A-Za-z0-9-]{1,64})$`)
	refRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
)

// ValidTag reports whether s is an access tag.
func ValidTag(s string) bool { return tagRE.MatchString(s) }

// ValidRef reports whether s can identify a document.
func ValidRef(s string) bool { return refRE.MatchString(s) }

// ValidTags checks a tag list (non-empty, bounded, all valid).
func ValidTags(tags []string) bool {
	if len(tags) == 0 || len(tags) > MaxTags {
		return false
	}
	for _, t := range tags {
		if !ValidTag(t) {
			return false
		}
	}
	return true
}

// Chunk splits text into passages of at most MaxChunkRunes, on paragraph and
// then sentence boundaries. Every passage is a verbatim span of the text.
func Chunk(text string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		for _, piece := range pieces(para) {
			if cur.Len() > 0 && utf8.RuneCountInString(cur.String())+1+utf8.RuneCountInString(piece) > MaxChunkRunes {
				flush()
			}
			if cur.Len() > 0 {
				cur.WriteString("\n")
			}
			cur.WriteString(piece)
		}
		// Paragraphs stay together when they fit; a new one starts a new line.
	}
	flush()
	return out
}

// pieces returns the paragraph itself if it fits, else its sentences, else
// hard cuts of MaxChunkRunes.
func pieces(para string) []string {
	if utf8.RuneCountInString(para) <= MaxChunkRunes {
		return []string{para}
	}
	var out []string
	start := 0
	for i, r := range para {
		if r == '.' || r == '!' || r == '?' {
			end := i + utf8.RuneLen(r)
			out = append(out, hardCut(strings.TrimSpace(para[start:end]))...)
			start = end
		}
	}
	if rest := strings.TrimSpace(para[start:]); rest != "" {
		out = append(out, hardCut(rest)...)
	}
	return out
}

func hardCut(s string) []string {
	var out []string
	runes := []rune(s)
	for len(runes) > MaxChunkRunes {
		out = append(out, string(runes[:MaxChunkRunes]))
		runes = runes[MaxChunkRunes:]
	}
	if len(runes) > 0 {
		out = append(out, string(runes))
	}
	return out
}

// Query turns a question into a to_tsquery expression: words of 3+ letters,
// cut to a prefix (a crude stemmer for inflected languages), OR-ed together.
// Returns "" when the question has no searchable word.
func Query(question string) string {
	seen := map[string]bool{}
	var terms []string
	for _, w := range strings.FieldsFunc(strings.ToLower(question), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		runes := []rune(w)
		if len(runes) < 3 {
			continue
		}
		if len(runes) > 5 {
			runes = runes[:max(5, len(runes)-3)] // "договора" -> "догов", "оплаты" -> "оплат"
		}
		term := string(runes)
		if !seen[term] {
			seen[term] = true
			terms = append(terms, term+":*")
		}
		if len(terms) == 20 {
			break
		}
	}
	return strings.Join(terms, " | ")
}
