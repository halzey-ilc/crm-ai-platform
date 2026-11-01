package httpapi

import "time"

// SetEventTiming shortens the job event stream intervals for tests.
func SetEventTiming(poll, heartbeat time.Duration) {
	eventPoll, eventHeartbeat = poll, heartbeat
}
