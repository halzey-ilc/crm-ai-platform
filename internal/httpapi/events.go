package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Event stream timing; variables so tests can shorten them.
var (
	eventPoll        = time.Second
	eventHeartbeat   = 15 * time.Second
	eventStreamLimit = 20 * time.Minute
)

// GET /v1/jobs/{id}/events: Server-Sent Events instead of polling GET /v1/jobs/{id}.
//
//	event: status   data: {"status":"running","attempts":1}   on every change
//	event: job      data: <the same body as GET /v1/jobs/{id}>  once finished, then the stream ends
//
// The result arrives whole: outputs are checked against their contract before
// they are stored, so there is no partial text to stream. A ": ping" comment
// every 15 s keeps proxies from closing an idle stream. The stream ends after
// 20 minutes; reconnect or fall back to GET.
func (h *handlers) jobEvents(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	id := r.PathValue("id")
	job, res, err := h.d.Jobs.Get(r.Context(), p.Tenant, id)
	if err != nil {
		h.jobError(w, r, err)
		return
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	write := func(s string) bool {
		// The server's write timeout is short; each event gets its own.
		_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if _, err := fmt.Fprint(w, s); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	send := func(event string, v any) bool {
		data, _ := json.Marshal(v)
		return write("event: " + event + "\ndata: " + string(data) + "\n\n")
	}

	last := ""
	poll := time.NewTicker(eventPoll)
	defer poll.Stop()
	ping := time.NewTicker(eventHeartbeat)
	defer ping.Stop()
	for {
		if key := fmt.Sprint(job.Status, job.Attempts); key != last {
			last = key
			if !send("status", map[string]any{"status": job.Status, "attempts": job.Attempts}) {
				return
			}
		}
		if job.Status.Terminal() {
			send("job", jobView(job, res))
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if !write(": ping\n\n") {
				return
			}
		case <-poll.C:
			if job, res, err = h.d.Jobs.Get(r.Context(), p.Tenant, id); err != nil {
				if r.Context().Err() == nil {
					send("error", map[string]string{"code": "internal_error", "message": "could not read the job"})
				}
				return
			}
		}
	}
}
