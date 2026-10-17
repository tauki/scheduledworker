package scheduledworker

import "time"

// MetricsCollector adapts any metrics backend without a core dependency.
// Methods must be prompt and safe for concurrent calls. Panics are isolated.
// Scheduled counts queue insertions, including repeats/retries; other counters
// count attempts. Timing covers the execution middleware and handler.
type MetricsCollector interface {
	IncScheduled()
	IncStarted()
	IncCompleted()
	IncFailed()
	IncPanicked()
	ObserveWait(time.Duration)
	ObserveRun(time.Duration)
}

// WithMetrics appends a collector independently of hook option ordering.
func WithMetrics(collector MetricsCollector) Option {
	return OptionFunc(func(s *Spec) {
		if collector != nil {
			s.collectors = append(s.collectors, collector)
		}
	})
}

func (w *worker) metric(task *Task, fn func(MetricsCollector)) {
	if task.spec == nil {
		return
	}
	for _, collector := range task.spec.collectors {
		w.callback(func() { fn(collector) })
	}
}

// MetricsFuncs adapts callback-based metric APIs. Nil functions are no-ops.
type MetricsFuncs struct {
	Scheduled func()
	Started   func()
	Completed func()
	Failed    func()
	Panicked  func()
	Wait      func(time.Duration)
	Run       func(time.Duration)
}

func (m MetricsFuncs) IncScheduled() {
	if m.Scheduled != nil {
		m.Scheduled()
	}
}
func (m MetricsFuncs) IncStarted() {
	if m.Started != nil {
		m.Started()
	}
}
func (m MetricsFuncs) IncCompleted() {
	if m.Completed != nil {
		m.Completed()
	}
}
func (m MetricsFuncs) IncFailed() {
	if m.Failed != nil {
		m.Failed()
	}
}
func (m MetricsFuncs) IncPanicked() {
	if m.Panicked != nil {
		m.Panicked()
	}
}
func (m MetricsFuncs) ObserveWait(d time.Duration) {
	if m.Wait != nil {
		m.Wait(d)
	}
}
func (m MetricsFuncs) ObserveRun(d time.Duration) {
	if m.Run != nil {
		m.Run(d)
	}
}
