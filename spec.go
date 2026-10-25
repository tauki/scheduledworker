package scheduledworker

import (
	"fmt"
	"time"
)

// Both admission APIs copy into spec before validation and coordinator handoff.
type spec Spec

func normalizeSpec(s spec, h Handler) (spec, error) {
	bad := func(field string) (spec, error) { return s, fmt.Errorf("%w: %s", ErrInvalidSpec, field) }
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

func resolveSpec(s spec, now instant) (first, until int64, err error) {
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
