package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"crmai/internal/jobs"
	"crmai/internal/knowledge"
	"crmai/internal/store/postgres"
)

// KnowledgeStore keeps documents and searches them with an access filter.
type KnowledgeStore interface {
	PutDocument(ctx context.Context, tenantID string, d postgres.Document) (int, bool, error)
	DeleteDocument(ctx context.Context, tenantID, ref string) error
	Search(ctx context.Context, tenantID string, tags []string, query string, limit int) ([]knowledge.Passage, error)
}

var languages = map[string]bool{"ru": true, "ky": true, "en": true}

func decodeStrict(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func (h *handlers) badBody(w http.ResponseWriter, r *http.Request, err error, what string) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "request body too large")
		return
	}
	writeError(w, r, http.StatusBadRequest, "invalid_request", what)
}

type putDocumentBody struct {
	Title      string   `json:"title"`
	Language   string   `json:"language"`
	AccessTags []string `json:"access_tags"`
	Text       string   `json:"text"`
}

// PUT /v1/knowledge/documents/{ref}: create or replace a document.
func (h *handlers) putDocument(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	ref := r.PathValue("ref")
	var body putDocumentBody
	if err := decodeStrict(r, &body); err != nil {
		h.badBody(w, r, err, "body must be {title, language, access_tags, text}")
		return
	}
	title := strings.TrimSpace(body.Title)
	switch {
	case !knowledge.ValidRef(ref):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "document ref must be 1-64 chars of [A-Za-z0-9._:-]")
	case title == "" || utf8.RuneCountInString(title) > 300:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "title must be 1-300 characters")
	case !languages[body.Language]:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "language must be ru, ky or en")
	case !knowledge.ValidTags(body.AccessTags):
		writeError(w, r, http.StatusBadRequest, "invalid_request",
			"access_tags must be 1-50 of: all, role:<name>, dept:<id>, user:<id>")
	case strings.TrimSpace(body.Text) == "" || utf8.RuneCountInString(body.Text) > knowledge.MaxDocumentRunes:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "text must be 1-200000 characters")
	default:
		n, changed, err := h.d.Knowledge.PutDocument(r.Context(), p.Tenant, postgres.Document{
			Ref: ref, Title: title, Language: body.Language, Tags: body.AccessTags, Text: body.Text, CreatedBy: p.Subject,
		})
		if err != nil {
			h.d.Log.Error("put document failed", "err", err, "correlation_id", CorrelationID(r.Context()))
			writeError(w, r, http.StatusInternalServerError, "internal_error", "internal error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"schema": "ai.knowledge_document.v1", "ref": ref, "passages": n, "changed": changed})
	}
}

// DELETE /v1/knowledge/documents/{ref}
func (h *handlers) deleteDocument(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	err := h.d.Knowledge.DeleteDocument(r.Context(), p.Tenant, r.PathValue("ref"))
	switch {
	case errors.Is(err, postgres.ErrDocumentNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "document not found")
	case err != nil:
		h.d.Log.Error("delete document failed", "err", err, "correlation_id", CorrelationID(r.Context()))
		writeError(w, r, http.StatusInternalServerError, "internal_error", "internal error")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

type askBody struct {
	Question   string   `json:"question"`
	Language   string   `json:"language"`
	AccessTags []string `json:"access_tags"`
}

// POST /v1/knowledge/ask: search the documents the asker may see and queue an
// answer job (knowledge.answer.v1) on the passages found. Poll GET /v1/jobs/{id}.
func (h *handlers) ask(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	key := r.Header.Get("Idempotency-Key")
	if !jobs.ValidIdempotencyKey(key) {
		writeError(w, r, http.StatusBadRequest, "idempotency_key_required",
			"Idempotency-Key header is required (8-128 chars of [A-Za-z0-9._:-])")
		return
	}
	var body askBody
	if err := decodeStrict(r, &body); err != nil {
		h.badBody(w, r, err, "body must be {question, language, access_tags}")
		return
	}
	question := strings.TrimSpace(body.Question)
	switch {
	case question == "" || utf8.RuneCountInString(question) > 1000:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "question must be 1-1000 characters")
		return
	case !languages[body.Language]:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "language must be ru, ky or en")
		return
	case !knowledge.ValidTags(body.AccessTags):
		writeError(w, r, http.StatusBadRequest, "invalid_request",
			"access_tags must be 1-50 of: all, role:<name>, dept:<id>, user:<id>")
		return
	}
	passages, err := h.d.Knowledge.Search(r.Context(), p.Tenant, body.AccessTags, knowledge.Query(question), knowledge.MaxPassages)
	if err != nil {
		h.jobError(w, r, err)
		return
	}
	if passages == nil {
		passages = []knowledge.Passage{}
	}
	input, _ := json.Marshal(knowledge.Input{Language: body.Language, Question: question, Passages: passages})
	job, replayed, err := h.d.Jobs.Create(r.Context(), jobs.CreateRequest{
		TenantID: p.Tenant, CreatedBy: p.Subject, Type: "knowledge.answer.v1", Input: input,
		IdempotencyKey: key, CorrelationID: CorrelationID(r.Context()),
	})
	if err != nil {
		h.jobError(w, r, err)
		return
	}
	status := http.StatusAccepted
	if replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("Location", "/v1/jobs/"+job.ID)
	writeJSON(w, status, jobView(job, nil))
}
