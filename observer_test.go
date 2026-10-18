package scheduledworker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestObserverBlockingDropsAndFinalCounters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		first := true
		s := newTestScheduler(t, Config{Workers: 1, EventBuffer: 1, Observer: func(context.Context, Event) {
			if first {
				first = false
				close(entered)
				<-release
			}
			panic("observer failure")
		}})
		h := schedule(t, s, Spec{}, func(context.Context, RunInfo) error { <-entered; return nil })
		<-entered
		if r := result(t, h); r.LastRun.Outcome != Succeeded {
			t.Fatal(r)
		}
		synctest.Wait()
		if s.Status().EventsDropped == 0 {
			t.Fatal("events did not drop", s.Status())
		}
		stopped := make(chan error, 1)
		go func() { stopped <- s.Shutdown(background) }()
		synctest.Wait()
		select {
		case <-s.Done():
			t.Fatal("observer not joined")
		default:
		}
		close(release)
		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
		if got := s.Status(); got.ObserverPanics != 2 || got.EventsDropped != 2 {
			t.Fatal(got)
		}
	})
}
func TestObserverEscalationDuringDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		calls := 0
		s := newTestScheduler(t, Config{Workers: 1, EventBuffer: 16, Observer: func(ctx context.Context, _ Event) {
			calls++
			if calls == 1 {
				close(entered)
				<-ctx.Done()
				<-release
			}
		}})
		h := schedule(t, s, Spec{}, func(context.Context, RunInfo) error { return nil })
		<-entered
		result(t, h)
		deadline, cancel := context.WithTimeout(background, time.Second)
		defer cancel()
		if err := s.Shutdown(deadline); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		synctest.Wait()
		if s.Status().State != Stopping {
			t.Fatal(s.Status())
		}
		close(release)
		shutdown(t, s)
		if calls != 1 || s.Status().EventsDropped != 3 {
			t.Fatal(calls, s.Status())
		}
	})
}
func TestObserverOrderAndCancellationDuringBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var kinds []EventKind
		s := newTestScheduler(t, Config{Workers: 1, Observer: func(_ context.Context, e Event) { kinds = append(kinds, e.Kind) }})
		s.jitter = func(n uint64) uint64 { return n - 1 }
		h := schedule(t, s, Spec{Retry: RetryPolicy{MaxAttempts: 2}}, func(context.Context, RunInfo) error { return errors.New("retry") })
		synctest.Wait()
		_ = h.Cancel(background)
		result(t, h)
		shutdown(t, s)
		want := []EventKind{RunStarted, AttemptFinished, RetryScheduled, RunFinished, ScheduleFinished}
		if len(kinds) != len(want) {
			t.Fatal(kinds)
		}
		for i := range want {
			if kinds[i] != want[i] {
				t.Fatal(kinds)
			}
		}
	})
}

func TestObserverPanicUpdatesStatusWhileIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		s := newTestScheduler(t, Config{Workers: 1, Observer: func(_ context.Context, event Event) {
			if event.Kind == ScheduleFinished {
				close(entered)
				<-release
				panic("observer failed after the coordinator became idle")
			}
		}})
		defer shutdown(t, s)
		h := schedule(t, s, Spec{}, func(context.Context, RunInfo) error { return nil })
		result(t, h)
		<-entered
		synctest.Wait()
		before := s.Status()
		if before.ActiveSchedules != 0 || !before.NextWakeAt.IsZero() || before.ObserverPanics != 0 {
			t.Fatalf("scheduler did not become idle before the panic: %+v", before)
		}

		close(release)
		synctest.Wait()
		if got := s.Status(); got.ObserverPanics != 1 {
			t.Fatalf("idle snapshot hid the recovered observer panic: %+v", got)
		}
	})
}

func TestObserverDiscardUpdatesStatusDuringStopping(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		observerEntered := make(chan struct{})
		observerRelease := make(chan struct{})
		workerEntered := make(chan struct{})
		workerRelease := make(chan struct{})
		s := newTestScheduler(t, Config{Workers: 2, EventBuffer: 8, Observer: func(ctx context.Context, _ Event) {
			close(observerEntered)
			<-ctx.Done()
			<-observerRelease
		}})
		defer shutdown(t, s)
		releaseObserver := sync.OnceFunc(func() { close(observerRelease) })
		releaseWorker := sync.OnceFunc(func() { close(workerRelease) })
		defer releaseWorker()
		defer releaseObserver()
		blocked := schedule(t, s, Spec{}, func(context.Context, RunInfo) error {
			close(workerEntered)
			<-workerRelease
			return nil
		})
		<-workerEntered
		<-observerEntered
		other := schedule(t, s, Spec{}, func(context.Context, RunInfo) error { return nil })
		result(t, other)
		synctest.Wait()
		ctx, cancel := context.WithCancel(background)
		cancel()
		if err := s.Shutdown(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		synctest.Wait()
		if got := s.Status(); got.State != Stopping || got.EventsDropped != 0 {
			t.Fatalf("unexpected snapshot before observer discard: %+v", got)
		}

		releaseObserver()
		synctest.Wait()
		if got := s.Status(); got.State != Stopping || got.RunningAttempts != 1 || got.EventsDropped != 4 {
			t.Fatalf("snapshot hid observer discards while waiting for a handler: %+v", got)
		}
		releaseWorker()
		result(t, blocked)
	})
}
