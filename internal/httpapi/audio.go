package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"

	"crmai/internal/knowledge"
	"crmai/internal/store/postgres"
)

// AudioStore keeps call recordings until they are transcribed.
type AudioStore interface {
	PutAudio(ctx context.Context, tenantID, ref, contentType, createdBy string, data []byte) (string, bool, error)
	DeleteAudio(ctx context.Context, tenantID, ref string) error
}

var audioType = regexp.MustCompile(`^audio/[a-z0-9.+-]{1,40}$`)

// PUT /v1/audio/{ref}: the raw recording as the body, with an audio/* Content-Type
// (wav, mp3, ogg, m4a, flac, webm). Then create a call.transcribe.v1 job with
// this ref. Recordings are deleted automatically after a few hours.
func (h *handlers) putAudio(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	ref := r.PathValue("ref")
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch {
	case !knowledge.ValidRef(ref):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "audio ref must be 1-64 chars of [A-Za-z0-9._:-]")
		return
	case !audioType.MatchString(ct):
		writeError(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be audio/* (e.g. audio/wav, audio/mpeg)")
		return
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		h.badBody(w, r, err, "could not read the recording")
		return
	}
	if len(data) == 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "the recording is empty")
		return
	}
	sum, changed, err := h.d.Audio.PutAudio(r.Context(), p.Tenant, ref, ct, p.Subject, data)
	if err != nil {
		h.d.Log.Error("put audio failed", "err", err, "correlation_id", CorrelationID(r.Context()))
		writeError(w, r, http.StatusInternalServerError, "internal_error", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": "ai.audio.v1", "ref": ref, "bytes": len(data), "sha256": sum, "changed": changed})
}

// DELETE /v1/audio/{ref}: withdraw a recording before it expires.
func (h *handlers) deleteAudio(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	ref := r.PathValue("ref")
	if !knowledge.ValidRef(ref) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "audio ref must be 1-64 chars of [A-Za-z0-9._:-]")
		return
	}
	switch err := h.d.Audio.DeleteAudio(r.Context(), p.Tenant, ref); {
	case errors.Is(err, postgres.ErrAudioNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "no such recording")
	case err != nil:
		h.d.Log.Error("delete audio failed", "err", err, "correlation_id", CorrelationID(r.Context()))
		writeError(w, r, http.StatusInternalServerError, "internal_error", "internal error")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
