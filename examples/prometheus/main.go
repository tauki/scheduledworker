package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	sw "github.com/tauki/scheduledworker/v2"
)

// Pull cumulative counters from authoritative snapshots. Best-effort observer
// events are suitable for sampled histograms, not authoritative counter totals.
type collector struct {
	scheduler                 *sw.Scheduler
	succeeded, failed, active *prom.Desc
}

func (c collector) Describe(ch chan<- *prom.Desc) { ch <- c.succeeded; ch <- c.failed; ch <- c.active }
func (c collector) Collect(ch chan<- prom.Metric) {
	s := c.scheduler.Status()
	ch <- prom.MustNewConstMetric(c.succeeded, prom.CounterValue, float64(s.Counters.RunsSucceeded))
	ch <- prom.MustNewConstMetric(c.failed, prom.CounterValue, float64(s.Counters.AttemptsFailed))
	ch <- prom.MustNewConstMetric(c.active, prom.GaugeValue, float64(s.ActiveSchedules))
}
func main() {
	s, err := sw.NewWithOptions()
	if err != nil {
		panic(err)
	}
	if err = s.Start(context.Background()); err != nil {
		panic(err)
	}
	registry := prom.NewRegistry()
	registry.MustRegister(collector{s, prom.NewDesc("scheduledworker_v2_runs_succeeded_total", "Terminal successful runs.", nil, nil), prom.NewDesc("scheduledworker_v2_attempts_failed_total", "Failed attempts including timeout and panic; excluding cancellation.", nil, nil), prom.NewDesc("scheduledworker_v2_active_schedules", "Retained registrations, including canceling handlers.", nil, nil)})
	h, err := s.ScheduleWithOptions(context.Background(), func(context.Context, sw.RunInfo) error { return nil })
	if err != nil {
		panic(err)
	}
	if _, err = h.Wait(context.Background()); err != nil {
		panic(err)
	}
	if err = s.Shutdown(context.Background()); err != nil {
		panic(err)
	}
	response := httptest.NewRecorder()
	promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), "scheduledworker_v2_runs_succeeded_total 1") {
		panic(response.Body.String())
	}
	fmt.Println("Prometheus: authoritative runs_succeeded_total = 1")
}
