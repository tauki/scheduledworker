package scheduledworker

import (
	"container/heap"
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type shiftingClock struct {
	clock
	shift atomic.Int64
	reads atomic.Uint64
}

func (c *shiftingClock) now() instant {
	n := c.clock.now()
	c.reads.Add(1)
	n.wall = n.wall.Add(time.Duration(c.shift.Load()))
	return n
}

func TestWallJumpDoesNotChangeFixedDelayOrCutoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _ := New(Config{Workers: 1})
		clock := &shiftingClock{clock: s.clock}
		s.clock = clock
		if err := s.Start(background); err != nil {
			t.Fatal(err)
		}
		defer shutdown(t, s)
		anchor := time.Now()
		started := make(chan struct{})
		release := make(chan struct{})
		times := make(chan time.Time, 2)
		h := schedule(t, s, Spec{Kind: FixedDelay, At: anchor, Every: 10 * time.Second, Until: anchor.Add(10 * time.Second)}, func(_ context.Context, r RunInfo) error {
			times <- r.ScheduledAt
			if r.Number == 1 {
				close(started)
				<-release
			}
			return nil
		})
		<-started
		clock.shift.Store(int64(24 * time.Hour))
		close(release)
		r := result(t, h)
		if r.Reason != UntilReached || r.Counters.RunsStarted != 2 {
			t.Fatal(r)
		}
		if first, second := <-times, <-times; !first.Equal(anchor) || !second.Equal(anchor.Add(10*time.Second)) {
			t.Fatal(first, second)
		}
		if !r.LastRun.FinishedAt.Equal(anchor.Add(24*time.Hour + 10*time.Second)) {
			t.Fatal(r.LastRun)
		}
	})
}
func TestSaturatedReadyWorkDoesNotSpin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _ := New(Config{Workers: 1})
		clock := &shiftingClock{clock: s.clock}
		s.clock = clock
		_ = s.Start(background)
		defer shutdown(t, s)
		release := make(chan struct{})
		started := make(chan struct{})
		a := schedule(t, s, Spec{}, func(context.Context, RunInfo) error { close(started); <-release; return nil })
		<-started
		b := schedule(t, s, Spec{}, func(context.Context, RunInfo) error { return nil })
		synctest.Wait()
		before := clock.reads.Load()
		time.Sleep(time.Hour)
		synctest.Wait()
		if got := clock.reads.Load() - before; got > 2 {
			t.Fatalf("idle coordinator woke %d times", got)
		}
		close(release)
		result(t, a)
		result(t, b)
	})
}
func TestRetryReadyRejoinsTailAndKeepsRunIdentity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestScheduler(t, Config{Workers: 1})
		defer shutdown(t, s)
		s.jitter = func(n uint64) uint64 { return n - 1 }
		order := make(chan string, 4)
		a := schedule(t, s, Spec{Retry: RetryPolicy{MaxAttempts: 2, InitialBackoff: time.Second, MaxBackoff: time.Second}}, func(_ context.Context, r RunInfo) error {
			order <- "a"
			if r.Attempt == 1 {
				return errors.New("retry")
			}
			return nil
		})
		synctest.Wait()
		release := make(chan struct{})
		started := make(chan struct{})
		block := schedule(t, s, Spec{}, func(context.Context, RunInfo) error { close(started); <-release; return nil })
		<-started
		b := schedule(t, s, Spec{}, func(context.Context, RunInfo) error { order <- "b"; return nil })
		time.Sleep(time.Second)
		synctest.Wait()
		if a.Status().State != Ready || s.Status().WaitingRetries != 0 || s.Status().ReadySchedules != 2 {
			t.Fatal(a.Status(), s.Status())
		}
		close(release)
		result(t, block)
		result(t, b)
		result(t, a)
		for _, want := range []string{"a", "b", "a"} {
			if got := <-order; got != want {
				t.Fatal(got, want)
			}
		}
	})
}
func TestRetryAllowedBeyondNominalUntilAndFinishPrecedence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestScheduler(t, Config{Workers: 1})
		defer shutdown(t, s)
		s.jitter = func(n uint64) uint64 { return n - 1 }
		anchor := time.Now()
		h := schedule(t, s, Spec{Kind: FixedRate, At: anchor.Add(10 * time.Second), Every: time.Second, MaxRuns: 1, Until: anchor.Add(12 * time.Second), Retry: RetryPolicy{MaxAttempts: 2, InitialBackoff: 5 * time.Second, MaxBackoff: 5 * time.Second}}, func(_ context.Context, r RunInfo) error {
			if r.Attempt == 1 {
				return errors.New("retry")
			}
			return nil
		})
		r := result(t, h)
		if r.Reason != RunLimitReached || r.Counters.AttemptsDispatched != 2 || !r.LastRun.FinishedAt.Equal(anchor.Add(15*time.Second)) {
			t.Fatal(r)
		}
		release := make(chan struct{})
		started := make(chan struct{})
		x := schedule(t, s, Spec{}, func(context.Context, RunInfo) error { close(started); <-release; panic("panic wins cancellation") })
		<-started
		_ = x.Cancel(background)
		wait := make(chan struct{})
		go func() { shutdown(t, s); close(wait) }()
		synctest.Wait()
		close(release)
		<-wait
		if got := result(t, x); got.Reason != ExplicitlyCanceled || got.LastRun.Outcome != Panicked {
			t.Fatal(got)
		}
	})
}

func FuzzIndexedHeap(f *testing.F) {
	f.Add([]byte{4, 1, 3, 0, 2, 8, 255})
	f.Fuzz(func(t *testing.T, ops []byte) {
		var h deadlineHeap
		nodes := make([]deadline, 16)
		for i := range nodes {
			nodes[i].index = -1
			nodes[i].order = uint64(i)
		}
		for i, op := range ops {
			if i > 1000 {
				break
			}
			d := &nodes[int(op)%len(nodes)]
			if d.index >= 0 {
				heap.Remove(&h, d.index)
			} else {
				d.at = int64(op)
				heap.Push(&h, d)
			}
			for j, n := range h {
				if n.index != j {
					t.Fatal("index")
				}
				if j > 0 && h.Less(j, (j-1)/2) {
					t.Fatal("order")
				}
			}
		}
		var prev *deadline
		for len(h) > 0 {
			d := heap.Pop(&h).(*deadline)
			if prev != nil && (d.at < prev.at || d.at == prev.at && d.order < prev.order) {
				t.Fatal("pop order")
			}
			prev = d
		}
	})
}

type manualClock struct{ at int64 }

func (c *manualClock) now() instant { return instant{c.at, time.Unix(0, c.at)} }

// Exercise transitions without workers so every operation can verify the actual
// heap, ready list, registration retention, and attempt/run accounting.
func FuzzCoordinatorTransitions(f *testing.F) {
	f.Add([]byte{0, 0, 1, 2, 3, 4, 0, 7, 2, 1, 8, 9})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 2, 3, 4, 5, 6})
	f.Fuzz(func(t *testing.T, ops []byte) {
		s, _ := New(Config{Workers: 2, MaxSchedules: 8})
		s.state = Running
		clock := &manualClock{}
		s.clock = clock
		s.jitter = func(n uint64) uint64 { return n / 2 }
		c := newCoordinator(s, background)
		for i, op := range ops {
			if i >= 128 {
				break
			}
			switch op % 6 {
			case 0:
				input := Spec{Kind: Kind(op / 6 % 3), Every: time.Duration(op/6%3) * time.Second, MaxRuns: 3, Retry: RetryPolicy{MaxAttempts: 2}}
				if input.Kind == Once {
					input.MaxRuns = 1
				}
				spec, err := normalizeSpec(input, func(context.Context, RunInfo) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				cmd := command{ctx: background, spec: spec, handler: func(context.Context, RunInfo) error {
					if op%2 == 0 {
						return errors.New("fail")
					}
					return nil
				}, reply: make(chan response, 1)}
				c.command(cmd)
				<-cmd.reply
			case 1:
				clock.at += int64(time.Second) * int64(op/6+1)
			case 2:
				if c.ready.Len() > 0 && c.status.RunningAttempts < 2 {
					c.dispatch(c.ready.Front().Value.(*registration))
				}
			case 3:
				select {
				case a := <-c.jobs:
					c.complete(invoke(a, clock))
				default:
				}
			case 4:
				for _, r := range c.regs {
					c.cancel(r, ExplicitlyCanceled, true)
					break
				}
			case 5:
				if op > 200 {
					c.beginStop()
					s.state = Stopping
				}
			}
			for len(c.timers) > 0 && c.timers[0].at <= clock.at {
				c.due(heap.Pop(&c.timers).(*deadline), clock.now())
			}
			if len(c.regs) > 8 || c.status.RunningAttempts > 2 || len(c.timers) > 2*len(c.regs) || c.ready.Len() > len(c.regs) {
				t.Fatal("resource bound", c.status, len(c.timers))
			}
			ready, waiting, buffered := 0, 0, 0
			for _, r := range c.regs {
				st := r.h.Status()
				if st.State == Ready {
					ready++
				}
				if st.State == RetryWaiting {
					waiting++
				}
				if !st.BufferedAt.IsZero() {
					buffered++
				}
				if st.Counters.AttemptsDispatched < st.Counters.RunsStarted || st.Counters.RunsSucceeded+st.Counters.RunsFailed+st.Counters.RunsCanceled > st.Counters.RunsStarted {
					t.Fatal(st)
				}
			}
			if ready != c.status.ReadySchedules || waiting != c.status.WaitingRetries || buffered != c.status.BufferedSchedules {
				t.Fatal("snapshot gauges", c.status, ready, waiting, buffered)
			}
		}
		c.beginStop()
		for len(c.jobs) > 0 {
			c.complete(invoke(<-c.jobs, clock))
		}
		if len(c.regs) != 0 || len(c.timers) != 0 || c.ready.Len() != 0 || c.status.ActiveSchedules != 0 {
			t.Fatal("retained registrations")
		}
	})
}
func TestTimeRangeValidation(t *testing.T) {
	if _, ok := addTime(math.MaxInt64, time.Nanosecond); ok {
		t.Fatal("overflow")
	}
	if _, ok := addTime(math.MinInt64, -time.Nanosecond); ok {
		t.Fatal("underflow")
	}
	if _, _, err := resolveSpec(Spec{Kind: FixedDelay, Every: time.Duration(math.MaxInt64)}, instant{mono: 1, wall: time.Now()}); !errors.Is(err, ErrInvalidSpec) {
		t.Fatal(err)
	}
}
