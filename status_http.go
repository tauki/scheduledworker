package scheduledworker

import (
	"encoding/json"
	"net/http"
)

// NewStatusHandler exposes a snapshot. The application owns routing, access
// control and server lifetime; no HTTP server is started by the library.
func NewStatusHandler(w interface{ Status() Status }) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(w.Status())
	})
}

// NewHealthHandler reports readiness: started and accepting work. Normal queued
// or executing work is healthy. Stopping, stopped, and idle workers return 503.
func NewHealthHandler(w interface{ Status() Status }) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
		status := w.Status()
		if status.Running && !status.Stopping && !status.Stopped {
			_, _ = rw.Write([]byte("ok\n"))
			return
		}
		rw.WriteHeader(http.StatusServiceUnavailable)
		_, _ = rw.Write([]byte("not ready\n"))
	})
}
