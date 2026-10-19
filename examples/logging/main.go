package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"

	sw "github.com/tauki/scheduledworker/v2"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	s, err := sw.New(sw.WithObserver(func(_ context.Context, e sw.Event) {
		// IDs belong in logs/traces, not unbounded metric labels. Events can drop.
		log.Info("scheduler event", "schedule", e.ScheduleID, "run", e.Run.RunID, "kind", e.Kind, "outcome", e.Outcome)
	}))
	if err != nil {
		panic(err)
	}
	if err = s.Start(context.Background()); err != nil {
		panic(err)
	}
	h, err := s.Schedule(context.Background(), func(ctx context.Context, r sw.RunInfo) error {
		// Immutable business metadata stays with application instrumentation.
		log.InfoContext(ctx, "refresh", "customer", "example", "run", r.RunID)
		return nil
	}, sw.WithName("refresh"))
	if err != nil {
		panic(err)
	}
	if _, err = h.Wait(context.Background()); err != nil {
		panic(err)
	}
	// Readiness concerns lifecycle. Future work and a busy pool are normal.
	status := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snapshot := s.Status()
		w.Header().Set("Content-Type", "application/json")
		if snapshot.State != sw.Running {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(snapshot)
	})
	response := httptest.NewRecorder()
	status.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if response.Code != http.StatusOK {
		panic("unexpected readiness")
	}
	if err = s.Shutdown(context.Background()); err != nil {
		panic(err)
	}
}
