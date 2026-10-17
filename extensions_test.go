package scheduledworker

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func requireSubmit(t *testing.T, w Worker, task Task, opts ...Option) {
	t.Helper()
	if err := w.SubmitWithOptions(task, opts...); err != nil {
		t.Fatal(err)
	}
}
func waitIdle(t *testing.T, w Worker) {
	t.Helper()
	waitFor(t, func() bool { s := w.Status(); return s.QueueLen == 0 && s.InFlight == 0 })
}

func TestExtendedValidationAndClosedAdmission(t *testing.T) {
	for name, opts := range map[string][]Option{
		"zero interval": {WithEvery(0)}, "negative interval": {WithEvery(-1)},
		"negative repeat": {WithRepeat(-2)}, "unbounded immediate": {WithRepeat(Forever)},
		"negative attempts": {WithRetry(RetryPolicy{MaxAttempts: -1})},
		"negative timeout":  {WithTimeout(-1)}, "fixed rate without interval": {WithFixedRate()},
		"unknown mode":    {OptionFunc(func(s *Spec) { s.Mode = 42 })},
		"negative jitter": {WithJitter(-1)}, "invalid timezone": {WithBusinessHours("missing/zone", 9, 17)},
		"invalid hours": {WithBusinessHours("UTC", 17, 9)}, "empty key": {WithKeyLock("")},
	} {
		t.Run(name, func(t *testing.T) {
			w := New()
			defer w.Stop()
			if err := w.SubmitWithOptions(Task{Fn: func() {}}, opts...); !errors.Is(err, ErrInvalidTask) {
				t.Fatalf("error=%v", err)
			}
			if w.Status().QueueLen != 0 {
				t.Fatal("invalid task admitted")
			}
		})
	}
	w := New()
	if err := w.SubmitWithOptions(Task{}); !errors.Is(err, ErrInvalidTask) {
		t.Fatal(err)
	}
	if err := w.SubmitWithOptions(Task{Fn: func() {}, FnErr: func(context.Context) error { return nil }}); !errors.Is(err, ErrInvalidTask) {
		t.Fatal(err)
	}
	w.Stop()
	if err := w.SubmitWithOptions(Task{Fn: func() {}}, OptionFunc(func(*Spec) { t.Error("option ran after stop") })); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
}

func TestJitterAndMiddlewareCannotBypassRepeatOrUntil(t *testing.T) {
	for _, repeat := range []int{0, 1, 3} {
		w := New().Start()
		var calls, reschedules atomic.Int64
		requireSubmit(t, w, Task{Fn: func() { calls.Add(1) }}, WithRepeat(repeat), WithJitter(time.Millisecond),
			WithOnReschedule(func(ctx context.Context, task *Task, next Next) error {
				reschedules.Add(1)
				task.At = time.Time{} // cannot advance scheduler eligibility
				return next(ctx, task)
			}))
		want := int64(repeat)
		if want == 0 {
			want = 1
		}
		waitFor(t, func() bool { return calls.Load() == want && w.Status().InFlight == 0 && w.Status().QueueLen == 0 })
		w.Stop()
		if reschedules.Load() != want-1 {
			t.Fatalf("repeat=%d reschedules=%d", repeat, reschedules.Load())
		}
	}
	w := New().Start()
	defer w.Stop()
	var calls atomic.Int64
	at := time.Now()
	requireSubmit(t, w, Task{At: at, Fn: func() { calls.Add(1) }}, WithEvery(time.Hour), WithUntil(at), WithJitter(time.Millisecond), WithOnEnqueue(func(ctx context.Context, task *Task, next Next) error {
		task.At = task.At.Add(time.Nanosecond)
		return next(ctx, task)
	}))
	if w.Status().Scheduled != 0 || calls.Load() != 0 {
		t.Fatal("jitter bypassed initial cutoff")
	}
	requireSubmit(t, w, Task{At: at, Fn: func() { calls.Add(1) }}, WithEvery(time.Hour), WithUntil(at), WithOnReschedule(func(ctx context.Context, task *Task, next Next) error { task.At = at; return next(ctx, task) }))
	waitFor(t, func() bool { return calls.Load() == 1 && w.Status().InFlight == 0 })
	if w.Status().QueueLen != 0 {
		t.Fatal("reschedule bypassed Until")
	}
}

func TestRetriesCountAttemptsSeparatelyAndPreserveRecurrence(t *testing.T) {
	w := New().Start()
	defer w.Stop()
	var calls atomic.Int64
	var mu sync.Mutex
	var attempts, retries []int
	requireSubmit(t, w, Task{FnErr: func(context.Context) error { calls.Add(1); return errors.New("failure") }},
		WithRepeat(2), WithRetry(RetryPolicy{MaxAttempts: 3, Backoff: func(n int) time.Duration {
			mu.Lock()
			retries = append(retries, n)
			mu.Unlock()
			return 0
		}}), WithOnStart(func(task *Task) { mu.Lock(); attempts = append(attempts, task.Spec().Attempt); mu.Unlock() }))
	waitFor(t, func() bool { return calls.Load() == 6 && w.Status().InFlight == 0 && w.Status().QueueLen == 0 })
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(attempts, []int{1, 2, 3, 1, 2, 3}) || !reflect.DeepEqual(retries, []int{1, 2, 1, 2}) {
		t.Fatalf("attempts=%v retries=%v", attempts, retries)
	}
	if s := w.Status(); s.Failed != 6 || s.Started != 6 || s.Scheduled != 6 {
		t.Fatalf("status=%+v", s)
	}
}

func TestRetryWaitFreesSlotAndStopSuppressesRetry(t *testing.T) {
	w := New().SetMaxWorker(1).Start()
	failed := make(chan struct{})
	requireSubmit(t, w, Task{FnErr: func(context.Context) error { close(failed); return errors.New("failure") }}, WithRetry(RetryPolicy{MaxAttempts: 2, Backoff: func(int) time.Duration { return time.Hour }}))
	waitSignal(t, failed)
	waitFor(t, func() bool { s := w.Status(); return s.InFlight == 0 && s.QueueLen == 1 })
	other := make(chan struct{})
	w.Submit(Task{Fn: func() { close(other) }})
	waitSignal(t, other)
	if err := w.StopCtx(context.Background(), Abort); err != nil {
		t.Fatal(err)
	}
	if s := w.Status(); s.Started != 2 || s.Dropped != 1 {
		t.Fatalf("status=%+v", s)
	}
}

func TestPanicRetryAndHookIsolation(t *testing.T) {
	w := New().Start()
	defer w.Stop()
	var calls, panics, completions, runs atomic.Int64
	collector := MetricsFuncs{Panicked: func() { panics.Add(1) }, Run: func(time.Duration) { runs.Add(1) }}
	requireSubmit(t, w, Task{Fn: func() {
		if calls.Add(1) == 1 {
			panic("boom")
		}
	}},
		WithRetry(RetryPolicy{MaxAttempts: 2, RetryOnPanic: true}), WithMetrics(collector),
		WithOnPanic(func(*Task, any) { panic("broken hook") }), WithOnComplete(func(*Task) { completions.Add(1) }))
	waitFor(t, func() bool { return completions.Load() == 1 && w.Status().InFlight == 0 })
	if panics.Load() != 1 || runs.Load() != 2 || calls.Load() != 2 || w.Status().CallbackPanicked != 1 {
		t.Fatalf("status=%+v metrics panics=%d runs=%d", w.Status(), panics.Load(), runs.Load())
	}
}

func TestMiddlewareOrderDropsAndRepeatedNext(t *testing.T) {
	w := New().Start()
	defer w.Stop()
	var order []string
	var mu sync.Mutex
	appendOrder := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }
	wrap := func(name string) Interceptor {
		return func(ctx context.Context, t *Task, next Next) error {
			appendOrder(name + " before")
			err := next(ctx, t)
			appendOrder(name + " after")
			return err
		}
	}
	requireSubmit(t, w, Task{Fn: func() { appendOrder("handler") }}, WithOnRun(wrap("outer")), WithOnRun(wrap("inner")))
	waitFor(t, func() bool { return w.Status().Completed == 1 && w.Status().InFlight == 0 })
	mu.Lock()
	if !reflect.DeepEqual(order, []string{"outer before", "inner before", "handler", "inner after", "outer after"}) {
		t.Fatal(order)
	}
	mu.Unlock()
	var calls atomic.Int64
	twice := func(ctx context.Context, task *Task, next Next) error {
		_ = next(ctx, task)
		_ = next(ctx, task)
		return nil
	}
	if err := w.SubmitWithOptions(Task{Fn: func() { calls.Add(1) }}, WithOnEnqueue(twice)); !errors.Is(err, ErrNextCalledTwice) {
		t.Fatal(err)
	}
	seen := make(chan error, 1)
	requireSubmit(t, w, Task{Fn: func() { calls.Add(1) }}, WithOnRun(twice), WithOnError(func(_ *Task, err error) { seen <- err }))
	select {
	case err := <-seen:
		if !errors.Is(err, ErrNextCalledTwice) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("error hook missing")
	}
	waitIdle(t, w)
	if calls.Load() != 1 {
		t.Fatalf("handler ran %d times", calls.Load())
	}
	requireSubmit(t, w, Task{Fn: func() { t.Error("dropped task ran") }}, WithOnEnqueue(func(context.Context, *Task, Next) error { return nil }))
	waitIdle(t, w)
}

func TestMetadataSnapshotsAndMetricsComposeWithHooks(t *testing.T) {
	for _, metricsFirst := range []bool{false, true} {
		w := New()
		var completed, hook atomic.Int64
		labels := map[string]string{"kind": "original"}
		attrs := map[string]any{"kind": "original"}
		var retained *Spec
		options := []Option{WithLabels(labels), WithAttributes(attrs), OptionFunc(func(s *Spec) { retained = s })}
		metrics := WithMetrics(MetricsFuncs{Completed: func() { completed.Add(1) }})
		onComplete := WithOnComplete(func(task *Task) {
			snapshot := task.Spec()
			if snapshot.Labels["kind"] != "original" || snapshot.Attrs["kind"] != "original" {
				t.Error("configuration alias leaked")
			}
			snapshot.Labels["kind"] = "mutated"
			if task.Spec().Labels["kind"] != "original" {
				t.Error("snapshot modified live spec")
			}
			hook.Add(1)
		})
		if metricsFirst {
			options = append(options, metrics, onComplete)
		} else {
			options = append(options, onComplete, metrics)
		}
		requireSubmit(t, w, Task{Fn: func() {}}, options...)
		labels["kind"], attrs["kind"], retained.Labels["kind"] = "changed", "changed", "changed"
		w.Start()
		waitFor(t, func() bool { return hook.Load() == 1 && w.Status().InFlight == 0 })
		w.Stop()
		if completed.Load() != 1 {
			t.Fatalf("completion metric=%d", completed.Load())
		}
	}
}

func TestTimeoutIsCooperativeAndKeepsSlot(t *testing.T) {
	w := New().SetMaxWorker(1).Start()
	canceled, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer w.Stop()
	defer unblock.Do(func() { close(release) })
	requireSubmit(t, w, Task{FnErr: func(ctx context.Context) error { <-ctx.Done(); close(canceled); <-release; return ctx.Err() }}, WithTimeout(time.Millisecond))
	waitSignal(t, canceled)
	second := make(chan struct{})
	w.Submit(Task{Fn: func() { close(second) }})
	if s := w.Status(); s.InFlight != 1 || s.QueueLen != 1 {
		t.Fatalf("timeout freed running slot: %+v", s)
	}
	select {
	case <-second:
		t.Error("second started while timed-out handler still running")
	default:
	}
	unblock.Do(func() { close(release) })
	waitSignal(t, second)
}

func TestDeadlineAndParentContext(t *testing.T) {
	for _, earlyDeadline := range []bool{true, false} {
		w := New().Start()
		observed := make(chan error, 1)
		deadline := time.Now().Add(time.Hour)
		timeout := time.Millisecond
		if earlyDeadline {
			deadline = time.Now().Add(time.Millisecond)
			timeout = time.Hour
		}
		requireSubmit(t, w, Task{FnErr: func(ctx context.Context) error { <-ctx.Done(); observed <- ctx.Err(); return ctx.Err() }}, WithTimeout(timeout), WithDeadline(deadline))
		select {
		case err := <-observed:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("earlier bound ignored")
		}
		w.Stop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := New().Start()
	defer w.Stop()
	var calls atomic.Int64
	requireSubmit(t, w, Task{FnErr: func(ctx context.Context) error { calls.Add(1); return ctx.Err() }}, WithContext(ctx), WithRepeat(10), WithRetry(RetryPolicy{MaxAttempts: 10}))
	waitFor(t, func() bool { return calls.Load() == 1 && w.Status().InFlight == 0 })
	if w.Status().QueueLen != 0 {
		t.Fatal("canceled registration repeated")
	}
}

func TestStopCtxCanEscalateAndStillWaits(t *testing.T) {
	w := New().SetMaxWorker(1).Start()
	running, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer w.Stop()
	defer unblock.Do(func() { close(release) })
	w.Submit(Task{Fn: func() { close(running); <-release }})
	waitSignal(t, running)
	w.Submit(Task{At: time.Now().Add(time.Hour), Fn: func() { t.Error("aborted task ran") }})
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := w.StopCtx(ctx, Drain); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if s := w.Status(); s.QueueLen != 1 || s.Stopped {
		t.Fatalf("drain lost queued/running work: %+v", s)
	}
	aborted := make(chan struct{})
	go func() { _ = w.StopCtx(context.Background(), Abort); close(aborted) }()
	waitFor(t, func() bool { return w.Status().QueueLen == 0 })
	select {
	case <-aborted:
		t.Error("Abort returned with running handler")
	default:
	}
	unblock.Do(func() { close(release) })
	waitSignal(t, aborted)
	if err := w.StopCtx(context.Background(), Drain); err != nil {
		t.Fatal(err)
	}
	if s := w.Status(); !s.Stopped || s.InFlight != 0 || s.Dropped != 1 {
		t.Fatalf("status=%+v", s)
	}
}

func TestCancelOnStopAndPrefilledQueueRejected(t *testing.T) {
	w := New().Start()
	running := make(chan struct{})
	requireSubmit(t, w, Task{FnErr: func(ctx context.Context) error { close(running); <-ctx.Done(); return ctx.Err() }}, WithCancelOnStop(), WithRepeat(3))
	waitSignal(t, running)
	if err := w.StopCtx(context.Background(), Drain); err != nil {
		t.Fatal(err)
	}
	if s := w.Status(); s.Started != 1 || s.Failed != 1 {
		t.Fatalf("status=%+v", s)
	}
	q := NewPriorityQueue()
	q.Push(&Task{Fn: func() { t.Error("queue admitted") }})
	w.SetQueue(q)
	if w.Status().QueueLen != 0 || q.Len() != 1 {
		t.Fatal("queue replacement accepted after StopCtx")
	}
}

func TestKeyContentionDoesNotConsumeSlotsOrBlockOtherWork(t *testing.T) {
	w := New().SetMaxWorker(2).Start().(*worker)
	first, release, second, other := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer w.Stop()
	defer unblock.Do(func() { close(release) })
	requireSubmit(t, w, Task{Fn: func() { close(first); <-release; panic("release key on panic") }}, WithKeyLock("same"))
	waitSignal(t, first)
	requireSubmit(t, w, Task{Fn: func() { close(second) }}, WithKeyLock("same"))
	w.Submit(Task{Fn: func() { close(other) }})
	waitSignal(t, other)
	select {
	case <-second:
		t.Error("same-key task overlapped")
	default:
	}
	unblock.Do(func() { close(release) })
	waitSignal(t, second)
	waitIdle(t, w)
	w.Lock()
	keys := len(w.keys)
	w.Unlock()
	if keys != 0 {
		t.Fatalf("retained %d idle keys", keys)
	}
}

func TestFixedRateBuffersLatestAndFixedDelayUsesCompletion(t *testing.T) {
	origin := time.Unix(0, 0)
	for _, tc := range []struct {
		mode           ScheduleMode
		finished, next time.Duration
	}{
		{FixedDelay, 16 * time.Second, 26 * time.Second},
		{FixedRate, 16 * time.Second, 10 * time.Second},
		{FixedRate, 36 * time.Second, 30 * time.Second},
		{FixedRate, 10 * time.Second, 10 * time.Second},
		{FixedRate, time.Hour * 100000, time.Hour * 100000},
	} {
		w := New().(*worker)
		task := &Task{At: origin, spec: &Spec{Context: context.Background(), Every: 10 * time.Second, Repeat: Forever, Mode: tc.mode}, state: &taskState{remaining: Forever, attempt: 1, nominal: origin}}
		next := w.nextOccurrence(task, nil, false, origin.Add(tc.finished))
		if next == nil || !next.At.Equal(origin.Add(tc.next)) {
			t.Fatalf("mode=%d finished=%v next=%v", tc.mode, tc.finished, next)
		}
		w.Stop()
	}
}

func TestBusinessHoursAcrossDST(t *testing.T) {
	location, err := time.LoadLocation("Europe/Oslo")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		at      time.Time
		date    int
		elapsed time.Duration
	}{
		{time.Date(2026, 3, 28, 21, 0, 0, 0, location), 29, 11 * time.Hour},
		{time.Date(2026, 10, 24, 21, 0, 0, 0, location), 25, 13 * time.Hour},
	} {
		next := businessTime(tc.at, location, 9, 17)
		if next.Day() != tc.date || next.Hour() != 9 || next.Sub(tc.at) != tc.elapsed {
			t.Fatalf("at=%v next=%v elapsed=%v", tc.at, next, next.Sub(tc.at))
		}
	}
	w := New()
	defer w.Stop()
	at := time.Date(2026, 3, 28, 21, 0, 0, 0, location)
	requireSubmit(t, w, Task{At: at, Fn: func() {}}, WithBusinessHours("Europe/Oslo", 9, 17))
	if !w.Status().NextDue.Equal(businessTime(at, location, 9, 17)) {
		t.Fatal("business hours not applied at admission")
	}
}

func TestBackoffBoundsIncludingOverflow(t *testing.T) {
	next := ExponentialBackoff(time.Second, 2, 10*time.Second, false)
	for n, want := range map[int]time.Duration{0: time.Second, 1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 1000000: 10 * time.Second} {
		if got := next(n); got != want {
			t.Fatalf("n=%d delay=%v want=%v", n, got, want)
		}
	}
	jitter := ExponentialBackoff(time.Duration(math.MaxInt64/2), 2, time.Duration(math.MaxInt64), true)
	for i := 0; i < 100; i++ {
		if got := jitter(1000); got < 0 || got > time.Duration(math.MaxInt64) {
			t.Fatal(got)
		}
	}
}

func TestHealthStatusAndQueueObservations(t *testing.T) {
	w := New()
	health := NewHealthHandler(w)
	check := func(want int) {
		t.Helper()
		rr := httptest.NewRecorder()
		health.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
		if rr.Code != want {
			t.Fatalf("health=%d want=%d", rr.Code, want)
		}
	}
	check(http.StatusServiceUnavailable)
	var observations atomic.Int64
	w.AddQueueObserver(func(int) { _ = w.Status(); observations.Add(1) })
	w.Start()
	w.Submit(Task{At: time.Now().Add(time.Hour), Fn: func() { t.Error("aborted future task ran") }})
	check(http.StatusOK)
	rr := httptest.NewRecorder()
	NewStatusHandler(w).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "application/json" {
		t.Fatal(rr)
	}
	if err := w.StopCtx(context.Background(), Abort); err != nil {
		t.Fatal(err)
	}
	check(http.StatusServiceUnavailable)
	if observations.Load() < 3 {
		t.Fatal("queue mutations not observed")
	}
}

func TestStopWaitsForCompletionObservers(t *testing.T) {
	w := New().Start()
	running, release, observing, finishObservation, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var unblockHandler, unblockObserver sync.Once
	defer w.Stop()
	defer unblockObserver.Do(func() { close(finishObservation) })
	defer unblockHandler.Do(func() { close(release) })
	w.Submit(Task{Fn: func() { close(running); <-release }})
	waitSignal(t, running)
	var observed atomic.Bool
	w.AddQueueObserver(func(int) {
		if w.Status().Completed == 1 && observed.CompareAndSwap(false, true) {
			close(observing)
			<-finishObservation
		}
	})
	unblockHandler.Do(func() { close(release) })
	waitSignal(t, observing)
	go func() { w.Stop(); close(stopped) }()
	waitFor(t, func() bool { return w.Status().Stopping })
	select {
	case <-stopped:
		t.Error("Stop returned with completion observer running")
	default:
	}
	unblockObserver.Do(func() { close(finishObservation) })
	waitSignal(t, stopped)
}

func TestSamplerStopsWithContext(t *testing.T) {
	w := New()
	defer w.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	report, done := make(chan struct{}, 1), make(chan struct{})
	go func() {
		StartQueueLengthSampler(ctx, w, time.Millisecond, func(int) {
			select {
			case report <- struct{}{}:
			default:
			}
		})
		close(done)
	}()
	waitSignal(t, report)
	cancel()
	waitSignal(t, done)
}

func TestBrokenPoliciesAreReportedWithoutLosingFutureOccurrences(t *testing.T) {
	w := New().Start()
	defer w.Stop()
	var calls, policyErrors atomic.Int64
	requireSubmit(t, w, Task{FnErr: func(context.Context) error { calls.Add(1); return errors.New("handler") }}, WithRepeat(2),
		WithRetry(RetryPolicy{MaxAttempts: 2, Backoff: func(int) time.Duration { panic("backoff") }}),
		WithOnError(func(_ *Task, err error) {
			var p *PanicError
			if errors.As(err, &p) {
				policyErrors.Add(1)
			}
		}))
	waitFor(t, func() bool { return calls.Load() == 2 && w.Status().InFlight == 0 && w.Status().QueueLen == 0 })
	if policyErrors.Load() != 2 || w.Status().CallbackPanicked != 2 {
		t.Fatalf("policy errors=%d status=%+v", policyErrors.Load(), w.Status())
	}
}

func TestRescheduleErrorsAreObservable(t *testing.T) {
	w := New().Start()
	defer w.Stop()
	expected := errors.New("reschedule")
	observed := make(chan error, 1)
	requireSubmit(t, w, Task{Fn: func() {}}, WithRepeat(2), WithOnReschedule(func(context.Context, *Task, Next) error { return expected }), WithOnError(func(_ *Task, err error) { observed <- err }))
	select {
	case err := <-observed:
		if !errors.Is(err, expected) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("policy error not reported")
	}
	waitIdle(t, w)
	if s := w.Status(); s.Completed != 1 || s.Scheduled != 1 {
		t.Fatalf("status=%+v", s)
	}
}

func TestBusinessHoursWithMissingCivilHour(t *testing.T) {
	location, err := time.LoadLocation("Europe/Oslo")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 3, 29, 1, 30, 0, 0, location)
	if got := businessTime(at, location, 2, 4).In(location); got.Day() != 29 || got.Hour() != 3 || got.Minute() != 0 {
		t.Fatalf("partial missing window: %v", got)
	}
	if got := businessTime(at, location, 2, 3).In(location); got.Day() != 30 || got.Hour() != 2 {
		t.Fatalf("entire missing window: %v", got)
	}
}

func TestConcurrentExtendedAdmissionConfigurationAndShutdown(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		w := New().SetMaxWorker(3).Start().(*worker)
		var group sync.WaitGroup
		group.Add(4)
		go func() {
			defer group.Done()
			for n := 0; n < 30; n++ {
				err := w.SubmitWithOptions(Task{FnErr: func(ctx context.Context) error { return ctx.Err() }}, WithRepeat(2), WithRetry(RetryPolicy{MaxAttempts: 2}), WithKeyLock("shared"), WithCancelOnStop(), WithOnComplete(func(*Task) { _ = w.Status() }))
				if err != nil && !errors.Is(err, ErrStopped) {
					t.Errorf("admission: %v", err)
				}
			}
		}()
		go func() {
			defer group.Done()
			for n := 0; n < 30; n++ {
				w.SetQueue(NewPriorityQueue())
				w.SetMaxWorker(n % 4)
				w.SetDuration(time.Millisecond)
			}
		}()
		go func() {
			defer group.Done()
			if err := w.StopCtx(context.Background(), Drain); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer group.Done()
			if err := w.StopCtx(context.Background(), Abort); err != nil {
				t.Error(err)
			}
		}()
		group.Wait()
		if s := w.Status(); !s.Stopped || s.QueueLen != 0 || s.InFlight != 0 {
			t.Fatalf("incomplete shutdown: %+v", s)
		}
		w.Lock()
		retained := len(w.keys) + len(w.active)
		w.Unlock()
		if retained != 0 {
			t.Fatalf("retained %d execution resources", retained)
		}
	}
}

func TestRecurrenceWaitsForObserverCleanupAndRetainsCancellation(t *testing.T) {
	w := New().SetMaxWorker(3).Start()
	first, releaseFirst, observing, releaseObserver, second, releaseSecond, other := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var unblockFirst, unblockObserver, unblockSecond sync.Once
	defer w.Stop()
	defer unblockSecond.Do(func() { close(releaseSecond) })
	defer unblockObserver.Do(func() { close(releaseObserver) })
	defer unblockFirst.Do(func() { close(releaseFirst) })
	var calls atomic.Int64
	requireSubmit(t, w, Task{FnErr: func(ctx context.Context) error {
		if calls.Add(1) == 1 {
			close(first)
			<-releaseFirst
			return nil
		}
		close(second)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-releaseSecond:
			return nil
		}
	}}, WithRepeat(2), WithCancelOnStop())
	waitSignal(t, first)
	var observed atomic.Bool
	w.AddQueueObserver(func(length int) {
		if length == 1 && w.Status().Completed == 1 && observed.CompareAndSwap(false, true) {
			close(observing)
			<-releaseObserver
		}
	})
	unblockFirst.Do(func() { close(releaseFirst) })
	waitSignal(t, observing)
	w.Submit(Task{Fn: func() { close(other) }})
	waitSignal(t, other)
	if s := w.Status(); s.QueueLen != 1 {
		t.Errorf("next occurrence dispatched before previous cleanup: %+v", s)
	}
	unblockObserver.Do(func() { close(releaseObserver) })
	waitSignal(t, second)
	// Wait until the previous invocation has actually released its bookkeeping.
	waitFor(t, func() bool { return w.Status().InFlight == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := w.StopCtx(ctx, Drain); err != nil {
		t.Errorf("recurring cancellation was lost: %v", err)
	}
}
