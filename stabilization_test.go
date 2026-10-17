package scheduledworker

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerDispatchesWithAFreeSlot(t *testing.T) {
	w := New().SetMaxWorker(2).Start()
	first, release, second := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer w.Stop()
	defer close(release)
	w.Submit(Task{Fn: func() { close(first); <-release }})
	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("first task did not start")
	}
	w.Submit(Task{Fn: func() { close(second) }})
	select {
	case <-second:
	case <-time.After(2 * time.Second):
		t.Fatal("new task blocked despite an available worker slot")
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not make progress")
	}
}

func TestWorkerWakesForEarlierDeadline(t *testing.T) {
	w := New().SetDuration(time.Hour).Start()
	defer w.Stop()
	late, early := make(chan struct{}), make(chan struct{})
	w.Submit(Task{At: time.Now().Add(60 * time.Millisecond), Fn: func() { close(late) }})
	w.Submit(Task{Fn: func() { close(early) }})
	waitSignal(t, early)
	waitSignal(t, late)
}

func TestWorkerFirstRunAndFixedDelay(t *testing.T) {
	const interval = 20 * time.Millisecond
	w := New().Start()
	defer w.Stop()
	starts, finishes := make(chan time.Time, 2), make(chan time.Time, 2)
	before := time.Now()
	w.Submit(Task{Fn: func() {
		starts <- time.Now()
		finishes <- time.Now()
	}}, Every(interval), Repeat(2))
	var firstStart, firstFinish, secondStart time.Time
	select {
	case firstStart = <-starts:
	case <-time.After(2 * time.Second):
		t.Fatal("first run missing")
	}
	select {
	case firstFinish = <-finishes:
	case <-time.After(2 * time.Second):
		t.Fatal("first completion missing")
	}
	select {
	case secondStart = <-starts:
	case <-time.After(2 * time.Second):
		t.Fatal("repeat missing")
	}
	if firstStart.Before(before.Add(interval)) {
		t.Fatal("first execution ignored Every")
	}
	if secondStart.Before(firstFinish.Add(interval)) {
		t.Fatal("repeat interval was measured before completion")
	}
}

func TestWorkerNeverDispatchesEarly(t *testing.T) {
	w := New().Start()
	defer w.Stop()
	at := time.Now().Add(20 * time.Millisecond)
	ran := make(chan time.Time, 1)
	w.Submit(Task{At: at, Fn: func() { ran <- time.Now() }})
	select {
	case actual := <-ran:
		if actual.Before(at) {
			t.Fatalf("started %v early", at.Sub(actual))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scheduled task missing")
	}
}

func TestWorkerFiniteRepeatsFinishWithoutStop(t *testing.T) {
	w := New().Start().(*worker)
	var calls atomic.Int64
	w.Submit(Task{Fn: func() { calls.Add(1) }}, Repeat(3))
	waitFor(t, func() bool {
		w.Lock()
		defer w.Unlock()
		return calls.Load() == 3 && w.inFlight == 0 && w.queue.Len() == 0
	})
	w.Stop()
	if got := calls.Load(); got != 3 {
		t.Fatalf("executions = %d, want 3", got)
	}
}

func TestWorkerUntilIncludesBoundaryAndRejectsPastCutoff(t *testing.T) {
	w := New().Start()
	var calls atomic.Int64
	at := time.Now()
	w.Submit(Task{At: at, Fn: func() { calls.Add(1) }}, Until(at))
	w.Submit(Task{At: at.Add(time.Millisecond), Fn: func() { calls.Add(100) }}, Until(at))
	w.Stop()
	if got := calls.Load(); got != 1 {
		t.Fatalf("executions = %d, want boundary only", got)
	}
}

func TestWorkerQueueReplacementPreservesTasks(t *testing.T) {
	w := New()
	var calls atomic.Int64
	for i := 0; i < 10; i++ {
		w.Submit(Task{Fn: func() { calls.Add(1) }})
	}
	replacement := NewPriorityQueue()
	w.SetQueue(replacement)
	if replacement.Len() != 10 {
		t.Fatalf("transferred %d tasks", replacement.Len())
	}
	// Setting the already-owned queue must also terminate.
	replaced := make(chan struct{})
	go func() { w.SetQueue(replacement); close(replaced) }()
	waitSignal(t, replaced)
	w.Start().Stop()
	if got := calls.Load(); got != 10 {
		t.Fatalf("executions = %d", got)
	}
}

func TestWorkerQueueReplacementIgnoredDuringStop(t *testing.T) {
	w := New().SetMaxWorker(1).Start().(*worker)
	started, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer w.Stop()
	defer unblock.Do(func() { close(release) })
	w.Submit(Task{Fn: func() { close(started); <-release }})
	waitSignal(t, started)
	var accepted, rejected atomic.Int64
	w.Submit(Task{Fn: func() { accepted.Add(1) }})
	w.Lock()
	original := w.queue
	w.Unlock()
	go func() { w.Stop(); close(stopped) }()
	waitFor(t, func() bool { w.Lock(); defer w.Unlock(); return w.closed })

	replacement := NewPriorityQueue()
	replacement.Push(&Task{Fn: func() { rejected.Add(1) }})
	w.SetQueue(replacement)
	w.Lock()
	replaced := w.queue != original
	w.Unlock()
	if replaced {
		t.Error("queue replaced while Stop was draining")
	}
	unblock.Do(func() { close(release) })
	waitSignal(t, stopped)
	if accepted.Load() != 1 || rejected.Load() != 0 {
		t.Fatalf("executions: accepted=%d rejected=%d", accepted.Load(), rejected.Load())
	}
	if replacement.Len() != 1 {
		t.Fatal("rejected replacement queue was modified")
	}
}

func TestWorkerQueueReplacementIgnoredAfterStop(t *testing.T) {
	for _, started := range []bool{false, true} {
		name := "before Start"
		if started {
			name = "after Start"
		}
		t.Run(name, func(t *testing.T) {
			w := New().(*worker)
			if started {
				w.Start()
			}
			w.Stop()
			original := w.queue
			replacement := NewPriorityQueue()
			replacement.Push(&Task{Fn: func() { t.Error("task admitted after Stop") }})
			w.SetQueue(replacement)
			if w.queue != original || w.queue.Len() != 0 {
				t.Error("stopped worker accepted a replacement queue")
			}
			if replacement.Len() != 1 {
				t.Error("rejected replacement queue was modified")
			}
		})
	}
}

func TestWorkerIncreasingLimitWakesDispatcher(t *testing.T) {
	w := New().SetMaxWorker(1).Start()
	started, release, second := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer w.Stop()
	defer close(release)
	w.Submit(Task{Fn: func() { close(started); <-release }})
	waitSignal(t, started)
	w.Submit(Task{Fn: func() { close(second) }})
	w.SetMaxWorker(2)
	waitSignal(t, second)
}

func TestWorkerLimitBoundsConcurrentExecutions(t *testing.T) {
	const limit = 3
	w := New().SetMaxWorker(limit).Start().(*worker)
	release := make(chan struct{})
	defer w.Stop()
	defer close(release)
	var active, maximum atomic.Int64
	for i := 0; i < 20; i++ {
		w.Submit(Task{Fn: func() {
			n := active.Add(1)
			defer active.Add(-1)
			for old := maximum.Load(); n > old; old = maximum.Load() {
				if maximum.CompareAndSwap(old, n) {
					break
				}
			}
			<-release
		}})
	}
	waitFor(t, func() bool { return active.Load() == limit })
	w.Lock()
	inFlight, queued := w.inFlight, w.queue.Len()
	w.Unlock()
	if inFlight != limit || queued != 20-limit || maximum.Load() != limit {
		t.Fatalf("in flight=%d queued=%d maximum=%d", inFlight, queued, maximum.Load())
	}
}

func TestWorkerStopDrainsFutureWorkAndIsIdempotent(t *testing.T) {
	w := New().Start()
	ran := make(chan struct{})
	at := time.Now().Add(20 * time.Millisecond)
	w.Submit(Task{At: at, Fn: func() {
		if time.Now().Before(at) {
			t.Error("draining stop ran task early")
		}
		close(ran)
	}}, Every(time.Hour))
	w.Stop()
	waitSignal(t, ran)
	w.Stop()
	w.Start()
	w.Submit(Task{Fn: func() { t.Error("task admitted after stop") }})
}

func TestWorkerStopBeforeStartDiscardsPendingTasks(t *testing.T) {
	w := New()
	w.Submit(Task{At: time.Now().Add(time.Hour), Fn: func() { t.Error("task ran after stop-before-start") }})
	w.Stop()
	w.Start().Stop()
}

func TestWorkerStopWaitsForExecutingTask(t *testing.T) {
	w := New().Start().(*worker)
	started, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	w.Submit(Task{Fn: func() { close(started); <-release }})
	waitSignal(t, started)
	go func() { w.Stop(); close(stopped) }()
	waitFor(t, func() bool { w.Lock(); defer w.Unlock(); return w.closed })
	select {
	case <-stopped:
		t.Error("Stop returned before the function returned")
	default:
	}
	close(release)
	waitSignal(t, stopped)
}

func TestWorkerConcurrentLifecycleAndConfiguration(t *testing.T) {
	for i := 0; i < 40; i++ {
		w := New()
		var goroutines sync.WaitGroup
		goroutines.Add(4)
		go func() { defer goroutines.Done(); w.Start(); w.Start() }()
		go func() { defer goroutines.Done(); w.Stop(); w.Stop() }()
		go func() {
			defer goroutines.Done()
			for n := 0; n < 10; n++ {
				w.Submit(Task{Fn: func() {}})
			}
		}()
		go func() {
			defer goroutines.Done()
			for n := 0; n < 10; n++ {
				w.SetMaxWorker(n % 3)
				w.SetDuration(time.Duration(n+1) * time.Millisecond)
				w.SetQueue(NewPriorityQueue())
			}
		}()
		goroutines.Wait()
		w.Stop()
	}
}

func TestWorkerInvalidOptionsAndIgnoredInputs(t *testing.T) {
	for name, fn := range map[string]func(){
		"zero interval":              func() { Every(0) },
		"negative interval":          func() { Every(-time.Second) },
		"invalid repeat":             func() { Repeat(-2) },
		"zero duration":              func() { New().SetDuration(0) },
		"nil queue":                  func() { New().SetQueue(nil) },
		"typed nil queue":            func() { var q *PriorityQueue; New().SetQueue(q) },
		"unbounded immediate repeat": func() { New().Submit(Task{Fn: func() {}}, RepeatForever()) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected invalid argument panic")
				}
			}()
			fn()
		})
	}
	w := New().SetMaxWorker(0).Start()
	w.Submit(Task{}, nil)
	ran := make(chan struct{})
	w.Submit(Task{Fn: func() { close(ran) }}, nil)
	waitSignal(t, ran)
	w.Stop()
}

func TestWorkerPrefilledQueueTasksRunOnce(t *testing.T) {
	q := NewPriorityQueue()
	var calls atomic.Int64
	q.Push(&Task{Fn: func() { calls.Add(1) }})
	w := New().SetQueue(q).Start().(*worker)
	defer w.Stop()
	waitFor(t, func() bool {
		w.Lock()
		defer w.Unlock()
		return calls.Load() == 1 && w.inFlight == 0 && w.queue.Len() == 0
	})
}

func TestWorkerUntilStopsRecurrenceWithoutStop(t *testing.T) {
	w := New().Start().(*worker)
	defer w.Stop()
	var calls atomic.Int64
	at := time.Now()
	w.Submit(Task{At: at, Fn: func() { calls.Add(1) }}, Every(time.Hour), RepeatForever(), Until(at))
	waitFor(t, func() bool {
		w.Lock()
		defer w.Unlock()
		return calls.Load() == 1 && w.inFlight == 0 && w.queue.Len() == 0
	})
}

func TestWorkerLowerLimitWaitsForExistingExecutions(t *testing.T) {
	w := New().SetMaxWorker(2).Start().(*worker)
	first, second := make(chan struct{}), make(chan struct{})
	var releaseFirst, releaseSecond sync.Once
	defer w.Stop()
	defer releaseFirst.Do(func() { close(first) })
	defer releaseSecond.Do(func() { close(second) })
	started, third := make(chan struct{}, 2), make(chan struct{})
	w.Submit(Task{Fn: func() { started <- struct{}{}; <-first }})
	w.Submit(Task{Fn: func() { started <- struct{}{}; <-second }})
	waitSignal(t, started)
	waitSignal(t, started)
	w.SetMaxWorker(1)
	w.Submit(Task{Fn: func() { close(third) }})
	releaseFirst.Do(func() { close(first) })
	waitFor(t, func() bool { w.Lock(); defer w.Unlock(); return w.inFlight == 1 })
	select {
	case <-third:
		t.Error("new task started before execution count fell below the lower limit")
	default:
	}
	releaseSecond.Do(func() { close(second) })
	waitSignal(t, third)
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		runtime.Gosched()
	}
}
