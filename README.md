<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" 
            srcset="https://user-images.githubusercontent.com/12199227/174419794-a099417b-7447-4a60-a45e-d6f5d9d1a4a6.png"
            height="250" width="250">
    <source media="(prefers-color-scheme: light)" 
            srcset="https://user-images.githubusercontent.com/12199227/174419793-2678aca5-fc77-4538-b53d-25fb71804f9a.png"
            height="250" width="250">
    <img alt="logo" 
         src="https://user-images.githubusercontent.com/12199227/174419789-aa234491-5eae-4247-979a-300efec24e9d.png"
         height="250" width="250">
  </picture>
</p>

![main](https://github.com/tauki/scheduledworker/actions/workflows/main.yaml/badge.svg)
[![Go Reference](https://pkg.go.dev/badge/github.com/tauki/scheduledworker.svg)](https://pkg.go.dev/github.com/tauki/scheduledworker)
[![Go Report Card](https://goreportcard.com/badge/github.com/tauki/scheduledworker)](https://goreportcard.com/report/github.com/tauki/scheduledworker)

# ScheduledWorker

An embedded Go library for one-shot and fixed-delay function scheduling. Tasks live in memory, execute when eligible worker slots are available, and can repeat a finite number of times or until a cutoff.

This checkout is the v1.0.0 stabilization candidate. See [CHANGELOG.md](CHANGELOG.md) for changes from v0.9.0. It requires Go 1.20 or newer and has no external dependencies.

## Quick start

```go
package main

import (
    "fmt"
    "time"

    sw "github.com/tauki/scheduledworker"
)

func main() {
    w := sw.New().SetMaxWorker(4).Start()
    w.Submit(sw.Task{
        At: time.Now().Add(100 * time.Millisecond),
        Fn: func() { fmt.Println("work completed") },
    })
    w.Stop() // drains the scheduled task and waits for its function
}
```

New creates no goroutine or timer until Start. Tasks can be submitted before Start. Start is idempotent; a stopped worker cannot restart.

## Repeating work

```go
w.Submit(sw.Task{Fn: refresh}, sw.Every(time.Minute), sw.Repeat(3))
```

This waits one minute before the first execution, then waits one minute after each function completes. It executes three times in total if the worker is not stopped first. To run immediately before repeating, supply `At: time.Now()`.

- `Every(d)` requires d > 0 and implies unlimited repetition unless a nonzero Repeat value has already been supplied.
- `Repeat(n)` counts the first execution, including a recovered panic. Zero means the default one-shot behavior; `Forever` (-1) is the only supported negative count.
- `RepeatForever()` requires a positive Every interval. Finite repeats without Every execute again as soon as a slot is available.
- `Until(t)` is an inclusive bound on scheduled times, including the initial At. A zero time removes the bound. It does not terminate a function or impose a latest actual start time.
- Options apply left to right. Put `Repeat(n)` after `Every(d)` to make the finite count explicit.

Recurring executions of one submitted task never overlap. Independent submissions of the same function can overlap. v1 provides fixed-delay scheduling; fixed-rate and calendar policies are outside this API.

## Execution and shutdown guarantees

- At is the earliest eligibility time, not an execution deadline. Capacity, OS scheduling, and handler duration can delay execution. Equal deadlines have no guaranteed relative order.
- A zero At means immediately eligible, or now + Every when an interval is supplied.
- The default concurrency limit is ten. SetMaxWorker can change it safely at runtime; nonpositive values normalize to one. Lowering it does not interrupt existing invocations.
- Panics from Fn are recovered and repeat accounting continues. v1 has no automatic retry/error-reporting API; wrap functions to log or report failures explicitly. Functions must return or panic for their execution slots to be released.
- Stop immediately closes admission and prevents further repeats. It drains previously queued tasks at their scheduled times and waits for dispatched functions, so far-future tasks or nonreturning functions can delay it indefinitely.
- Stop before Start discards pending work and returns. Repeated/concurrent Stop calls wait for the same completion; later Submit calls are ignored. Nil functions are ignored.
- Call Stop from the owning application. Calling it from an executing function waits on itself. Functions should not wait for other work queued behind the same saturated worker.
- Tasks and queue memory are unbounded and process-local. Process termination loses queued work. Multiple processes do not coordinate their schedules.

SetDuration retains the idle wake interval setting (positive durations only). Scheduled deadlines and new submissions wake independently; it is not an interval added to every task's start latency.

## Custom queues

SetQueue replaces the queued-task implementation and transfers pending tasks. The built-in priority queue orders by Task.At. Its empty Pop/Peek return nil. Installed queues belong exclusively to the worker: do not mutate them directly while it is running. Queue methods must be prompt, internally consistent, and must not call worker methods. Prefilled tasks without options execute once.

## Examples and checks

```sh
go run ./examples/basic
go run ./examples/periodic
go test ./...
go test -race ./...
go vet ./...
```

The examples use only the standard library. Executable package examples also run with the test suite.
