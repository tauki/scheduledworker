package scheduledworker

import (
	"sync"
	"testing"
	"time"
)

func TestScheduledWorker(t *testing.T) {
	scheduler := New().SetDuration(time.Microsecond).Start()
	scheduler.Start()

	workDone := 0
	mu := sync.Mutex{}

	scheduler.Submit(Task{
		At: time.Now().Add(time.Microsecond),
		Fn: func() {
			mu.Lock()
			workDone++
			mu.Unlock()
		},
	})

	scheduler.Submit(Task{
		At: time.Now().Add(time.Microsecond * 20),
		Fn: func() {
			mu.Lock()
			workDone++
			mu.Unlock()
		},
	})

	scheduler.Submit(Task{
		At: time.Now().Add(time.Microsecond * 40),
		Fn: func() {
			mu.Lock()
			workDone++
			mu.Unlock()
		},
	})

	scheduler.Stop()
	if workDone != 3 {
		t.Errorf("expected: %d, got: %d", 3, workDone)
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
