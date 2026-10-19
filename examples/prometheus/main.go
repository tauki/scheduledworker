package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	sw "github.com/tauki/scheduledworker"
)

func run() error {
	w := sw.New().Start()
	defer w.Stop()
	registry := prom.NewRegistry()
	counter := func(name string) prom.Counter {
		c := prom.NewCounter(prom.CounterOpts{Name: "scheduledworker_" + name + "_total", Help: "Scheduler attempt and admission counters."})
		registry.MustRegister(c)
		return c
	}
	scheduled, started, completed, failed, panicked := counter("scheduled"), counter("started"), counter("completed"), counter("failed"), counter("panicked")
	wait := prom.NewHistogram(prom.HistogramOpts{Name: "scheduledworker_wait_seconds", Help: "Eligibility-to-start delay."})
	duration := prom.NewHistogram(prom.HistogramOpts{Name: "scheduledworker_run_seconds", Help: "Attempt duration."})
	registry.MustRegister(wait, duration, prom.NewGaugeFunc(prom.GaugeOpts{Name: "scheduledworker_queue_length", Help: "Current queued entries."}, func() float64 { return float64(w.Status().QueueLen) }))
	metrics := sw.MetricsFuncs{
		Scheduled: scheduled.Inc, Started: started.Inc, Completed: completed.Inc, Failed: failed.Inc, Panicked: panicked.Inc,
		Wait: func(d time.Duration) { wait.Observe(d.Seconds()) }, Run: func(d time.Duration) { duration.Observe(d.Seconds()) },
	}
	if err := w.SubmitWithOptions(sw.Task{Fn: func() {}}, sw.WithMetrics(metrics)); err != nil {
		return err
	}
	w.Stop()
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.Handle("/status", sw.NewStatusHandler(w))
	mux.Handle("/healthz", sw.NewHealthHandler(w))
	// Exercise a real scrape handler without starting a long-running server.
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "scheduledworker_completed_total 1") {
		return fmt.Errorf("unexpected scrape: %s", response.Body.String())
	}
	fmt.Println("Prometheus scrape: completed_total = 1")
	return nil
}
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
