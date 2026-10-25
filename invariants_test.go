package scheduledworker

import (
	"container/heap"
	"context"
	"errors"
	"math"
	"slices"
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
		s, _ := NewWithOptions(WithWorkers(1))
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
		h := schedule(t, s, func(_ context.Context, r RunInfo) error {
			times <- r.ScheduledAt
			if r.Number == 1 {
				close(started)
				<-release
			}
			return nil
		}, WithFixedDelay(10*time.Second), WithAt(anchor), WithUntil(anchor.Add(10*time.Second)))
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
		s, _ := NewWithOptions(WithWorkers(1))
		clock := &shiftingClock{clock: s.clock}
		s.clock = clock
		_ = s.Start(background)
		defer shutdown(t, s)
		release := make(chan struct{})
		started := make(chan struct{})
		a := schedule(t, s, func(context.Context, RunInfo) error { close(started); <-release; return nil })
		<-started
		b := schedule(t, s, func(context.Context, RunInfo) error { return nil })
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
		s := newTestScheduler(t, WithWorkers(1))
		defer shutdown(t, s)
		s.jitter = func(n uint64) uint64 { return n - 1 }
		order := make(chan string, 4)
		a := schedule(t, s, func(_ context.Context, r RunInfo) error {
			order <- "a"
			if r.Attempt == 1 {
				return errors.New("retry")
			}
			return nil
		}, WithRetry(RetryPolicy{MaxAttempts: 2, InitialBackoff: time.Second, MaxBackoff: time.Second}))
		synctest.Wait()
		release := make(chan struct{})
		started := make(chan struct{})
		block := schedule(t, s, func(context.Context, RunInfo) error { close(started); <-release; return nil })
		<-started
		b := schedule(t, s, func(context.Context, RunInfo) error { order <- "b"; return nil })
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
		s := newTestScheduler(t, WithWorkers(1))
		defer shutdown(t, s)
		s.jitter = func(n uint64) uint64 { return n - 1 }
		anchor := time.Now()
		h := schedule(t, s, func(_ context.Context, r RunInfo) error {
			if r.Attempt == 1 {
				return errors.New("retry")
			}
			return nil
		}, WithFixedRate(time.Second), WithAt(anchor.Add(10*time.Second)), WithMaxRuns(1), WithUntil(anchor.Add(12*time.Second)), WithRetry(RetryPolicy{MaxAttempts: 2, InitialBackoff: 5 * time.Second, MaxBackoff: 5 * time.Second}))
		r := result(t, h)
		if r.Reason != RunLimitReached || r.Counters.AttemptsDispatched != 2 || !r.LastRun.FinishedAt.Equal(anchor.Add(15*time.Second)) {
			t.Fatal(r)
		}
		release := make(chan struct{})
		started := make(chan struct{})
		x := schedule(t, s, func(context.Context, RunInfo) error { close(started); <-release; panic("panic wins cancellation") })
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
	f.Add([]byte{4, 1, 3, 0, 2, 8, 255, 0})
	f.Add([]byte{7, 5, 2, 5, 3, 5, 0, 5, 1, 5, 6, 5, 2, 0, 2, 5})
	f.Fuzz(func(t *testing.T, ops []byte) { checkHeapOperations(t, ops) })
}

func TestHeapFuzzExercisesPriorityTies(t *testing.T) {
	// Equal timestamps across registrations, and tick/retry ties within each
	// registration. Remove and reinsert a node to exercise index maintenance.
	got := checkHeapOperations(t, []byte{7, 5, 2, 5, 3, 5, 0, 5, 1, 5, 6, 5, 2, 0, 2, 5})
	want := []deadline{
		{at: 5, order: 0, index: -1}, {at: 5, order: 0, retry: true, index: -1},
		{at: 5, order: 1, index: -1}, {at: 5, order: 1, retry: true, index: -1},
		{at: 5, order: 3, index: -1}, {at: 5, order: 3, retry: true, index: -1},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("pop order: got %v, want %v", got, want)
	}
}

func checkHeapOperations(t *testing.T, ops []byte) []deadline {
	t.Helper()
	var h deadlineHeap
	nodes := make([]deadline, 16)
	for i := range nodes {
		nodes[i].index = -1
		// Each pair models the tick and retry nodes of one registration.
		nodes[i].order = uint64(i / 2)
		nodes[i].retry = i%2 != 0
	}
	// Decode node identity and time independently, so timestamps can tie.
	for i := 0; i+1 < len(ops) && i < 2000; i += 2 {
		d := &nodes[int(ops[i])%len(nodes)]
		if d.index >= 0 {
			heap.Remove(&h, d.index)
		} else {
			d.at = int64(int8(ops[i+1]))
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
	var popped []deadline
	var prev *deadline
	for len(h) > 0 {
		d := heap.Pop(&h).(*deadline)
		if d.index != -1 {
			t.Fatal("popped node still indexed")
		}
		if prev != nil && (d.at < prev.at || d.at == prev.at &&
			(d.order < prev.order || d.order == prev.order && !d.retry && prev.retry)) {
			t.Fatal("pop order")
		}
		popped = append(popped, *d)
		prev = d
	}
	return popped
}

type manualClock struct{ at int64 }

func (c *manualClock) now() instant { return instant{c.at, time.Unix(0, c.at)} }

// Exercise transitions without workers so every operation can verify the actual
// heap, ready list, registration retention, and attempt/run accounting.
func FuzzCoordinatorTransitions(f *testing.F) {
	f.Add([]byte{0, 0, 1, 2, 3, 4, 0, 7, 2, 1, 8, 9})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 2, 3, 4, 5, 6})
	f.Add([]byte{18, 2, 3})
	f.Add([]byte{0, 2, 3, 1, 2, 3})
	f.Add([]byte{36, 2, 3, 1, 2, 3})
	f.Add([]byte{0, 18, 4, 2, 3, 1, 2, 3})
	f.Fuzz(func(t *testing.T, ops []byte) { checkCoordinatorTransitions(t, ops) })
}

func TestCoordinatorFuzzExercisesOutcomes(t *testing.T) {
	for _, tt := range []struct {
		name string
		ops  []byte
		want Counters
	}{
		{"success", []byte{18, 2, 3}, Counters{RunsStarted: 1, RunsSucceeded: 1, AttemptsDispatched: 1}},
		{"failure", []byte{0, 2, 3, 1, 2, 3}, Counters{RunsStarted: 1, RunsFailed: 1, AttemptsDispatched: 2, AttemptsFailed: 2, RetriesScheduled: 1}},
		{"retry recovery", []byte{36, 2, 3, 1, 2, 3}, Counters{RunsStarted: 1, RunsSucceeded: 1, AttemptsDispatched: 2, AttemptsFailed: 1, RetriesScheduled: 1}},
		{"cancel oldest", []byte{0, 18, 4, 2, 3, 1, 2, 3}, Counters{RunsStarted: 1, RunsSucceeded: 1, AttemptsDispatched: 1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkCoordinatorTransitions(t, tt.ops); got != tt.want {
				t.Fatalf("counters: got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func checkCoordinatorTransitions(t *testing.T, ops []byte) Counters {
	t.Helper()
	s, _ := NewWithOptions(WithWorkers(2), WithMaxSchedules(8))
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
			arg := op / 6
			input := spec{Kind: Kind(arg % 3), Every: time.Duration(arg%3) * time.Second, MaxRuns: 3, Retry: RetryPolicy{MaxAttempts: 2}}
			if input.Kind == Once {
				input.MaxRuns = 1
			}
			spec, err := normalizeSpec(input, func(context.Context, RunInfo) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			// Outcome is independent of the opcode and schedule kind: always
			// fail, always succeed, or fail the first attempt then recover.
			behavior := arg / 3 % 3
			cmd := command{ctx: background, spec: spec, handler: func(_ context.Context, run RunInfo) error {
				if behavior == 0 || behavior == 2 && run.Attempt == 1 {
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
			// Map iteration must not change the meaning of a saved fuzz input.
			var oldest *registration
			for _, r := range c.regs {
				if oldest == nil || r.order < oldest.order {
					oldest = r
				}
			}
			if oldest != nil {
				c.cancel(oldest, ExplicitlyCanceled, true)
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
	return c.status.Counters
}
func TestTimeRangeValidation(t *testing.T) {
	if _, ok := addTime(math.MaxInt64, time.Nanosecond); ok {
		t.Fatal("overflow")
	}
	if _, ok := addTime(math.MinInt64, -time.Nanosecond); ok {
		t.Fatal("underflow")
	}
	if _, _, err := resolveSpec(spec{Kind: FixedDelay, Every: time.Duration(math.MaxInt64)}, instant{mono: 1, wall: time.Now()}); !errors.Is(err, ErrInvalidSpec) {
		t.Fatal(err)
	}
}
