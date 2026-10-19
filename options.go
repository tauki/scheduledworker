package scheduledworker

import "fmt"

type config struct {
	workers      int
	maxSchedules int
	observer     Observer
	eventBuffer  int
}

// Option configures a scheduler before construction. Options apply in order;
// the last value for a setting wins. A nil Option is invalid.
type Option func(*config)

// WithWorkers bounds dispatched attempts until their handlers return.
// Zero selects the default of 10; negative values are invalid.
func WithWorkers(n int) Option {
	return func(c *config) { c.workers = n }
}

// WithMaxSchedules bounds registrations, including those being canceled.
// Zero selects the default of 10,000. The limit must be at least the worker count.
func WithMaxSchedules(n int) Option {
	return func(c *config) { c.maxSchedules = n }
}

// WithObserver enables asynchronous, best-effort event delivery. Nil disables it.
func WithObserver(observer Observer) Option {
	return func(c *config) { c.observer = observer }
}

// WithEventBuffer bounds queued observer events. Zero selects 256 when an
// observer is enabled; negative values are invalid.
func WithEventBuffer(n int) Option {
	return func(c *config) { c.eventBuffer = n }
}

func normalizeConfig(c config) (config, error) {
	if c.workers < 0 {
		return c, fmt.Errorf("%w: workers is negative", ErrInvalidConfig)
	}
	if c.maxSchedules < 0 {
		return c, fmt.Errorf("%w: max schedules is negative", ErrInvalidConfig)
	}
	if c.eventBuffer < 0 {
		return c, fmt.Errorf("%w: event buffer is negative", ErrInvalidConfig)
	}
	if c.workers == 0 {
		c.workers = 10
	}
	if c.maxSchedules == 0 {
		c.maxSchedules = 10_000
	}
	if c.workers > c.maxSchedules {
		return c, fmt.Errorf("%w: workers exceeds max schedules", ErrInvalidConfig)
	}
	if c.eventBuffer == 0 && c.observer != nil {
		c.eventBuffer = 256
	}
	return c, nil
}
