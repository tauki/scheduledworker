package main

import (
	"fmt"
	"sync"
	"time"

	sw "github.com/tauki/scheduledworker"
)

func main() {
	w := sw.New().Start()
	var done sync.WaitGroup
	done.Add(1)
	w.Submit(sw.Task{At: time.Now().Add(20 * time.Millisecond), Fn: func() {
		fmt.Println("one-shot task finished")
		done.Done()
	}})
	done.Wait()
	w.Stop()
}
