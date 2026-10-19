package scheduledworker_test

import (
	"context"
	"fmt"
	"time"

	sw "github.com/tauki/scheduledworker/v2"
)

func ExampleScheduler() {
	s, err := sw.New(sw.WithWorkers(4), sw.WithMaxSchedules(1000))
	if err != nil {
		panic(err)
	}
	if err = s.Start(context.Background()); err != nil {
		panic(err)
	}
	h, err := s.Schedule(context.Background(),
		func(ctx context.Context, r sw.RunInfo) error { return nil },
		sw.WithName("refresh"), sw.WithFixedDelay(time.Minute),
		sw.WithAt(time.Now()), sw.WithMaxRuns(1),
		sw.WithRetry(sw.RetryPolicy{MaxAttempts: 3}))
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
	fmt.Println(result.Counters.RunsSucceeded)
	// Output: 1
}
