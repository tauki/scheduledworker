package scheduledworker_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	sw "github.com/tauki/scheduledworker"
)

func ExampleNew() {
	w := sw.New().Start()
	w.Submit(sw.Task{Fn: func() { fmt.Println("work completed") }})
	w.Stop()
	// Output: work completed
}

func ExampleEvery() {
	w := sw.New().Start()
	var done sync.WaitGroup
	done.Add(3)
	calls := 0
	w.Submit(sw.Task{Fn: func() { calls++; done.Done() }}, sw.Every(time.Millisecond), sw.Repeat(3))
	done.Wait()
	w.Stop()
	fmt.Println("executions:", calls)
	// Output: executions: 3
}

func Example_contextTask() {
	w := sw.New().Start()
	err := w.SubmitWithOptions(sw.Task{FnErr: func(ctx context.Context) error {
		_, hasDeadline := ctx.Deadline()
		fmt.Println("deadline:", hasDeadline)
		return nil
	}}, sw.WithTimeout(time.Second))
	if err != nil {
		panic(err)
	}
	w.Stop()
	// Output: deadline: true
}

func Example_pauseResume() {
	w := sw.New().Pause().Start()
	w.Submit(sw.Task{Fn: func() { fmt.Println("work completed") }})
	fmt.Println("paused queue:", w.Status().QueueLen)
	w.Resume()
	w.Stop()
	// Output:
	// paused queue: 1
	// work completed
}
