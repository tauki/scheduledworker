package main

import (
	"fmt"
	"sync"
	"time"

	sw "github.com/tauki/scheduledworker"
)

func main() {
	w := sw.New().SetMaxWorker(2).Start()
	var done sync.WaitGroup
	done.Add(3)
	n := 0
	w.Submit(sw.Task{At: time.Now(), Fn: func() {
		n++
		fmt.Println("run", n)
		done.Done()
	}}, sw.Every(20*time.Millisecond), sw.Repeat(3))
	done.Wait()
	w.Stop()
}
