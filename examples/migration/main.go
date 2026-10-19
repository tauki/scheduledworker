package main

import (
	"context"
	"fmt"
	"time"

	v1 "github.com/tauki/scheduledworker"
	v2 "github.com/tauki/scheduledworker/v2"
)

// This compares deterministic test work only. Never register the same business
// side effect in two engines merely to compare behavior.
func main() {
	w := v1.New().Start()
	if err := w.SubmitWithOptions(v1.Task{Fn: func() {}}, v1.WithEvery(time.Millisecond), v1.WithRepeat(3)); err != nil {
		panic(err)
	}
	w.Stop() // drains the accepted first occurrence and prevents recurrence
	s, err := v2.New(v2.WithWorkers(1))
	if err != nil {
		panic(err)
	}
	if err = s.Start(context.Background()); err != nil {
		panic(err)
	}
	h, err := s.Schedule(context.Background(), func(context.Context, v2.RunInfo) error { return nil }, v2.WithFixedDelay(time.Millisecond), v2.WithMaxRuns(3))
	if err != nil {
		panic(err)
	}
	result, err := h.Wait(context.Background())
	if err != nil {
		panic(err)
	}
	if err = s.Shutdown(context.Background()); err != nil {
		panic(err)
	}
	fmt.Printf("v2 completed %d runs; v1 and v2 have separate shutdown contracts\n", result.Counters.RunsSucceeded)
}
