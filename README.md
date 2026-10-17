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

An embedded Go library for one-shot, fixed-delay, and fixed-rate function scheduling. Tasks live in memory, execute when eligible worker slots are available, and can repeat a finite number of times or until a cutoff.

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

Recurring executions of one submitted task never overlap. Independent submissions of the same function can overlap. Legacy options provide fixed-delay scheduling. Extended options add fixed-rate execution and daily business-hour filters.

## Execution and shutdown guarantees

- At is the earliest eligibility time, not an execution deadline. Capacity, OS scheduling, and handler duration can delay execution. Equal deadlines have no guaranteed relative order.
- A zero At means immediately eligible, or now + Every when an interval is supplied.
- The default concurrency limit is ten. SetMaxWorker can change it safely at runtime; nonpositive values normalize to one. Lowering it does not interrupt existing invocations.
- Panics from Fn are recovered and repeat accounting continues. Extended options provide retries and error/panic hooks; legacy submissions do not retry automatically. Functions must return or panic for their execution slots to be released.
- Stop immediately closes admission and prevents further repeats. It drains previously queued tasks at their scheduled times and waits for dispatched functions, so far-future tasks or nonreturning functions can delay it indefinitely.
- Stop before Start discards pending work and returns. Repeated/concurrent Stop calls wait for the same completion; later Submit calls are ignored. Submissions without either handler are ignored.
- Call Stop from the owning application. Calling it from an executing function waits on itself. Functions should not wait for other work queued behind the same saturated worker.
- Tasks and queue memory are unbounded and process-local. Process termination loses queued work. Multiple processes do not coordinate their schedules.

SetDuration retains the idle wake interval setting (positive durations only). Scheduled deadlines and new submissions wake independently; it is not an interval added to every task's start latency.

## Custom queues

SetQueue replaces the queued-task implementation and transfers pending tasks. Replacements after Stop begins are ignored. The built-in priority queue orders by Task.At. Its empty Pop/Peek return nil; Push requires a non-nil task and panics on nil. Installed queues belong exclusively to the worker: do not mutate them directly while it is running. Queue methods must be prompt, internally consistent, and must not call worker methods. Prefilled tasks without options execute once.

## Examples and checks

```sh
go run ./examples/basic
go run ./examples/periodic
go test ./...
go test -race ./...
go vet ./...
```

The basic, periodic, and extended examples use only the standard library. Executable package examples also run with the test suite.

## Context-aware tasks and options

All features are available through `New()` and the `Worker` interface:

```go
w := scheduledworker.New().SetMaxWorker(4).Start()
err := w.SubmitWithOptions(scheduledworker.Task{
    FnErr: func(ctx context.Context) error {
        return refresh(ctx)
    },
},
    scheduledworker.WithEvery(time.Minute),
    scheduledworker.WithRepeat(3),
    scheduledworker.WithRetry(scheduledworker.RetryPolicy{
        MaxAttempts: 3,
        Backoff: scheduledworker.ExponentialBackoff(time.Second, 2, time.Minute, true),
    }),
    scheduledworker.WithTimeout(30*time.Second),
    scheduledworker.WithCancelOnStop(),
)
if err != nil { /* handle invalid configuration or closed admission */ }
```

`SubmitWithOptions` accepts exactly one of `Fn` and `FnErr`. It returns `ErrInvalidTask` for invalid settings and `ErrStopped` after shutdown starts. Enqueue middleware can drop a task; a drop or an initial time beyond `Until` returns nil without admission. `Task.At` takes precedence over `Spec.At`. A zero time uses now + Every. `OptionFunc` supports custom configuration, and options apply left to right. Configuration maps/slices are copied at admission; objects inside `Attrs` remain caller-owned and must be immutable or synchronized.

The legacy `Submit` also accepts `FnErr`, using a background context; if both handlers are present there, `FnErr` takes precedence. New code should use `SubmitWithOptions` for validation, deadlines and observation.

| Feature | API and guarantee |
| --- | --- |
| Recurrence | `WithEvery`, `WithRepeat`, `WithUntil`, `WithFixedDelay`, `WithFixedRate`. Repeat counts occurrences, including exhausted failures; retry attempts do not consume extra occurrences. Until bounds queued eligibility, including transformed times and retries. |
| Fixed rate | Uses the original time grid and never overlaps its own registration. After a slow occurrence, the latest missed tick is retained; earlier missed ticks are discarded. Finishing at t=36 for a 10-second schedule anchored at t=0 makes t=30 eligible next. |
| Retries | `MaxAttempts` includes the initial attempt; 0/1 disables retry. `Backoff` receives retry numbers starting at 1. A retry waits in the queue and uses the normal concurrency/key limits. Exhaustion permits the next recurring occurrence. Negative custom delays normalize to zero. A backoff panic is reported to OnError and exhausts the current occurrence. Handler panics retry only with `RetryOnPanic`. |
| Cancellation | `WithContext`, `WithTimeout`, `WithDeadline`, `WithCancelOnStop`. Timeout and deadline both apply; the earlier bound wins. Handlers must observe cancellation and return an error to report failure. A canceled parent context ends future recurrence/retries. |
| Hooks | `WithOnStart`, `WithOnComplete`, `WithOnError`, `WithOnPanic`. Start and outcome hooks run for each attempt with detached task snapshots; retained attempt metadata stays unchanged as retries advance. OnError also reports retry/reschedule policy errors. Hook/collector panics are isolated and counted by `Status.CallbackPanicked`. |
| Middleware | `WithOnEnqueue`, `WithOnRun`, `WithOnReschedule`. Registration order determines wrapping order. Call `next` synchronously zero or one times; repeated calls return `ErrNextCalledTwice`. Run middleware wraps the handler. Scheduling middleware may defer eligibility or drop work; it cannot bypass Repeat/Until or advance a time before the scheduler's eligibility. |
| Jitter | `WithJitter(d)` adds a random delay in [0,d) to initial and recurring eligibility. Zero disables jitter; negative values are invalid. Retry delays use their own backoff. |
| Business hours | `WithBusinessHours("Europe/Oslo", 9, 17)` defers eligibility into a daily window. Zones/hours are validated; calendar arithmetic respects DST using Go's civil-time resolution. Weekends are included. This filters eligibility rather than guaranteeing actual execution occurs inside the window. |
| Keyed exclusion | `WithKeyLock("customer-id")` serializes tasks sharing a key within one worker. Contention waits before dispatch, leaving slots free for other keys. Idle keys are removed. |
| Metadata | `WithLabels`, `WithAttributes`; hooks/middleware can read a defensive `task.Spec()` snapshot with the current attempt number. |
| Metrics | `WithMetrics(MetricsCollector)` and `MetricsFuncs` adapt application-owned backends without a core dependency. Collectors compose independently of hook option order. |

Middleware must return synchronously and must not retain its task or continuation. Callbacks run outside internal locks and may run concurrently. They must be prompt and must not call Stop or wait for work queued behind the same worker. Hook/collector time occupies an execution slot. A middleware panic is recovered; enqueue failures return an error, run failures follow the attempt policy, and reschedule failures end that registration. `runtime.Goexit`, `os.Exit`, and panics in application-created goroutines are outside the result/repetition guarantee.

## Shutdown and observation

`Stop()` retains its draining contract. Tasks with `WithCancelOnStop()` receive cancellation when shutdown starts, including already queued tasks when they later dispatch. Other handlers continue normally. Cancellation never kills a goroutine or frees its execution slot before it returns.

`StopCtx(ctx, Drain)` limits the caller's wait while keeping queued work. `StopCtx(ctx, Abort)` drops queued work and waits for dispatched functions and completion callbacks. A context timeout returns `ctx.Err()` while shutdown continues. Repeated calls wait for the same completion; Abort can escalate an earlier Drain. Neither method can restart a stopped worker. Calling Stop before Start still discards pending work.

`Status()` reports lifecycle, queue length, in-flight work, next eligibility time, and counters. `Scheduled` counts scheduler queue insertions, including repeats/retries; prefilled custom queues bypass that counter. Attempt counters distinguish successes, errors and panics. `AddQueueObserver` reports queue mutations outside locks; concurrent notifications can arrive out of order, so use `Status()` or `StartQueueLengthSampler` for a current gauge. The sampler blocks until its context is canceled, and the application owns any goroutine used for it.

`NewStatusHandler(w)` serves JSON. `NewHealthHandler(w)` returns 200 while started and accepting work, including when future tasks are queued or all slots are occupied; idle/stopping/stopped workers return 503. The application owns routing, access control and HTTP server lifetime.

## Integration examples

- [Combined options](examples/extended/main.go): finite fixed-rate recurrence, retries, jitter, keyed exclusion, cancellation and hooks. Run `go run ./examples/extended`.
- [Prometheus](examples/prometheus): counters, histograms, queue gauge and HTTP scrape handler.
- [OpenTelemetry](examples/otel): execution spans and SDK metrics.

The telemetry examples are separate modules with pinned SDK dependencies and Go 1.26 minimums. The library itself remains dependency-free and supports Go 1.20. Each integration has its own CI build, vet and executable smoke check.

```sh
(cd examples/prometheus && go run .)
(cd examples/otel && go run .)
```

The examples use the official [Prometheus Go client pattern](https://prometheus.io/docs/guides/go-application/) and [OpenTelemetry Go instrumentation APIs](https://opentelemetry.io/docs/languages/go/instrumentation/). Their programs finish after a demonstration rather than starting a persistent server.

For custom implementations of `Worker`, v1 adds `SubmitWithOptions`, `StopCtx`, `Status`, and `AddQueueObserver` to the interface. Existing callers keep the same constructor and fluent methods.
