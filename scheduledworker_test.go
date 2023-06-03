package scheduledworker

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestScheduledWorker(t *testing.T) {
	scheduler := New().
		SetDuration(time.Microsecond).
		SetMaxWorker(1).
		Start()

	var wg sync.WaitGroup
	wg.Add(3)
	counter := int64(0)

	scheduler.Submit(Task{
		At: time.Now().Add(time.Microsecond),
		Fn: func() {
			if atomic.LoadInt64(&counter) != 0 {
				t.Errorf("counter should be 0")
			}
			atomic.AddInt64(&counter, 1)
			wg.Done()
		},
	})

	scheduler.Submit(Task{
		At: time.Now().Add(time.Microsecond * 20),
		Fn: func() {
			if atomic.LoadInt64(&counter) != 1 {
				t.Errorf("counter should be 1")
			}
			atomic.AddInt64(&counter, 1)
			wg.Done()
		},
	})

	scheduler.Submit(Task{
		At: time.Now().Add(time.Microsecond * 50),
		Fn: func() {
			if atomic.LoadInt64(&counter) != 2 {
				t.Errorf("counter should be 2")
			}
			atomic.AddInt64(&counter, 1)
			wg.Done()
		},
	})
	wg.Wait()

	scheduler.Stop()
	if counter != 3 {
		t.Errorf("expected: %d, got: %d", 3, counter)
	}
}

func TestScheduledWorker_Recursion(t *testing.T) {
	scheduler := New().SetDuration(time.Second).Start()
	var f1 func()
	f1 = func() {
		scheduler.Submit(Task{
			At: time.Now(),
			Fn: func() {
				t.Errorf("recursion detected after closing scheduler")
			},
		})
	}

	scheduler.Submit(Task{
		At: time.Now(),
		Fn: func() {
			scheduler.Submit(Task{
				At: time.Now(),
				Fn: func() {
					f1()
				},
			})
		},
	})

	scheduler.Stop()
}

func TestScheduledWorker_panic(t *testing.T) {
	scheduler := New().SetDuration(time.Nanosecond).Start()

	workDone := 0
	tc := make(chan bool, 2)

	scheduler.Submit(Task{
		At: time.Now(),
		Fn: func() {
			workDone++
			tc <- true
			panic("should be recovered")
		},
	}, Repeat(2))

	<-tc
	<-tc
	scheduler.Stop()
	if workDone != 2 {
		t.Errorf("expected: %d, got: %d", 1, workDone)
	}
}
