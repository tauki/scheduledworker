package scheduledworker

import (
	"fmt"
	"time"
)

func normalizeConfig(c Config) (Config, error) {
	if c.Workers < 0 {
		return c, fmt.Errorf("%w: Workers is negative", ErrInvalidConfig)
	}
	if c.MaxSchedules < 0 {
		return c, fmt.Errorf("%w: MaxSchedules is negative", ErrInvalidConfig)
	}
	if c.EventBuffer < 0 {
		return c, fmt.Errorf("%w: EventBuffer is negative", ErrInvalidConfig)
	}
	if c.Workers == 0 {
		c.Workers = 10
	}
	if c.MaxSchedules == 0 {
		c.MaxSchedules = 10_000
	}
	if c.Workers > c.MaxSchedules {
		return c, fmt.Errorf("%w: Workers exceeds MaxSchedules", ErrInvalidConfig)
	}
	if c.EventBuffer == 0 && c.Observer != nil {
		c.EventBuffer = 256
	}
	return c, nil
}
func normalizeSpec(s Spec, h Handler) (Spec, error) {
	bad := func(field string) (Spec, error) { return s, fmt.Errorf("%w: %s", ErrInvalidSpec, field) }
	if h == nil {
		return bad("Handler is nil")
	}
	if s.Kind > FixedRate {
		return bad("Kind")
	}
	if s.Overlap > SkipOverlap || (s.Overlap == SkipOverlap && s.Kind != FixedRate) {
		return bad("Overlap")
	}
	if s.Kind == Once {
		if s.Every != 0 || s.MaxRuns > 1 {
			return bad("Every/MaxRuns for Once")
		}
		s.MaxRuns = 1
	} else if s.Every <= 0 {
		return bad("Every")
	}
	if s.Timeout < 0 {
		return bad("Timeout")
	}
	r := &s.Retry
	if r.MaxAttempts < 0 || r.InitialBackoff < 0 || r.MaxBackoff < 0 {
		return bad("Retry")
	}
	if r.MaxAttempts == 0 {
		r.MaxAttempts = 1
	}
	if r.MaxAttempts == 1 {
		if r.InitialBackoff != 0 || r.MaxBackoff != 0 {
			return bad("Retry backoff without retries")
		}
	} else {
		if r.InitialBackoff == 0 {
			r.InitialBackoff = time.Second
		}
		if r.MaxBackoff == 0 {
			r.MaxBackoff = time.Minute
		}
		if r.MaxBackoff < r.InitialBackoff {
			return bad("Retry.MaxBackoff")
		}
	}
	return s, nil
}

func resolveSpec(s Spec, now instant) (first, until int64, err error) {
	bad := func(field string) (int64, int64, error) {
		return 0, 0, fmt.Errorf("%w: %s outside elapsed-time range", ErrInvalidSpec, field)
	}
	var ok bool
	if s.At.IsZero() {
		first, ok = addTime(now.mono, s.Every)
	} else {
		first, ok = resolve(s.At, now)
	}
	if !ok {
		return bad("At/Every")
	}
	if !s.Until.IsZero() {
		until, ok = resolve(s.Until, now)
		if !ok {
			return bad("Until")
		}
		if until < first {
			return 0, 0, fmt.Errorf("%w: Until precedes first occurrence", ErrInvalidSpec)
		}
	}
	return first, until, nil
}
