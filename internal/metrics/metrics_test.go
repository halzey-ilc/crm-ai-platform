package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"crmai/internal/metrics"
)

func TestExpositionFormat(t *testing.T) {
	r := metrics.NewRegistry()
	c := r.NewCounter("t_requests_total", "Requests.", "route")
	h := r.NewHistogram("t_seconds", "Latency.", []float64{0.1, 1}, "route")
	g := r.NewGauge("t_queue", "Queue.")
	c.Inc(`/a"b`)
	c.Add(2, `/a"b`)
	h.Observe(0.05, "/x")
	h.Observe(0.5, "/x")
	h.Observe(5, "/x")
	g.Set(7)

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	out := rec.Body.String()
	for _, want := range []string{
		"# TYPE t_requests_total counter",
		`t_requests_total{route="/a\"b"} 3`,
		`t_seconds_bucket{route="/x",le="0.1"} 1`,
		`t_seconds_bucket{route="/x",le="1"} 2`,
		`t_seconds_bucket{route="/x",le="+Inf"} 3`,
		`t_seconds_count{route="/x"} 3`,
		"t_queue 7",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Fatal("wrong content type")
	}
}

func TestRoutesHaveNoIdsOrArbitraryPaths(t *testing.T) {
	cases := map[string]string{
		"/v1/jobs/7b0c2f6e-1d2a-4f3b-9c8d-0e1f2a3b4c5d":        "/v1/jobs/:id",
		"/v1/jobs/7b0c2f6e-1d2a-4f3b-9c8d-0e1f2a3b4c5d/cancel": "/v1/jobs/:id/cancel",
		"/v1/jobs":         "/v1/jobs",
		"/wp-admin/x.php":  "other",
		"/v1/jobs/acme-01": "other",
	}
	for path, want := range cases {
		if got := metrics.Route(path); got != want {
			t.Errorf("Route(%q) = %q, want %q", path, got, want)
		}
	}
}
