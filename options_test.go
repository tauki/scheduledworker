package scheduledworker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestConstructorOptionsDefaultsAndOrder(t *testing.T) {
	observer := func(context.Context, Event) {}
	for _, tt := range []struct {
		name                       string
		opts                       []Option
		workers, schedules, buffer int
		observed                   bool
	}{
		{"defaults", nil, 10, 10000, 0, false},
		{"explicit zero", []Option{WithWorkers(0), WithMaxSchedules(0), WithEventBuffer(0)}, 10, 10000, 0, false},
		{"observer default buffer", []Option{WithObserver(observer)}, 10, 10000, 256, true},
		{"explicit buffer", []Option{WithEventBuffer(3), WithObserver(observer)}, 10, 10000, 3, true},
		{"last values win", []Option{WithWorkers(20), WithMaxSchedules(2), WithWorkers(1), WithObserver(observer), WithObserver(nil), WithEventBuffer(5), WithEventBuffer(0)}, 1, 2, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, err := New(tt.opts...)
			if err != nil {
				t.Fatal(err)
			}
			defer shutdown(t, s)
			if c := s.cfg; c.workers != tt.workers || c.maxSchedules != tt.schedules || c.eventBuffer != tt.buffer || (c.observer != nil) != tt.observed {
				t.Fatalf("unexpected configuration: %+v", c)
			}
		})
	}
}

func TestScheduleOptionsAreCopiedAndReusable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestScheduler(t, WithWorkers(2))
		defer shutdown(t, s)
		s.jitter = func(n uint64) uint64 { return n / 2 }
		anchor := time.Now()
		policy := RetryPolicy{MaxAttempts: 2}
		opts := []ScheduleOption{
			WithName("old"), WithName("refresh"),
			WithFixedRate(time.Second), WithFixedDelay(time.Minute),
			WithMaxRuns(3), WithMaxRuns(1), WithRetry(policy),
			WithTimeout(time.Second), WithTimeout(0),
		}
		policy.MaxAttempts = 1 // Options capture the policy value, not this variable.
		var handles []*Handle
		for i := 0; i < 2; i++ {
			h := schedule(t, s, func(_ context.Context, run RunInfo) error {
				if run.Name != "refresh" || !run.ScheduledAt.Equal(anchor.Add(time.Minute)) {
					t.Error(run)
				}
				if run.Attempt == 1 {
					return errors.New("retry")
				}
				return nil
			}, opts...)
			handles = append(handles, h)
		}
		opts[len(opts)-1] = WithTimeout(time.Nanosecond)
		opts[1] = WithName("changed after admission")
		for _, h := range handles {
			r := result(t, h)
			if r.Reason != RunLimitReached || r.Counters.RunsSucceeded != 1 || r.Counters.AttemptsDispatched != 2 {
				t.Fatal(r)
			}
			if !r.LastRun.FinishedAt.Equal(anchor.Add(time.Minute + time.Second/2)) {
				t.Fatal("retry defaults", r.LastRun)
			}
		}
	})
}

func TestInvalidScheduleOptionsDoNotCommit(t *testing.T) {
	s := newTestScheduler(t, WithWorkers(1), WithMaxSchedules(1))
	defer shutdown(t, s)
	for _, opts := range [][]ScheduleOption{{WithName("valid"), nil}, {WithFixedDelay(0)}, {WithOverlap(SkipOverlap)}} {
		h, err := s.Schedule(background, func(context.Context, RunInfo) error {
			t.Error("invalid registration executed")
			return nil
		}, opts...)
		if h != nil || !errors.Is(err, ErrInvalidSpec) {
			t.Fatal(h, err)
		}
	}
	if st := s.Status(); st.Accepted != 0 || st.ActiveSchedules != 0 {
		t.Fatal(st)
	}
	h := schedule(t, s, func(context.Context, RunInfo) error { return nil })
	if r := result(t, h); r.Reason != OneShotFinished || r.Counters.AttemptsDispatched != 1 || r.Counters.RunsSucceeded != 1 {
		t.Fatal(r)
	}
}

func TestOptionsCanBeSharedByConcurrentCallers(t *testing.T) {
	constructor := []Option{WithWorkers(1), WithMaxSchedules(4)}
	opts := []ScheduleOption{WithName("shared"), WithMaxRuns(1)}
	var callers sync.WaitGroup
	for i := 0; i < 8; i++ {
		callers.Go(func() {
			s := newTestScheduler(t, constructor...)
			defer shutdown(t, s)
			h := schedule(t, s, func(_ context.Context, run RunInfo) error {
				if run.Name != "shared" {
					t.Error(run)
				}
				return nil
			}, opts...)
			if result(t, h).Counters.RunsSucceeded != 1 {
				t.Error("shared options lost")
			}
		})
	}
	callers.Wait()
}
