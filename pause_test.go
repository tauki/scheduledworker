package scheduledworker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPauseResumeBeforeStart(t *testing.T) {
	w := New()
	defer w.Stop()
	if w.Pause().Pause() != w || !w.Status().Paused || w.Status().Running {
		t.Fatal("Pause started the worker or failed to set the gate")
	}
	w.Resume().Resume()
	if s := w.Status(); s.Paused || s.Running {
		t.Fatalf("Resume started the worker: %+v", s)
	}
	w.Pause()
	ran := make(chan struct{}, 2)
	w.Submit(Task{Fn: func() { ran <- struct{}{} }})
	w.Start().Start()
	requireSubmit(t, w, Task{Fn: func() { ran <- struct{}{} }})
	if s := w.Status(); !s.Running || !s.Paused || s.QueueLen != 2 || s.InFlight != 0 {
		t.Fatalf("Start resumed a paused worker or lost submissions: %+v", s)
	}
	w.Resume().Resume()
	waitSignal(t, ran)
	waitSignal(t, ran)
	waitIdle(t, w)
	if s := w.Status(); s.Paused || s.Scheduled != 2 || s.Completed != 2 {
		t.Fatalf("status=%+v", s)
	}
}

func TestPauseResumePreservesEligibility(t *testing.T) {
	for _, offset := range []time.Duration{-time.Hour, 60 * time.Millisecond} {
		t.Run(offset.String(), func(t *testing.T) {
			w := New().Pause().Start()
			defer func() { _ = w.StopCtx(context.Background(), Abort) }()
			at := time.Now().Add(offset)
			ran := make(chan time.Time, 1)
			var observed time.Time
			requireSubmit(t, w, Task{At: at, Fn: func() { ran <- time.Now() }}, WithUntil(at), WithOnStart(func(task *Task) { observed = task.At }))
			w.Start() // Repeated Start must not implicitly resume.
			select {
			case <-ran:
				t.Fatal("task dispatched while paused")
			case <-time.After(20 * time.Millisecond):
			}
			if s := w.Status(); s.QueueLen != 1 || s.Started != 0 || s.Dropped != 0 {
				t.Fatalf("pause changed queued work: %+v", s)
			}
			w.Resume()
			select {
			case actual := <-ran:
				if actual.Before(at) || !observed.Equal(at) {
					t.Fatalf("eligibility changed: original=%v observed=%v actual=%v", at, observed, actual)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Resume failed to wake scheduled work")
			}
		})
	}
}

func TestPauseDoesNotCancelOrWaitForActiveWork(t *testing.T) {
	w := New().SetMaxWorker(2).Start()
	release, running := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer w.Stop()
	defer unblock.Do(func() { close(release) })
	result := make(chan error, 1)
	requireSubmit(t, w, Task{FnErr: func(ctx context.Context) error {
		close(running)
		<-release
		result <- ctx.Err()
		return ctx.Err()
	}}, WithCancelOnStop(), WithKeyLock("shared"))
	waitSignal(t, running)
	paused := make(chan struct{})
	go func() { w.Pause(); close(paused) }()
	waitSignal(t, paused)
	same, other := make(chan struct{}), make(chan struct{})
	requireSubmit(t, w, Task{Fn: func() { close(same) }}, WithKeyLock("shared"))
	requireSubmit(t, w, Task{Fn: func() { close(other) }}, WithKeyLock("other"))
	if s := w.Status(); s.InFlight != 1 || s.QueueLen != 2 {
		t.Fatalf("paused dispatch filled a free execution slot: %+v", s)
	}
	w.Resume()
	waitSignal(t, other)
	select {
	case <-same:
		t.Error("Resume bypassed same-key exclusion")
	default:
	}
	w.Pause()
	unblock.Do(func() { close(release) })
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Pause canceled the running attempt: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("running attempt failed to return while paused")
	}
	waitFor(t, func() bool { return w.Status().InFlight == 0 })
	if s := w.Status(); !s.Paused || s.QueueLen != 1 || s.Completed != 2 {
		t.Fatalf("pause lost or dispatched queued keyed work: %+v", s)
	}
	w.Resume()
	waitSignal(t, same)
	waitIdle(t, w)
}

func TestPauseFromStartHookAllowsDispatchedAttempt(t *testing.T) {
	w := New().SetMaxWorker(1).Start()
	paused, release, first, second := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer w.Stop()
	defer unblock.Do(func() { close(release) })
	requireSubmit(t, w, Task{Fn: func() { close(first) }}, WithOnStart(func(*Task) {
		w.Pause()
		close(paused)
		<-release
	}))
	waitSignal(t, paused) // Pause has returned, before the dispatched handler enters.
	w.Submit(Task{Fn: func() { close(second) }})
	unblock.Do(func() { close(release) })
	waitSignal(t, first)
	waitFor(t, func() bool { return w.Status().InFlight == 0 })
	if s := w.Status(); !s.Paused || s.QueueLen != 1 || s.Completed != 1 {
		t.Fatalf("pause did not preserve the dispatch boundary: %+v", s)
	}
	w.Resume()
	waitSignal(t, second)
	waitIdle(t, w)
}

func TestPauseFromHandlerPreservesRecurrence(t *testing.T) {
	for _, kind := range []string{"legacy", "fixed-delay", "fixed-rate"} {
		t.Run(kind, func(t *testing.T) {
			w := New().Start().(*worker)
			defer w.Stop()
			var calls atomic.Int64
			ran := make(chan struct{}, 2)
			fn := func() {
				if calls.Add(1) == 1 {
					w.Pause()
				}
				ran <- struct{}{}
			}
			if kind == "legacy" {
				w.Submit(Task{Fn: fn}, Every(time.Millisecond), Repeat(2))
			} else {
				mode := WithFixedDelay()
				if kind == "fixed-rate" {
					mode = WithFixedRate()
				}
				requireSubmit(t, w, Task{At: time.Now().Add(-time.Second), Fn: fn}, WithEvery(time.Millisecond), WithRepeat(2), mode)
			}
			waitSignal(t, ran)
			waitFor(t, func() bool { return w.Status().InFlight == 0 })
			if s := w.Status(); !s.Paused || s.QueueLen != 1 || s.Started != 1 || s.Completed != 1 {
				t.Fatalf("recurrence did not wait while paused: %+v", s)
			}
			w.Lock()
			nextAt := w.queue.Peek().At
			w.Unlock()
			w.Pause() // Does not reset the existing occurrence's eligibility.
			w.Lock()
			unchanged := w.queue.Peek().At.Equal(nextAt)
			w.Unlock()
			if !unchanged {
				t.Fatal("Pause changed the next occurrence")
			}
			w.Resume()
			waitSignal(t, ran)
			waitIdle(t, w)
			if calls.Load() != 2 || w.Status().Scheduled != 2 {
				t.Fatalf("recurrence count=%d status=%+v", calls.Load(), w.Status())
			}
		})
	}
}

func TestPauseFromHooksPreservesRetryAndCompletion(t *testing.T) {
	w := New().Start()
	defer w.Stop()
	var calls atomic.Int64
	attempts := make(chan int, 2)
	requireSubmit(t, w, Task{FnErr: func(context.Context) error {
		if calls.Add(1) == 1 {
			return errors.New("retry")
		}
		return nil
	}}, WithRetry(RetryPolicy{MaxAttempts: 2}), WithOnStart(func(task *Task) {
		attempts <- task.Spec().Attempt
	}), WithOnError(func(*Task, error) { w.Pause() }), WithOnComplete(func(*Task) { w.Pause() }))
	waitFor(t, func() bool { s := w.Status(); return s.Paused && s.InFlight == 0 })
	if s := w.Status(); s.QueueLen != 1 || s.Failed != 1 || s.Started != 1 {
		t.Fatalf("retry did not wait while paused: %+v", s)
	}
	w.Resume()
	waitFor(t, func() bool { s := w.Status(); return s.Paused && s.InFlight == 0 && s.Completed == 1 })
	if s := w.Status(); s.QueueLen != 0 || s.Started != 2 || calls.Load() != 2 {
		t.Fatalf("retry or completion was lost: %+v", s)
	}
	readAttempt := func() int {
		t.Helper()
		select {
		case n := <-attempts:
			return n
		case <-time.After(2 * time.Second):
			t.Fatal("attempt hook missing")
			return 0
		}
	}
	if first, second := readAttempt(), readAttempt(); first != 1 || second != 2 {
		t.Fatalf("attempts=%d,%d", first, second)
	}
}

func TestPauseShutdownOverridesGate(t *testing.T) {
	for _, mode := range []StopMode{Drain, Abort} {
		t.Run(map[StopMode]string{Drain: "drain", Abort: "abort"}[mode], func(t *testing.T) {
			w := New().Pause().Start()
			defer func() { _ = w.StopCtx(context.Background(), Abort) }()
			at := time.Now().Add(20 * time.Millisecond)
			if mode == Abort {
				at = time.Now().Add(time.Hour)
			}
			var calls atomic.Int64
			w.Submit(Task{At: at, Fn: func() {
				if time.Now().Before(at) {
					t.Error("draining paused work ran early")
				}
				calls.Add(1)
			}}, Every(time.Hour))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := w.StopCtx(ctx, mode); err != nil {
				t.Fatalf("shutdown remained blocked by pause: %v", err)
			}
			want := int64(1)
			if mode == Abort {
				want = 0
			}
			if s := w.Status(); s.Paused || !s.Stopped || s.Running || s.QueueLen != 0 || calls.Load() != want || s.Dropped != 1-want {
				t.Fatalf("calls=%d status=%+v", calls.Load(), s)
			}
			w.Pause().Resume().Start()
			if err := w.SubmitWithOptions(Task{Fn: func() { t.Error("restarted after shutdown") }}); !errors.Is(err, ErrStopped) {
				t.Fatalf("closed admission reopened: %v", err)
			}
			if w.Status().Paused {
				t.Fatal("Pause changed a stopped worker")
			}
		})
	}
}

func TestPauseStopBeforeStartDiscardsWork(t *testing.T) {
	w := New().Pause()
	w.Submit(Task{At: time.Now().Add(time.Hour), Fn: func() { t.Error("stopped task ran") }})
	w.Stop()
	w.Resume().Start().Pause().Stop()
	if s := w.Status(); !s.Stopped || s.Paused || s.Running || s.Dropped != 1 {
		t.Fatalf("status=%+v", s)
	}
}

func TestPauseResumeRacesWithShutdown(t *testing.T) {
	for _, mode := range []StopMode{Drain, Abort} {
		for iteration := 0; iteration < 20; iteration++ {
			w := New().SetMaxWorker(2).Pause().Start()
			var calls atomic.Int64
			for i := 0; i < 20; i++ {
				w.Submit(Task{Fn: func() { calls.Add(1) }})
			}
			start := make(chan struct{})
			var group sync.WaitGroup
			for i := 0; i < 3; i++ {
				group.Add(1)
				go func() {
					defer group.Done()
					<-start
					for j := 0; j < 30; j++ {
						w.Pause().Resume()
					}
				}()
			}
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := w.StopCtx(ctx, mode); err != nil {
					t.Errorf("shutdown mode=%d: %v", mode, err)
				}
			}()
			close(start)
			group.Wait()
			_ = w.StopCtx(context.Background(), Abort)
			if s := w.Status(); !s.Stopped || s.Paused || s.Running || s.InFlight != 0 || s.QueueLen != 0 || calls.Load()+s.Dropped != 20 {
				t.Fatalf("calls=%d status=%+v", calls.Load(), s)
			}
		}
	}
}

func TestPauseReadinessAndStatus(t *testing.T) {
	w := New()
	defer w.Stop()
	health := NewHealthHandler(w)
	check := func(want int) {
		t.Helper()
		rr := httptest.NewRecorder()
		health.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
		if rr.Code != want {
			t.Fatalf("readiness=%d want=%d status=%+v", rr.Code, want, w.Status())
		}
	}
	w.Pause().Resume()
	check(http.StatusServiceUnavailable)
	w.Start()
	check(http.StatusOK)
	w.Pause()
	check(http.StatusServiceUnavailable)
	rr := httptest.NewRecorder()
	NewStatusHandler(w).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/status", nil))
	var status Status
	if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Paused || !status.Running || status.Stopping || status.Stopped {
		t.Fatalf("paused lifecycle snapshot=%+v", status)
	}
	w.Resume()
	check(http.StatusOK)
	w.Stop()
	check(http.StatusServiceUnavailable)
}
