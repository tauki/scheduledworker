package scheduledworker_test

import (
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
