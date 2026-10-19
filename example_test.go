package scheduledworker_test

import (
	"context"
	"fmt"
	"time"

	sw "github.com/tauki/scheduledworker/v2"
)

func ExampleScheduler() {
	s, err := sw.New(sw.Config{Workers: 4, MaxSchedules: 1000})
	if err != nil {
		panic(err)
	}
	if err = s.Start(context.Background()); err != nil {
		panic(err)
	}
	h, err := s.Schedule(context.Background(), sw.Spec{Name: "refresh", Kind: sw.FixedDelay, At: time.Now(), Every: time.Minute, MaxRuns: 1, Retry: sw.RetryPolicy{MaxAttempts: 3}}, func(ctx context.Context, r sw.RunInfo) error { return nil })
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
