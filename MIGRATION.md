# Migrating from v1 to v2

V2 requires Go 1.25 or newer. Follow the [installation instructions](README.md#installation) and change imports from `github.com/tauki/scheduledworker` to `github.com/tauki/scheduledworker/v2`. Both versions can coexist with separate scheduler instances; register each job with only one engine.

## Update construction and submission

- Replace `Worker` and `New().Start()` with `NewWithOptions(...)`, then `Start(ctx)`. Both return errors that must be checked. The concrete type is `*Scheduler`.
- Replace `SetMaxWorker` with `WithWorkers(n)` and set admission capacity with `WithMaxSchedules(n)`. Configuration is fixed at construction. Remove `SetDuration`; v2 needs no polling interval.
- Replace `Submit` / `SubmitWithOptions` with `ScheduleWithOptions(ctx, handler, ...)`. Handle admission errors, including `ErrCapacity`, and retain the returned handle for cancellation or results. V1 options cannot be reused directly.
- Replace `Task.Fn` / `Task.FnErr` with `Handler(context.Context, RunInfo) error`. Handlers must cooperate with cancellation.

```go
// V1: three occurrences, each with up to three attempts.
err := w.SubmitWithOptions(v1.Task{FnErr: refresh},
    v1.WithEvery(30*time.Second), v1.WithRepeat(3),
    v1.WithRetry(v1.RetryPolicy{MaxAttempts: 3}))

// V2: after constructing and starting s.
h, err := s.ScheduleWithOptions(ctx,
    func(ctx context.Context, _ v2.RunInfo) error { return refresh(ctx) },
    v2.WithFixedDelay(30*time.Second), v2.WithMaxRuns(3),
    v2.WithRetry(v2.RetryPolicy{MaxAttempts: 3}))
```

`New(Config)` and `Schedule(ctx, Spec, handler)` are also available for struct-based configuration. See the [quick start](README.md#quick-start) for complete startup, result handling, and shutdown.

## Translate scheduling intent

| V1 intent | V2 options |
| --- | --- |
| One-shot | Omit interval options; use `WithAt(at)` for an explicit time. |
| Unlimited interval (`RepeatForever` / `Forever`) | `WithFixedDelay(d)` with no run limit. |
| Finite interval, positive count `n` | `WithFixedDelay(d), WithMaxRuns(uint64(n))`; validate `n` before conversion. |
| Fixed rate | `WithFixedRate(d)`; choose `WithOverlap(SkipOverlap)` if missed ticks should be skipped while a run is open. The default is `BufferLatest`. |
| `Every(d), Repeat(0)` | One delayed one-shot with `WithAt(at)`; resolve `at` from an explicit time or admission time + `d`. Recurring `WithMaxRuns(0)` means unlimited. |
| `Repeat(n > 1)` without an interval | No equivalent; recurring intervals must be positive. |

V1 option order can change the intended count: `WithRepeat(0), WithEvery(d)` means unlimited, while the reverse means one delayed invocation. Translate the resolved policy. Resolve `Task.At` versus `Spec.At` explicitly.

Both versions delay the first interval run by the interval when `At` is zero. Use `WithAt(time.Now())` for an immediate first run. Failed runs count toward `WithMaxRuns`; retries belong to that run. Runs within one schedule never overlap.

## Update cancellation and shutdown

- The context passed to `Start` owns the scheduler lifetime. For graceful shutdown, start with a background context and call `Shutdown` with a fresh bounded context when termination is requested. A signal context passed directly to `Start` cancels dispatched work when signaled.
- The context passed to scheduling controls admission only. Replace v1 per-job parent-context ownership with explicit `Handle.Cancel(ctx)`. Canceling `Handle.Wait(ctx)` only ends the wait.
- Replace `Stop` / `StopCtx` with `Shutdown(ctx)`. Shutdown abandons undispatched work, including future jobs, and waits for dispatched attempts. It does not drain future due dates. Wait for finite handles first if those jobs must finish.
- Expiration of any shutdown caller cancels dispatched execution and observation. Handlers retain worker slots until they return; cancellation cannot forcibly stop them. Shutdown is permanent, and lifecycle waits must happen outside handlers and observers.

## Review retries and results

- `RetryPolicy.MaxAttempts` still includes the first attempt. Custom backoff callbacks become `InitialBackoff` and `MaxBackoff`, using capped exponential full jitter. Panics never retry; use `Permanent(err)` for non-retryable errors. Match `PanicError` from the v2 import; its `Stack` is a string.
- `WithUntil` now bounds nominal occurrence times inclusively, not retries. An occurrence scheduled at or before the cutoff may retry afterward. Use `WithTimeout` for a cooperative per-attempt limit; v1 absolute registration deadlines have no direct equivalent.
- A handler returning `nil` after its timeout is classified as `TimedOut`, rather than successful. Cancellation and timeout can take precedence over a returned error.
- `Handle.Wait` returns a final `Result` even when execution failed. Inspect `LastRun.Outcome` and `LastRun.Err`; a nil wait error does not mean the job succeeded. Use `errors.Is` with the v2 error sentinels.

## Check features and instrumentation

Keep jobs on v1 or redesign their policy if they require pause/resume, occurrence jitter, business-hour windows, keyed exclusion, custom queues, or scheduling middleware. Handler wrappers can change worker usage, run counts, and cancellation behavior.

Move labels and attributes into application-owned closures or instrumentation. Replace built-in health/status handlers and metrics collectors with adapters around `Scheduler.Status()` and `Handle.Status()`. Use snapshots for counters and final results; observer events are best effort and cannot replace reliable business hooks or cleanup.

Review dashboard units: `Accepted` counts registrations, not retry/recurrence enqueues. Attempt counters and run counters are separate; `AttemptsFailed` includes timeout and panic but excludes cancellation. See the [observability guide](README.md#observability) and [integration examples](README.md#integrations).

The [migration example and tests](examples/migration) demonstrate both APIs and compare interval defaults, shutdown, retries, and timeout outcomes.
