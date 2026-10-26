package scheduledworker

import (
	"context"
	"errors"
	"math"
	"testing"
	"testing/synctest"
	"time"
)

func TestOutcomesAndErrorPreservation(t *testing.T) {
	sentinel := errors.New("business error")
	for _, kind := range []Outcome{Succeeded, Failed, TimedOut, Panicked, Canceled} {
		synctest.Test(t, func(t *testing.T) {
			s := newTestScheduler(t, WithWorkers(1))
			defer shutdown(t, s)
			started := make(chan struct{})
			release := make(chan struct{})
			h := schedule(t, s, func(ctx context.Context, _ RunInfo) error {
				close(started)
				switch kind {
				case Failed:
					return sentinel
				case Panicked:
					panic("boom")
				case TimedOut:
					<-ctx.Done()
					return sentinel
				case Canceled:
					<-release
					return sentinel
				}
				return nil
			}, WithTimeout(time.Second))
			<-started
			if kind == Canceled {
				_ = h.Cancel(background)
				close(release)
			}
			r := result(t, h)
			if r.LastRun.Outcome != kind {
				t.Fatalf("got %+v want %v", r, kind)
			}
			if kind == Failed || kind == TimedOut || kind == Canceled {
				if !errors.Is(r.LastRun.Err, sentinel) {
					t.Fatal(r)
				}
			}
			if kind == TimedOut && !errors.Is(r.LastRun.Err, context.DeadlineExceeded) {
				t.Fatal(r)
			}
			if kind == Canceled && !errors.Is(r.LastRun.Err, context.Canceled) {
				t.Fatal(r)
			}
			if kind == Panicked {
				var pe *PanicError
				if !errors.As(r.LastRun.Err, &pe) || len(pe.Stack) == 0 || len(pe.Stack) > 16<<10 {
					t.Fatal(r)
				}
			}
		})
	}
}
func TestTimeoutRetainsSlotAndNilReturnTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestScheduler(t, WithWorkers(1))
		defer shutdown(t, s)
		s.jitter = func(uint64) uint64 { return 0 }
		release := make(chan struct{})
		started := make(chan RunInfo, 2)
		h := schedule(t, s, func(_ context.Context, r RunInfo) error {
			started <- r
			if r.Attempt == 1 {
				<-release
			}
			return nil
		}, WithTimeout(time.Second), WithRetry(RetryPolicy{MaxAttempts: 2}))
		<-started
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if s.Status().RunningAttempts != 1 || h.Status().Counters.AttemptsDispatched != 1 {
			t.Fatal(s.Status(), h.Status())
		}
		b := schedule(t, s, func(context.Context, RunInfo) error { return nil })
		synctest.Wait()
		select {
		case <-b.Done():
			t.Fatal("timeout freed occupied slot")
		default:
		}
		close(release)
		second := <-started
		r := result(t, h)
		result(t, b)
		if second.Attempt != 2 || r.Counters.AttemptsTimedOut != 1 || r.Counters.RunsSucceeded != 1 {
			t.Fatal(second, r)
		}
	})
}
func TestPermanentPanicAndRetryWaitingCancellation(t *testing.T) {
	if Permanent(nil) != nil {
		t.Fatal("Permanent(nil)")
	}
	for _, mode := range []string{"permanent", "panic", "cancel"} {
		synctest.Test(t, func(t *testing.T) {
			s := newTestScheduler(t, WithWorkers(1))
			defer shutdown(t, s)
			s.jitter = func(n uint64) uint64 { return n - 1 }
			underlying := errors.New("underlying")
			h := schedule(t, s, func(context.Context, RunInfo) error {
				switch mode {
				case "permanent":
					return Permanent(underlying)
				case "panic":
					panic("no retry")
				}
				return underlying
			}, WithRetry(RetryPolicy{MaxAttempts: 3, InitialBackoff: time.Hour, MaxBackoff: time.Hour}))
			synctest.Wait()
			if mode == "cancel" {
				if h.Status().State != RetryWaiting || h.Status().CurrentRun.Attempt != 1 || s.Status().WaitingRetries != 1 {
					t.Fatal(h.Status(), s.Status())
				}
				_ = h.Cancel(background)
			}
			r := result(t, h)
			if r.Counters.AttemptsDispatched != 1 {
				t.Fatal(r)
			}
			if mode == "cancel" && (!errors.Is(r.LastRun.Err, context.Canceled) || !errors.Is(r.LastRun.Err, underlying) || r.Counters.RunsCanceled != 1) {
				t.Fatal(r)
			}
			if mode == "permanent" && !errors.Is(r.LastRun.Err, underlying) {
				t.Fatal(r)
			}
		})
	}
}
func TestRetryCapSaturates(t *testing.T) {
	r := RetryPolicy{InitialBackoff: time.Duration(math.MaxInt64/2 + 1), MaxBackoff: math.MaxInt64}
	if retryCap(r, 100) != math.MaxInt64 {
		t.Fatal("overflow")
	}
}
