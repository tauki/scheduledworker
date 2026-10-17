package scheduledworker

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"time"
)

// WithJitter defers initial and recurring eligibility by a random duration in
// [0,maxJitter). Zero disables jitter. It does not affect retry delays.
func WithJitter(maxJitter time.Duration) Option {
	return OptionFunc(func(s *Spec) {
		if maxJitter < 0 {
			s.err = fmt.Errorf("%w: negative jitter", ErrInvalidTask)
			return
		}
		if maxJitter == 0 {
			return
		}
		jitter := func(ctx context.Context, t *Task, next Next) error {
			t.At = t.At.Add(time.Duration(rand.Int63n(int64(maxJitter))))
			return next(ctx, t)
		}
		s.OnEnqueue = append(s.OnEnqueue, jitter)
		s.OnReschedule = append(s.OnReschedule, jitter)
	})
}

// WithBusinessHours defers scheduling to the next daily [startHour,endHour)
// window in tz. Hours must satisfy 0<=start<end<=24. Unknown zones are errors.
// Calendar arithmetic respects DST; repeated/missing civil times use Go's
// time.Date resolution. This is a window filter, not a cron scheduler.
func WithBusinessHours(tz string, startHour, endHour int) Option {
	return OptionFunc(func(s *Spec) {
		location, err := time.LoadLocation(tz)
		if err != nil || startHour < 0 || startHour >= endHour || endHour > 24 {
			s.err = fmt.Errorf("%w: invalid business hours or timezone", ErrInvalidTask)
			return
		}
		adjust := func(ctx context.Context, t *Task, next Next) error {
			t.At = businessTime(t.At, location, startHour, endHour)
			return next(ctx, t)
		}
		s.OnEnqueue = append(s.OnEnqueue, adjust)
		s.OnReschedule = append(s.OnReschedule, adjust)
	})
}

func businessTime(at time.Time, location *time.Location, start, end int) time.Time {
	for {
		local := at.In(location)
		if local.Hour() >= start && local.Hour() < end {
			return at
		}
		day := local.Day()
		if local.Hour() >= end {
			day++
		}
		candidate := time.Date(local.Year(), local.Month(), day, start, 0, 0, 0, location)
		// Date may normalize a missing civil hour (or a skipped civil date)
		// backwards. Advance through the transition and recheck the window.
		for !candidate.After(at) {
			candidate = candidate.Add(time.Hour)
		}
		at = candidate
	}
}

// WithKeyLock serializes invocations sharing a nonempty key within this worker.
// Contending tasks wait in the queue without occupying slots. Keys are released
// on completion/panic and removed when idle. Different workers do not coordinate.
func WithKeyLock(key string) Option {
	return OptionFunc(func(s *Spec) {
		if key == "" {
			s.err = fmt.Errorf("%w: key must not be empty", ErrInvalidTask)
			return
		}
		s.Key = key
	})
}

// ExponentialBackoff returns bounded exponential delays. The argument is a
// one-based retry number, as supplied by RetryPolicy. With jitter,
// delays vary by +/-20% and remain within [0,max]. Invalid parameters panic.
func ExponentialBackoff(base time.Duration, factor float64, max time.Duration, jitter bool) func(int) time.Duration {
	if base <= 0 || max < base || factor < 1 || math.IsNaN(factor) || math.IsInf(factor, 0) {
		panic("scheduledworker: invalid exponential backoff")
	}
	return func(n int) time.Duration {
		if n < 1 {
			n = 1
		}
		value := float64(base) * math.Pow(factor, float64(n-1))
		duration := max
		if value < float64(max) {
			duration = time.Duration(value)
		}
		if jitter && duration > 0 {
			span := duration / 5
			delta := time.Duration(rand.Int63n(int64(2*span)+1)) - span
			if delta > 0 && duration > max-delta {
				return max
			}
			duration += delta
		}
		return duration
	}
}
