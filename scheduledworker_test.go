package scheduledworker

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestScheduledWorker(t *testing.T) {
	scheduler := New().
		SetMaxWorker(1)
	anchor := time.Now().Add(-time.Second)

	var wg sync.WaitGroup
	wg.Add(3)
	counter := int64(0)

	scheduler.Submit(Task{
		At: anchor.Add(time.Nanosecond),
		Fn: func() {
			if atomic.LoadInt64(&counter) != 0 {
				t.Errorf("counter should be 0")
			}
			atomic.AddInt64(&counter, 1)
			wg.Done()
		},
	})

	scheduler.Submit(Task{
		At: anchor.Add(2 * time.Nanosecond),
		Fn: func() {
			if atomic.LoadInt64(&counter) != 1 {
				t.Errorf("counter should be 1")
			}
			atomic.AddInt64(&counter, 1)
			wg.Done()
		},
	})

	scheduler.Submit(Task{
		At: anchor.Add(3 * time.Nanosecond),
		Fn: func() {
			if atomic.LoadInt64(&counter) != 2 {
				t.Errorf("counter should be 2")
			}
			atomic.AddInt64(&counter, 1)
			wg.Done()
		},
	})
	scheduler.Start()
	wg.Wait()

	scheduler.Stop()
	if counter != 3 {
		t.Errorf("expected: %d, got: %d", 3, counter)
	}
}

func TestScheduledWorker_Recursion(t *testing.T) {
	w := New().Start().(*worker)
	started, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	w.Submit(Task{Fn: func() {
		close(started)
		<-release
		w.Submit(Task{Fn: func() { t.Error("recursive submission admitted during stop") }})
	}})
	waitSignal(t, started)
	go func() { w.Stop(); close(stopped) }()
	waitFor(t, func() bool { w.Lock(); defer w.Unlock(); return w.closed })
	close(release)
	waitSignal(t, stopped)
}

func TestScheduledWorker_panic(t *testing.T) {
	scheduler := New().SetMaxWorker(1).Start()

	workDone := 0
	tc := make(chan struct{}, 2)

	scheduler.Submit(Task{
		At: time.Now(),
		Fn: func() {
			workDone++
			tc <- struct{}{}
			panic("should be recovered")
		},
	}, Repeat(2))

	waitSignal(t, tc)
	waitSignal(t, tc)
	scheduler.Stop()
	if workDone != 2 {
		t.Errorf("expected: %d, got: %d", 2, workDone)
	}
}
