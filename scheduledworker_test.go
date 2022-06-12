package scheduledworker

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
	require.Equal(t, 3, workDone)
}
