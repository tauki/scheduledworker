package scheduledworker

import "time"

// ScheduleOption configures ScheduleWithOptions before admission. Options apply in
// order; the last value for a setting wins. Options are not retained by the
// registration. A nil ScheduleOption is invalid.
type ScheduleOption func(*spec)

// WithName sets a name for run metadata and schedule snapshots.
func WithName(name string) ScheduleOption {
	return func(s *spec) { s.Name = name }
}

// WithAt sets the first nominal occurrence. Zero means immediate for a one-shot,
// or admission time plus the interval for a recurring schedule.
func WithAt(at time.Time) ScheduleOption {
	return func(s *spec) { s.At = at }
}

// WithFixedDelay schedules occurrences every interval after the previous run's
// final attempt returns. The interval must be positive.
func WithFixedDelay(every time.Duration) ScheduleOption {
	return func(s *spec) { s.Kind, s.Every = FixedDelay, every }
}

// WithFixedRate schedules occurrences on an anchored interval without overlap
// within the registration. The interval must be positive.
func WithFixedRate(every time.Duration) ScheduleOption {
	return func(s *spec) { s.Kind, s.Every = FixedRate, every }
}

// WithOverlap selects how fixed-rate ticks are handled during an open run.
// BufferLatest is the default. SkipOverlap is valid only for fixed-rate schedules.
func WithOverlap(policy OverlapPolicy) ScheduleOption {
	return func(s *spec) { s.Overlap = policy }
}

// WithMaxRuns bounds first dispatches, including failed runs. Zero is unlimited
// for recurring schedules. A one-shot accepts only zero or one and runs once.
func WithMaxRuns(n uint64) ScheduleOption {
	return func(s *spec) { s.MaxRuns = n }
}

// WithUntil sets an inclusive nominal occurrence cutoff. Zero disables it.
// It does not bound retry eligibility or execution times.
func WithUntil(until time.Time) ScheduleOption {
	return func(s *spec) { s.Until = until }
}

// WithTimeout sets a cooperative timeout per attempt. Zero disables it;
// negative durations are invalid. A slot stays occupied until the handler returns.
func WithTimeout(timeout time.Duration) ScheduleOption {
	return func(s *spec) { s.Timeout = timeout }
}

// WithRetry sets the retry policy. Defaults within the policy are applied during
// admission validation. With no option, each run has one attempt.
func WithRetry(policy RetryPolicy) ScheduleOption {
	return func(s *spec) { s.Retry = policy }
}
