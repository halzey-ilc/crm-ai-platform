package httpapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"crmai/internal/auth"
	"crmai/internal/httpapi"
	"crmai/internal/jobs"
	"crmai/internal/store/postgres"
	"crmai/internal/testutil"
)

type memAudio map[string]string

func (m memAudio) PutAudio(_ context.Context, tenant, ref, contentType, _ string, data []byte) (string, bool, error) {
	m[tenant+"/"+ref] = contentType + ":" + string(data)
	return strings.Repeat("a", 64), true, nil
}

func (m memAudio) DeleteAudio(_ context.Context, tenant, ref string) error {
	if _, ok := m[tenant+"/"+ref]; !ok {
		return postgres.ErrAudioNotFound
	}
	delete(m, tenant+"/"+ref)
	return nil
}

func TestAudioUpload(t *testing.T) {
	types := testutil.Registry(t)
	v, _ := auth.NewVerifier([][]byte{signingKey}, "crm-platform", "crm-ai-platform", 10*time.Minute)
	store := memAudio{}
	srv := httptest.NewServer(httpapi.NewHandler(httpapi.Deps{
		Jobs: jobs.NewService(testutil.NewMemoryRepo(), types), Types: types, Verifier: v, Audio: store,
		Ready: func(context.Context) error { return nil },
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)), RequestTimeout: 5 * time.Second,
		// The JSON limit is tiny; recordings have their own, larger one.
		MaxBodyBytes: 64, MaxAudioBytes: 1024, RateLimitRPS: 1000, RateLimitBurst: 1000,
	}))
	defer srv.Close()
	writer := token(t, "t1", auth.ScopeAudioWrite)
	put := func(tok, ref, ct, body string) int {
		code, _, _ := do(t, srv, call{method: http.MethodPut, path: "/v1/audio/" + ref, tok: tok, body: body,
			headers: map[string]string{"Content-Type": ct}})
		return code
	}
	recording := strings.Repeat("x", 500) // above the JSON limit, below the audio limit

	if code := put(writer, "call-1", "audio/mpeg", recording); code != http.StatusOK || store["t1/call-1"] != "audio/mpeg:"+recording {
		t.Fatalf("a recording must be stored for the token's tenant: %d %v", code, len(store))
	}
	if code := put(token(t, "t1"), "call-2", "audio/mpeg", "x"); code != http.StatusForbidden {
		t.Fatalf("uploading needs ai.audio.write, got %d", code)
	}
	if code := put(writer, "call-3", "application/json", `{}`); code != http.StatusUnsupportedMediaType {
		t.Fatalf("only audio/* is accepted, got %d", code)
	}
	if code := put(writer, "call-4", "audio/wav", strings.Repeat("x", 2000)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("the audio limit applies, got %d", code)
	}
	if code := put(writer, "bad%20ref", "audio/wav", "x"); code != http.StatusBadRequest {
		t.Fatalf("refs are validated, got %d", code)
	}
	code, _, _ := do(t, srv, call{method: http.MethodDelete, path: "/v1/audio/call-1", tok: writer})
	if code != http.StatusNoContent || len(store) != 0 {
		t.Fatalf("a recording can be withdrawn: %d", code)
	}
	code, _, _ = do(t, srv, call{method: http.MethodDelete, path: "/v1/audio/call-1", tok: writer})
	if code != http.StatusNotFound {
		t.Fatalf("deleting twice is a 404, got %d", code)
	}
	// Other paths keep the small JSON limit.
	job := `{"type":"call.summary.v1","input":{"language":"ru","transcript":"` + recording + `"}}`
	code, body, _ := do(t, srv, call{method: http.MethodPost, path: "/v1/jobs", tok: token(t, "t1"), body: job,
		headers: map[string]string{"Idempotency-Key": "k-12345678"}})
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("the JSON limit must still apply elsewhere: %d %v", code, body)
	}
}
