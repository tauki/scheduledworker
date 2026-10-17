package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	sw "github.com/tauki/scheduledworker"
)

func run() error {
	w := sw.New().SetMaxWorker(2).Start()
	defer func() { _ = w.StopCtx(context.Background(), sw.Abort) }()
	var attempts, completed atomic.Int64
	done := make(chan struct{})
	err := w.SubmitWithOptions(sw.Task{FnErr: func(ctx context.Context) error {
		if attempts.Add(1) == 1 {
			return errors.New("transient failure")
		}
		return ctx.Err()
	}}, sw.WithEvery(5*time.Millisecond), sw.WithRepeat(2), sw.WithFixedRate(),
		sw.WithRetry(sw.RetryPolicy{MaxAttempts: 3, Backoff: sw.ExponentialBackoff(time.Millisecond, 2, 10*time.Millisecond, true)}),
		sw.WithTimeout(time.Second), sw.WithCancelOnStop(), sw.WithKeyLock("refresh"), sw.WithJitter(time.Millisecond),
		sw.WithLabels(map[string]string{"job": "refresh"}),
		sw.WithOnComplete(func(*sw.Task) {
			if completed.Add(1) == 2 {
				close(done)
			}
		}))
	if err != nil {
		return err
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		return errors.New("scheduler did not finish")
	}
	if err := w.StopCtx(context.Background(), sw.Drain); err != nil {
		return err
	}
	fmt.Printf("occurrences=%d attempts=%d\n", completed.Load(), attempts.Load())
	return nil
}
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
