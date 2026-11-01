package httpapi_test

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
	"time"

	"crmai/internal/httpapi"
	"crmai/internal/metrics"
)

func TestJobEventsStreamUntilTheJobEnds(t *testing.T) {
	httpapi.SetEventTiming(20*time.Millisecond, 50*time.Millisecond)
	srv := newServer(t)
	// As in the gateway binary: the metrics wrapper must not hide Flush.
	srv.Config.Handler = metrics.HTTP(srv.Config.Handler)
	tok := token(t, "t1")
	code, body, _ := do(t, srv, call{method: http.MethodPost, path: "/v1/jobs", tok: tok, body: validJob,
		headers: map[string]string{"Idempotency-Key": "k-events-1"}})
	if code != http.StatusAccepted {
		t.Fatalf("create: %d %v", code, body)
	}
	id := body["id"].(string)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/jobs/"+id+"/events", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	next := func() string {
		select {
		case l, ok := <-lines:
			if !ok {
				return "<eof>"
			}
			return l
		case <-time.After(5 * time.Second):
			t.Fatal("no event within 5 s")
			return ""
		}
	}
	if l := next(); l != "event: status" {
		t.Fatalf("first event: %q", l)
	}
	if l := next(); !strings.Contains(l, `"status":"queued"`) {
		t.Fatalf("queued status: %q", l)
	}

	// Cancelling the queued job ends it; the stream reports it and closes.
	do(t, srv, call{method: http.MethodPost, path: "/v1/jobs/" + id + "/cancel", tok: tok})
	var all []string
	for l := next(); l != "<eof>"; l = next() {
		all = append(all, l)
	}
	text := strings.Join(all, "\n")
	if !strings.Contains(text, `"status":"cancelled"`) || !strings.Contains(text, "event: job") ||
		!strings.Contains(text, `"id":"`+id+`"`) {
		t.Fatalf("the stream must report the end and the final job, then close:\n%s", text)
	}

	// Another tenant's job is a 404 before any streaming starts.
	code, _, _ = do(t, srv, call{method: http.MethodGet, path: "/v1/jobs/" + id + "/events", tok: token(t, "t2")})
	if code != http.StatusNotFound {
		t.Fatalf("another tenant must get 404, got %d", code)
	}
}
