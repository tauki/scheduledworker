package scheduledworker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

var background = context.Background()

func newTestScheduler(t *testing.T, opts ...Option) *Scheduler {
	t.Helper()
	s, err := New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(background); err != nil {
		t.Fatal(err)
	}
	return s
}
func schedule(t *testing.T, s *Scheduler, h Handler, opts ...ScheduleOption) *Handle {
	t.Helper()
	handle, err := s.Schedule(background, h, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}
func result(t *testing.T, h *Handle) Result {
	t.Helper()
	r, err := h.Wait(background)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func shutdown(t *testing.T, s *Scheduler) {
	t.Helper()
	if err := s.Shutdown(background); err != nil {
		t.Fatal(err)
	}
}

func TestValidationAndLifecycle(t *testing.T) {
	for _, opts := range [][]Option{{WithWorkers(-1)}, {WithMaxSchedules(-1)}, {WithEventBuffer(-1)}, {WithWorkers(2), WithMaxSchedules(1)}, {nil}} {
		if _, err := New(opts...); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("options %v: %v", opts, err)
		}
	}
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if s.cfg.workers != 10 || s.cfg.maxSchedules != 10000 || s.Status().State != Created {
		t.Fatal("defaults")
	}
	noop := func(context.Context, RunInfo) error { return nil }
	if _, err = s.Schedule(background, noop); !errors.Is(err, ErrNotRunning) {
		t.Fatal(err)
	}
	if err = s.Start(nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(background)
	cancel()
	if err = s.Start(ctx); !errors.Is(err, context.Canceled) || s.Status().State != Created {
		t.Fatal(err)
	}
	shutdown(t, s)
	if err = s.Start(background); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if err = s.Shutdown(nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal(err)
	}
	invalid := [][]ScheduleOption{{WithOverlap(99)}, {WithOverlap(SkipOverlap)}, {WithMaxRuns(2)},
		{WithFixedRate(0)}, {WithFixedDelay(-1)}, {WithTimeout(-1)}, {nil},
		{WithRetry(RetryPolicy{MaxAttempts: -1})}, {WithRetry(RetryPolicy{InitialBackoff: 1})},
		{WithRetry(RetryPolicy{MaxAttempts: 2, InitialBackoff: time.Minute, MaxBackoff: time.Second})},
		{WithAt(time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC))},
		{WithAt(time.Now().Add(time.Hour)), WithUntil(time.Now())},
	}
	for _, opts := range invalid {
		if _, err = s.Schedule(background, noop, opts...); !errors.Is(err, ErrInvalidSpec) {
			t.Fatalf("options %v: %v", opts, err)
		}
	}
	if _, err = s.Schedule(nil, noop); !errors.Is(err, ErrInvalidArgument) {
		t.Fatal(err)
	}
	if _, err = s.Schedule(background, nil); !errors.Is(err, ErrInvalidSpec) {
		t.Fatal(err)
	}
}

func TestIndependentSlotsAndFutureAbandonment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestScheduler(t, WithWorkers(2))
		defer shutdown(t, s)
		release := make(chan struct{})
		started := make(chan struct{})
		a := schedule(t, s, func(context.Context, RunInfo) error { close(started); <-release; return nil })
		<-started
		b := schedule(t, s, func(context.Context, RunInfo) error { return nil })
		if r := result(t, b); r.LastRun.Outcome != Succeeded {
			t.Fatal(r)
		}
		future := schedule(t, s, func(context.Context, RunInfo) error { t.Error("future invoked"); return nil }, WithAt(time.Now().Add(30*24*time.Hour)))
		waitCtx, cancel := context.WithCancel(background)
		cancel()
		if _, err := a.Wait(waitCtx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		close(release)
		result(t, a)
		shutdown(t, s)
		if r := result(t, future); r.Reason != SchedulerStopped || r.Counters.RunsStarted != 0 {
			t.Fatal(r)
		}
		if s.Status().State != Stopped || s.Status().ActiveSchedules != 0 {
			t.Fatal(s.Status())
		}
	})
}

func TestNoEarlyDispatchAndFIFO(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestScheduler(t, WithWorkers(1))
		defer shutdown(t, s)
		at := time.Now().Add(time.Hour)
		order := make(chan int, 3)
		var handles []*Handle
		for i := 0; i < 3; i++ {
			handles = append(handles, schedule(t, s, func(context.Context, RunInfo) error { order <- i; return nil }, WithAt(at)))
		}
		time.Sleep(time.Hour - time.Nanosecond)
		synctest.Wait()
		if len(order) != 0 {
			t.Fatal("early dispatch")
		}
		time.Sleep(time.Nanosecond)
		for i, h := range handles {
			result(t, h)
			if got := <-order; got != i {
				t.Fatalf("order %d, want %d", got, i)
			}
		}
	})
}

func TestCancellationRetainsCapacityAndAcknowledgment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestScheduler(t, WithWorkers(1), WithMaxSchedules(1))
		defer shutdown(t, s)
		release := make(chan struct{})
		started := make(chan context.Context, 1)
		h := schedule(t, s, func(ctx context.Context, _ RunInfo) error { started <- ctx; <-release; return nil })
		ctx := <-started
		if err := h.Cancel(background); err != nil {
			t.Fatal(err)
		}
		if ctx.Err() != context.Canceled || h.Status().State != Canceling {
			t.Fatal(h.Status())
		}
		if _, err := s.Schedule(background, func(context.Context, RunInfo) error { return nil }); !errors.Is(err, ErrCapacity) {
			t.Fatal(err)
		}
		select {
		case <-h.Done():
			t.Fatal("capacity released before return")
		default:
		}
		close(release)
		r := result(t, h)
		if r.Reason != ExplicitlyCanceled || r.LastRun.Outcome != Canceled || !errors.Is(r.LastRun.Err, context.Canceled) {
			t.Fatal(r)
		}
		if h.owner.Load() != nil {
			t.Fatal("terminal handle retains scheduler")
		}
		if err := h.Cancel(background); err != nil {
			t.Fatal(err)
		}
		if got, err := h.Wait(ctx); err != nil || got.ID != h.ID() {
			t.Fatal(got, err)
		}
	})
}

func TestAdmissionContextOnlyAndCanceledTransaction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestScheduler(t, WithWorkers(1))
		defer shutdown(t, s)
		ctx, cancel := context.WithCancel(background)
		h, err := s.Schedule(ctx, func(context.Context, RunInfo) error { return nil }, WithAt(time.Now().Add(time.Hour)))
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		if _, err = s.Schedule(ctx, func(context.Context, RunInfo) error { t.Error("canceled admission ran"); return nil }); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if r := result(t, h); r.LastRun.Outcome != Succeeded {
			t.Fatal(r)
		}
	})
}

func TestShutdownEscalationAndLifetimeCancellation(t *testing.T) {
	for _, lifetime := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			parent, cancel := context.WithCancel(background)
			defer cancel()
			s, _ := New(WithWorkers(1))
			if err := s.Start(parent); err != nil {
				t.Fatal(err)
			}
			if err := s.Start(background); !errors.Is(err, ErrAlreadyStarted) {
				t.Fatal(err)
			}
			started := make(chan context.Context, 1)
			release := make(chan struct{})
			h := schedule(t, s, func(ctx context.Context, _ RunInfo) error { started <- ctx; <-release; return errors.New("returned") })
			execution := <-started
			wait := make(chan error, 1)
			go func() { wait <- s.Shutdown(background) }()
			synctest.Wait()
			if s.Status().State != Stopping || execution.Err() != nil {
				t.Fatal("graceful shutdown canceled execution")
			}
			if lifetime {
				cancel()
				synctest.Wait()
			} else {
				deadline, stop := context.WithTimeout(background, time.Second)
				defer stop()
				if err := s.Shutdown(deadline); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
				synctest.Wait()
			}
			if execution.Err() != context.Canceled {
				t.Fatal("escalation did not cancel")
			}
			select {
			case <-s.Done():
				t.Fatal("noncooperative handler lost")
			default:
			}
			close(release)
			if err := <-wait; err != nil {
				t.Fatal(err)
			}
			r := result(t, h)
			if r.Reason != SchedulerStopped || r.LastRun.Outcome != Canceled {
				t.Fatal(r)
			}
		})
	}
}

func TestConcurrentAdmissionCancellationAndStop(t *testing.T) {
	s := newTestScheduler(t, WithWorkers(4), WithMaxSchedules(50))
	var wg sync.WaitGroup
	var accepted, called atomic.Uint64
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithCancel(background)
			defer cancel()
			if i%3 == 0 {
				cancel()
			}
			h, err := s.Schedule(ctx, func(context.Context, RunInfo) error { called.Add(1); return nil })
			if err != nil {
				if h != nil {
					t.Error("error with committed handle")
				}
				return
			}
			accepted.Add(1)
			_ = h.Cancel(background)
			_, _ = h.Wait(background)
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); _ = s.Shutdown(background) }()
	wg.Wait()
	if got := s.Status(); got.Accepted != accepted.Load() || called.Load() > got.Counters.AttemptsDispatched || got.ActiveSchedules != 0 {
		t.Fatal(got, accepted.Load(), called.Load())
	}
}

func TestHandleCancelDuringGracefulShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestScheduler(t, WithWorkers(1))
		started := make(chan struct{})
		h := schedule(t, s, func(ctx context.Context, _ RunInfo) error { close(started); <-ctx.Done(); return nil })
		<-started
		stopped := make(chan error, 1)
		go func() { stopped <- s.Shutdown(background) }()
		synctest.Wait()
		if err := h.Cancel(background); err != nil {
			t.Fatal(err)
		}
		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
		if r := result(t, h); r.Reason != SchedulerStopped || r.LastRun.Outcome != Canceled {
			t.Fatal(r)
		}
	})
}

func TestExpiredShutdownPublishesStoppingImmediately(t *testing.T) {
	s := newTestScheduler(t, WithWorkers(1))
	started := make(chan struct{})
	release := make(chan struct{})
	h := schedule(t, s, func(context.Context, RunInfo) error { close(started); <-release; return nil })
	<-started
	ctx, cancel := context.WithCancel(background)
	cancel()
	if err := s.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if got := s.Status(); got.State != Stopping {
		t.Fatal("stale admission state", got)
	}
	close(release)
	result(t, h)
	shutdown(t, s)
}
