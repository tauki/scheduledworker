package scheduledworker

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestFixedRateOverlapPolicies(t *testing.T) {
	for _, policy := range []OverlapPolicy{BufferLatest, SkipOverlap} {
		synctest.Test(t, func(t *testing.T) {
			s := newTestScheduler(t, WithWorkers(2))
			defer shutdown(t, s)
			anchor := time.Now()
			release := make(chan struct{})
			runs := make(chan RunInfo, 3)
			h := schedule(t, s, func(_ context.Context, r RunInfo) error {
				runs <- r
				if r.Number == 1 {
					<-release
				}
				return nil
			}, WithFixedRate(5*time.Second), WithAt(anchor), WithOverlap(policy), WithMaxRuns(2))
			<-runs
			time.Sleep(16 * time.Second)
			synctest.Wait()
			if s.Status().RunningAttempts != 1 {
				t.Fatal("overlap", s.Status())
			}
			close(release)
			second := <-runs
			want := anchor.Add(15 * time.Second)
			if policy == SkipOverlap {
				want = anchor.Add(20 * time.Second)
			}
			if !second.ScheduledAt.Equal(want) {
				t.Fatalf("%v != %v", second.ScheduledAt, want)
			}
			r := result(t, h)
			if r.Reason != RunLimitReached || r.Counters.RunsStarted != 2 {
				t.Fatal(r)
			}
			if policy == BufferLatest && r.Counters.OccurrencesCoalesced != 2 {
				t.Fatal(r)
			}
			if policy == SkipOverlap && r.Counters.OccurrencesSkipped != 3 {
				t.Fatal(r)
			}
		})
	}
}
func TestFixedRateSaturationAndInclusiveUntil(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestScheduler(t, WithWorkers(1))
		defer shutdown(t, s)
		release := make(chan struct{})
		started := make(chan struct{})
		block := schedule(t, s, func(context.Context, RunInfo) error { close(started); <-release; return nil })
		<-started
		anchor := time.Now()
		nominal := make(chan time.Time, 1)
		h := schedule(t, s, func(_ context.Context, r RunInfo) error { nominal <- r.ScheduledAt; return nil }, WithFixedRate(time.Second), WithAt(anchor), WithUntil(anchor.Add(3*time.Second)), WithOverlap(SkipOverlap))
		time.Sleep(4 * time.Second)
		synctest.Wait()
		if s.Status().ReadySchedules != 1 || h.Status().Counters.OccurrencesCoalesced != 3 {
			t.Fatal(s.Status(), h.Status())
		}
		close(release)
		result(t, block)
		if got := <-nominal; !got.Equal(anchor.Add(3 * time.Second)) {
			t.Fatal(got)
		}
		if r := result(t, h); r.Reason != UntilReached || r.Counters.RunsStarted != 1 {
			t.Fatal(r)
		}
	})
}

func TestBillionMissedTicksAreCoalescedArithmetically(t *testing.T) {
	s, _ := NewWithOptions()
	c := newCoordinator(s, background)
	r := &registration{h: &Handle{id: "test"}, spec: spec{Kind: FixedRate, Every: time.Nanosecond}, hasNextTick: true}
	c.advanceRate(r, 1_000_000_000)
	if !r.hasPendingOccurrence || r.nominal != 1_000_000_000 || r.tick.at != 1_000_000_001 || r.status.Counters.OccurrencesCoalesced != 1_000_000_000 {
		t.Fatal(r)
	}
}
func TestFixedDelayAfterFailedRetryAndFairness(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestScheduler(t, WithWorkers(1))
		defer shutdown(t, s)
		s.jitter = func(n uint64) uint64 { return n - 1 }
		anchor := time.Now()
		runs := make(chan RunInfo, 3)
		h := schedule(t, s, func(_ context.Context, r RunInfo) error {
			runs <- r
			if r.Number == 1 {
				if r.Attempt == 1 {
					time.Sleep(3 * time.Second)
				}
				return errors.New("failure")
			}
			return nil
		}, WithFixedDelay(30*time.Second), WithAt(anchor), WithMaxRuns(2), WithRetry(RetryPolicy{MaxAttempts: 2, InitialBackoff: 5 * time.Second, MaxBackoff: 5 * time.Second}))
		first := <-runs
		second := <-runs
		third := <-runs
		if first.RunID != second.RunID || second.Attempt != 2 || third.Number != 2 || !third.ScheduledAt.Equal(anchor.Add(38*time.Second)) {
			t.Fatal(first, second, third)
		}
		r := result(t, h)
		if r.Counters.RunsFailed != 1 || r.Counters.RunsSucceeded != 1 || r.Counters.AttemptsDispatched != 3 {
			t.Fatal(r)
		}
	})
}
func TestIntervalsDefaultFirstTimeAndFailedRunsCount(t *testing.T) {
	for _, interval := range []ScheduleOption{WithFixedDelay(time.Minute), WithFixedRate(time.Minute)} {
		synctest.Test(t, func(t *testing.T) {
			s := newTestScheduler(t, WithWorkers(1))
			defer shutdown(t, s)
			anchor := time.Now()
			h := schedule(t, s, func(_ context.Context, r RunInfo) error {
				if r.Number == 1 && !r.ScheduledAt.Equal(anchor.Add(time.Minute)) {
					t.Error(r)
				}
				return errors.New("fail")
			}, interval, WithMaxRuns(3))
			r := result(t, h)
			if r.Reason != RunLimitReached || r.Counters.RunsFailed != 3 {
				t.Fatal(r)
			}
		})
	}
}

func TestZeroRunLimitIsUnlimitedOnlyForIntervals(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestScheduler(t, WithWorkers(1))
		defer shutdown(t, s)
		runs := make(chan uint64)
		h := schedule(t, s, func(_ context.Context, r RunInfo) error { runs <- r.Number; return nil }, WithFixedDelay(time.Minute))
		for want := uint64(1); want <= 2; want++ {
			if got := <-runs; got != want {
				t.Fatal(got, want)
			}
		}
		if err := h.Cancel(background); err != nil {
			t.Fatal(err)
		}
		if r := result(t, h); r.Reason != ExplicitlyCanceled || r.Counters.RunsStarted != 2 {
			t.Fatal(r)
		}
		once := schedule(t, s, func(context.Context, RunInfo) error { return nil })
		if r := result(t, once); r.Reason != OneShotFinished || r.Counters.RunsStarted != 1 {
			t.Fatal(r)
		}
	})
}
