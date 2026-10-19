package main

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	v1 "github.com/tauki/scheduledworker"
	v2 "github.com/tauki/scheduledworker/v2"
)

func TestIntervalDefaultsInBothV1APIsAndV2(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		anchor := time.Now()
		w := v1.New().Start()
		defer w.Stop()
		times := make(chan time.Time, 2)
		w.Submit(v1.Task{Fn: func() { times <- time.Now() }}, v1.Every(time.Minute), v1.Repeat(1))
		if err := w.SubmitWithOptions(v1.Task{Fn: func() { times <- time.Now() }}, v1.WithEvery(time.Minute), v1.WithRepeat(1)); err != nil {
			t.Fatal(err)
		}
		s, _ := v2.New(v2.Config{})
		_ = s.Start(context.Background())
		defer s.Shutdown(context.Background())
		h, err := s.Schedule(context.Background(), v2.Spec{Kind: v2.FixedDelay, Every: time.Minute, MaxRuns: 1}, func(_ context.Context, r v2.RunInfo) error {
			if !r.ScheduledAt.Equal(anchor.Add(time.Minute)) {
				t.Error(r)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if got := <-times; !got.Equal(anchor.Add(time.Minute)) {
				t.Fatal(got)
			}
		}
		if r, err := h.Wait(context.Background()); err != nil || r.Counters.RunsStarted != 1 {
			t.Fatal(r, err)
		}
	})
}
func TestShutdownDivergenceAndOrderedOptions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		anchor := time.Now()
		w := v1.New().Start()
		var calls int
		// Every then Repeat(0) resolves to one delayed invocation, not unlimited.
		w.Submit(v1.Task{Fn: func() { calls++ }}, v1.Every(time.Hour), v1.Repeat(0))
		w.Stop()
		if calls != 1 || !time.Now().Equal(anchor.Add(time.Hour)) {
			t.Fatal(calls, time.Now())
		}
		s, _ := v2.New(v2.Config{})
		_ = s.Start(context.Background())
		h, _ := s.Schedule(context.Background(), v2.Spec{At: time.Now().Add(time.Hour)}, func(context.Context, v2.RunInfo) error { t.Error("abandoned work invoked"); return nil })
		_ = s.Shutdown(context.Background())
		r, _ := h.Wait(context.Background())
		if r.Reason != v2.SchedulerStopped || !time.Now().Equal(anchor.Add(time.Hour)) {
			t.Fatal(r, time.Now())
		}
		if _, err := s.Schedule(context.Background(), v2.Spec{Kind: v2.FixedDelay, MaxRuns: 3}, func(context.Context, v2.RunInfo) error { return nil }); !errors.Is(err, v2.ErrInvalidSpec) {
			t.Fatal(err)
		}
	})
}
func TestNominalUntilKeepsRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		anchor := time.Now()
		w := v1.New().Start()
		defer w.Stop()
		var v1Calls int
		if err := w.SubmitWithOptions(v1.Task{At: anchor.Add(10 * time.Second), FnErr: func(context.Context) error { v1Calls++; return errors.New("fail") }}, v1.WithUntil(anchor.Add(12*time.Second)), v1.WithRetry(v1.RetryPolicy{MaxAttempts: 2, Backoff: func(int) time.Duration { return 5 * time.Second }})); err != nil {
			t.Fatal(err)
		}
		s, _ := v2.New(v2.Config{})
		_ = s.Start(context.Background())
		defer s.Shutdown(context.Background())
		h, _ := s.Schedule(context.Background(), v2.Spec{At: anchor.Add(10 * time.Second), Until: anchor.Add(12 * time.Second), Retry: v2.RetryPolicy{MaxAttempts: 2, InitialBackoff: 5 * time.Second, MaxBackoff: 5 * time.Second}}, func(context.Context, v2.RunInfo) error { return errors.New("fail") })
		r, _ := h.Wait(context.Background())
		w.Stop()
		if r.Counters.AttemptsDispatched != 2 || v1Calls != 1 {
			t.Fatal(r, v1Calls)
		}
		// Full jitter may put the v2 retry before or after Until; the budget, not
		// retry eligibility, controls whether it is allowed. Core tests fix jitter
		// at five seconds for the exact t=10/12/15 boundary.
	})
}

func TestNilReturnAfterTimeoutChangesOutcome(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := v1.New().Start()
		if err := w.SubmitWithOptions(v1.Task{FnErr: func(ctx context.Context) error { <-ctx.Done(); return nil }}, v1.WithTimeout(time.Second)); err != nil {
			t.Fatal(err)
		}
		s, _ := v2.New(v2.Config{})
		_ = s.Start(context.Background())
		defer s.Shutdown(context.Background())
		h, _ := s.Schedule(context.Background(), v2.Spec{Timeout: time.Second}, func(ctx context.Context, _ v2.RunInfo) error { <-ctx.Done(); return nil })
		r, _ := h.Wait(context.Background())
		w.Stop()
		if w.Status().Completed != 1 || r.LastRun.Outcome != v2.TimedOut || !errors.Is(r.LastRun.Err, context.DeadlineExceeded) {
			t.Fatal(w.Status(), r)
		}
	})
}
