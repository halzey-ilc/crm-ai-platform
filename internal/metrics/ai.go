package metrics

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The AI Platform's series. Labels never contain tenant ids or job content.
var (
	HTTPRequests = Default.NewCounter("ai_http_requests_total",
		"Gateway HTTP requests.", "method", "route", "status")
	HTTPDuration = Default.NewHistogram("ai_http_request_duration_seconds",
		"Gateway HTTP request duration.", []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}, "route")

	JobsFinished = Default.NewCounter("ai_jobs_finished_total",
		"Jobs that reached a final or retry state, by outcome (succeeded, failed, retry, cancelled) and error code.",
		"job_type", "outcome", "error_code")
	JobDuration = Default.NewHistogram("ai_job_execution_seconds",
		"Time spent executing one job attempt (model call and checks).",
		[]float64{0.1, 0.5, 1, 5, 15, 30, 60, 120, 300, 600}, "job_type")

	ModelCalls = Default.NewCounter("ai_model_calls_total",
		"Model provider calls by outcome (ok, transient, error).", "task", "provider", "outcome")
	ModelLatency = Default.NewHistogram("ai_model_call_seconds",
		"Model provider call latency.", []float64{0.05, 0.25, 1, 5, 15, 30, 60, 120, 300, 600}, "task", "provider")

	QueueJobs = Default.NewGauge("ai_queue_jobs",
		"Jobs in the queue by status, over all tenants (refreshed by the orchestrator).", "status")
	QueueOldestQueued = Default.NewGauge("ai_queue_oldest_queued_seconds",
		"Age of the oldest runnable queued job.")

	RetentionDeleted = Default.NewCounter("ai_retention_jobs_deleted_total",
		"Finished jobs (with their results) deleted by the retention cleanup.")
	RetentionRuns = Default.NewCounter("ai_retention_runs_total",
		"Retention cleanup runs by outcome (ok, error).", "outcome")
)

var idSegment = regexp.MustCompile(`/[0-9a-fA-F-]{36}(/|$)`)

// The gateway's routes; anything else (scanners, typos) is counted as "other"
// so arbitrary paths cannot create new series.
var knownRoutes = map[string]bool{
	"/v1/jobs": true, "/v1/jobs/:id": true, "/v1/jobs/:id/cancel": true, "/v1/job-types": true,
	"/health/live": true, "/health/ready": true,
	"/v1/knowledge/ask": true, "/v1/knowledge/documents/:ref": true,
}

// Route turns a path into a low-cardinality label: ids become ":id".
func Route(path string) string {
	route := idSegment.ReplaceAllString(path, "/:id$1")
	if strings.HasPrefix(route, "/v1/knowledge/documents/") {
		route = "/v1/knowledge/documents/:ref" // refs are CRM ids, not labels
	}
	if !knownRoutes[route] {
		return "other"
	}
	return route
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// HTTP instruments a handler with request counts and durations.
func HTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		started := time.Now()
		next.ServeHTTP(rec, r)
		route := Route(r.URL.Path)
		HTTPRequests.Inc(r.Method, route, strconv.Itoa(rec.status))
		HTTPDuration.Observe(time.Since(started).Seconds(), route)
	})
}
