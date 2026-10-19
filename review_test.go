package scheduledworker

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"
)

func admitForReview(t *testing.T, c *coordinator, spec spec, h Handler) *Handle {
	t.Helper()
	spec, err := normalizeSpec(spec, h)
	if err != nil {
		t.Fatal(err)
	}
	cmd := command{ctx: background, spec: spec, handler: h, reply: make(chan response, 1)}
	c.command(cmd)
	reply := <-cmd.reply
	if reply.err != nil {
		t.Fatal(reply.err)
	}
	return reply.handle
}

func TestCompletionBeforeDelayedFixedRateTickProcessing(t *testing.T) {
	for _, policy := range []OverlapPolicy{BufferLatest, SkipOverlap} {
		t.Run(map[OverlapPolicy]string{BufferLatest: "buffer", SkipOverlap: "skip"}[policy], func(t *testing.T) {
			s, _ := New(WithWorkers(1))
			s.state = Running
			clock := &manualClock{}
			s.clock = clock
			c := newCoordinator(s, background)
			h := admitForReview(t, c, spec{Kind: FixedRate, At: clock.now().wall, Every: 5 * time.Second, MaxRuns: 2, Overlap: policy}, func(context.Context, RunInfo) error { clock.at = int64(16 * time.Second); return nil })
			c.due(heap.Pop(&c.timers).(*deadline), clock.now())
			c.dispatch(c.regs[h.ID()])
			done := invoke(<-c.jobs, clock)
			// The handler has returned, but the coordinator has not processed t=5,
			// t=10, or t=15. Deliver its completion before those timer entries.
			c.complete(done)
			for len(c.timers) > 0 && c.timers[0].at <= clock.at {
				c.due(heap.Pop(&c.timers).(*deadline), clock.now())
			}
			st := h.Status()
			if policy == SkipOverlap {
				if st.Counters.OccurrencesSkipped != 3 || st.State != Scheduled || !st.NextScheduledAt.Equal(time.Unix(0, int64(20*time.Second))) {
					t.Errorf("missed overlap ticks: %+v", st)
				}
			} else if st.Counters.OccurrencesCoalesced != 2 || st.State != Ready || !st.NextScheduledAt.Equal(time.Unix(0, int64(15*time.Second))) {
				t.Errorf("buffered ticks: %+v", st)
			}
			c.beginStop()
		})
	}
}

func TestTimeoutClassificationDoesNotDependOnTimerCallback(t *testing.T) {
	clock := &manualClock{}
	// Use dispatch to set timeout metadata, then advance only the private elapsed
	// clock inside the handler; the real context timer has not fired yet.
	s, _ := New(WithWorkers(1))
	s.state = Running
	s.clock = clock
	c := newCoordinator(s, background)
	h := admitForReview(t, c, spec{Timeout: time.Second}, func(context.Context, RunInfo) error { clock.at = int64(2 * time.Second); return nil })
	c.due(heap.Pop(&c.timers).(*deadline), clock.now())
	c.dispatch(c.regs[h.ID()])
	done := invoke(<-c.jobs, clock)
	if done.outcome != TimedOut || !errors.Is(done.err, context.DeadlineExceeded) {
		t.Fatalf("expired elapsed deadline: %+v", done)
	}
	c.complete(done)
}

type blockingAsError struct{ entered, release chan struct{} }

func (*blockingAsError) Error() string { return "blocked classification" }
func (e *blockingAsError) As(any) bool { close(e.entered); <-e.release; return false }

func TestApplicationErrorClassificationCannotBlockCoordinator(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestScheduler(t, WithWorkers(2))
		defer shutdown(t, s)
		e := &blockingAsError{entered: make(chan struct{}), release: make(chan struct{})}
		h := schedule(t, s, func(_ context.Context, r RunInfo) error {
			if r.Attempt == 1 {
				return e
			}
			return nil
		}, WithRetry(RetryPolicy{MaxAttempts: 2}))
		<-e.entered
		ctx, cancel := context.WithTimeout(background, time.Second)
		defer cancel()
		other, err := s.Schedule(ctx, func(context.Context, RunInfo) error { return nil })
		close(e.release)
		if err != nil {
			t.Fatalf("application error blocked unrelated admission: %v", err)
		}
		result(t, other)
		result(t, h)
	})
}

func TestImmediateStopDropsTerminationEventsBeforeDelivery(t *testing.T) {
	s, _ := New(WithWorkers(1))
	s.state = Running
	s.clock = &manualClock{}
	c := newCoordinator(s, background)
	c.obs = newEventDispatcher(func(context.Context, Event) { t.Error("immediate stop delivered an event") }, 16)
	for i := 0; i < 4; i++ {
		admitForReview(t, c, spec{At: time.Unix(0, int64(time.Hour))}, func(context.Context, RunInfo) error { return nil })
	}
	s.requestStop(true)
	c.observeStop()
	if c.obs.ctx.Err() != context.Canceled || len(c.obs.queue) != 0 || c.status.EventsDropped != 4 {
		t.Fatalf("abort delivery was not sealed before cleanup: queued=%d dropped=%d", len(c.obs.queue), c.status.EventsDropped)
	}
	c.obs.seal()
	c.obs.run()
}

type panickingErrorMethods struct{}

func (*panickingErrorMethods) Error() string { return "broken error methods" }
func (*panickingErrorMethods) Is(error) bool { panic("Is must not run in cancellation cleanup") }
func (*panickingErrorMethods) As(any) bool   { panic("As panic") }

func TestErrorMethodPanicsStayWithinWorkerRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestScheduler(t, WithWorkers(1))
		defer shutdown(t, s)
		h := schedule(t, s, func(context.Context, RunInfo) error { return &panickingErrorMethods{} }, WithRetry(RetryPolicy{MaxAttempts: 3}))
		r := result(t, h)
		var pe *PanicError
		if r.LastRun.Outcome != Panicked || r.Counters.AttemptsDispatched != 1 || !errors.As(r.LastRun.Err, &pe) {
			t.Fatal(r)
		}
		other := schedule(t, s, func(context.Context, RunInfo) error { return nil })
		result(t, other)
	})
	err := &panickingErrorMethods{}
	joined := joinContext(context.Canceled, err)
	var preserved *panickingErrorMethods
	if !errors.Is(joined, context.Canceled) || !errors.As(joined, &preserved) || preserved != err {
		t.Fatal("error context not preserved")
	}
}

func TestWrappedPermanentErrorsNeverRetry(t *testing.T) {
	for _, wrap := range []func(error) error{
		func(err error) error { return Permanent(err) },
		func(err error) error { return fmt.Errorf("outer: %w", Permanent(err)) },
		func(err error) error { return errors.Join(errors.New("other"), Permanent(err)) },
	} {
		synctest.Test(t, func(t *testing.T) {
			s := newTestScheduler(t, WithWorkers(1))
			defer shutdown(t, s)
			sentinel := errors.New("permanent")
			h := schedule(t, s, func(context.Context, RunInfo) error { return wrap(sentinel) }, WithRetry(RetryPolicy{MaxAttempts: 3}))
			r := result(t, h)
			if r.Counters.AttemptsDispatched != 1 || r.LastRun.Outcome != Failed || !errors.Is(r.LastRun.Err, sentinel) {
				t.Fatal(r)
			}
		})
	}
}

func TestFixedRateTickExactlyAtCompletionAllowsBothEventOrders(t *testing.T) {
	for _, tickFirst := range []bool{false, true} {
		s, _ := New(WithWorkers(1))
		s.state = Running
		clock := &manualClock{}
		s.clock = clock
		c := newCoordinator(s, background)
		h := admitForReview(t, c, spec{Kind: FixedRate, At: clock.now().wall, Every: 5 * time.Second, Overlap: SkipOverlap}, func(context.Context, RunInfo) error { clock.at = int64(5 * time.Second); return nil })
		c.due(heap.Pop(&c.timers).(*deadline), clock.now())
		c.dispatch(c.regs[h.ID()])
		done := invoke(<-c.jobs, clock)
		if tickFirst {
			c.due(heap.Pop(&c.timers).(*deadline), clock.now())
		}
		c.complete(done)
		if !tickFirst {
			c.due(heap.Pop(&c.timers).(*deadline), clock.now())
		}
		st := h.Status()
		if tickFirst {
			if st.State != Scheduled || st.Counters.OccurrencesSkipped != 1 {
				t.Fatal(st)
			}
		} else if st.State != Ready || st.Counters.OccurrencesSkipped != 0 {
			t.Fatal(st)
		}
		c.beginStop()
	}
}
