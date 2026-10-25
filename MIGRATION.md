# Migrating from v1.0.0 to v2

V1.0.0 remains available at `github.com/tauki/scheduledworker`, with v1 maintenance on `release/v1`. V2 is under development at the repository root, uses `github.com/tauki/scheduledworker/v2`, and requires Go 1.25+. For evaluation, replace `COMMIT_SHA` in `go get github.com/tauki/scheduledworker/v2@COMMIT_SHA` with a commit SHA from [PR #18](https://github.com/tauki/scheduledworker/pull/18), then update imports explicitly. The two import paths can coexist in an application with separate scheduler ownership. There is no transparent Worker facade or automatic converter over opaque v1 options.

The options-based examples below target v2.1.0. Existing v2.0.0 applications can keep `New(Config)` and `Schedule(ctx, Spec, handler)` unchanged; both APIs share the same engine and validation. The import path stays `/v2`.

The completed v1 already fixed batch blocking, recurrence loss after failure, interceptor repeat accounting, and future-work health reporting. Those regression cases inform v2 tests; they are not remaining v1 defects. V1 already counts total retry attempts, counts failed occurrences against finite recurrence, and defaults the first zero-At interval to admission + Every in both submission APIs.

## Construction and lifetime

| V1 | V2 / required action |
| --- | --- |
| `Worker`, `New()`, `Start()` | Concrete `*Scheduler`, `NewWithOptions(...Option)` and `Start(ctx)` with checked errors. NewWithOptions() selects defaults. Consumers define narrow interfaces. Repeated startup is an error. |
| `SetMaxWorker` | `WithWorkers(n)` at construction; immutable afterward. Invalid sizes return errors rather than normalizing. |
| `SetDuration` | Removed. One next-deadline timer drives scheduling. |
| `Submit(Task, ...TaskOpt)`, `SubmitWithOptions(Task, ...Option)` | `ScheduleWithOptions(ctx, handler, ...ScheduleOption)` after successful Start, with bounded capacity and acknowledged admission. An error guarantees no committed registration. |
| `Task.Fn`, `Task.FnErr` | One `Handler(context.Context, RunInfo) error`. Choose one deliberately; wrapping `func()` cannot make its work cancelable. Legacy FnErr precedence disappears. |
| `WithContext`, `Spec.Context` | Explicit application ownership and Handle.Cancel. Schedule's context controls admission only; it is not a per-registration parent. |
| `WithDeadline`, `Spec.Deadline` | No equivalent absolute registration deadline. Per-attempt Timeout or a handler-only wrapper changes outcome/retry/recurrence semantics. |
| `WithCancelOnStop` | No per-job flag. Graceful shutdown lets dispatched attempts return; lifetime cancellation or shutdown escalation cancels all dispatched contexts. |
| `Pause`, `Resume`, `Status.Paused` | Unavailable in v2.0. Shutdown is terminal; canceling/re-registering does not preserve paused identity, queued work, limits, or retries. Keep v1 for these jobs. |
| `Stop`, `StopCtx`, `StopMode`, `Drain`, `Abort` | `Shutdown(ctx)` abandons undispatched work and waits for dispatched attempts. Any expired caller escalates shared cancellation, rather than only limiting that caller's wait. |

Shutdown never waits for a future due date. An undispatched one-shot finishes with SchedulerStopped and no run outcome. A dispatched attempt records its actual outcome, while the schedule reason is SchedulerStopped. Waiting for a known finite batch before shutdown is application policy; it is not a general drain adapter. Unlimited recurring handles do not naturally finish.

Use a background lifetime context plus an application-owned bounded Shutdown when graceful termination is desired. Passing a signal context directly to Start requests immediate cancellation when the signal fires. Canceling an admission context after success does not cancel the registration. Canceling Wait only ends that wait. Handle.Cancel acknowledges removal of future dispatch, retains capacity until active work returns, and is safe to request inside a handler. Lifecycle waits belong outside handlers/observers.

Do not add a hidden watcher goroutine per job and claim the core's fixed-goroutine guarantee. A derived handler context alone does not terminate the registration. Applications requiring v1 parent/deadline semantics should keep those jobs on v1 until their ownership model is explicitly redesigned.

## Scheduling and counts

| Resolved v1 intent | V2 scheduling options |
| --- | --- |
| One-shot with Repeat 0 or 1 and no interval | Omit interval options; use WithAt for an explicit time. Zero/omitted time is immediate. |
| Unlimited positive interval | `WithFixedDelay(d)`; omit WithMaxRuns. |
| Finite positive interval, n > 0 | `WithFixedDelay(d), WithMaxRuns(uint64(n))` after validating n. |
| Fixed rate | `WithFixedRate(d)` with optional count; BufferLatest is default, WithOverlap(SkipOverlap) is an explicit alternative. |
| `Every(d), Repeat(0)` | One delayed Once; resolve intended At as admission + d when no explicit At exists. Recurring MaxRuns 0 would incorrectly mean unlimited. |
| `Repeat(n > 1)` without Every | Unsupported. V2 rejects recurring zero intervals. A handler loop or multiple one-shots changes identities, ordering, cancellation, retry, and accounting. |

V1 options apply left to right: `WithRepeat(0), WithEvery(d)` becomes unlimited, while the reverse order becomes one delayed invocation. Translate the final intent rather than copying option names/numeric values. `RepeatForever`/`Forever` (-1) maps to recurring MaxRuns 0; other negative counts are invalid. Task.At's precedence over Spec.At must be resolved explicitly when converting existing code. `Until` zero remains unbounded.

```go
// V1: final intent is three fixed-delay occurrences, each up to three attempts.
err := w.SubmitWithOptions(v1.Task{FnErr: refresh},
    v1.WithEvery(30*time.Second), v1.WithRepeat(3),
    v1.WithRetry(v1.RetryPolicy{MaxAttempts: 3}))

// V2.1: after checking NewWithOptions and Start errors.
h, err := s.ScheduleWithOptions(admissionCtx,
    func(ctx context.Context, run v2.RunInfo) error { return refresh(ctx) },
    v2.WithFixedDelay(30*time.Second), v2.WithMaxRuns(3),
    v2.WithRetry(v2.RetryPolicy{MaxAttempts: 3}))
```

V2 options apply left to right, and the last value for each setting wins. WithFixedDelay and WithFixedRate each replace the mode and interval. Validation happens after all options. Constructor Option and ScheduleOption are distinct; v1 option types cannot be passed to either.

Both count failed runs and keep retry attempts within one run. Positive limits bound actual first dispatches, not guaranteed executions before cancellation/shutdown. Use v2.WithAt(time.Now()) for an immediate first v2 interval; resolve v1 At explicitly as before.

Fixed delay starts after the final attempt returns, for every terminal outcome. Fixed rate anchors nominal times and never self-overlaps, including retry waiting. Under global saturation, v2 refreshes a ready occurrence to the latest eligible tick immediately before dispatch. Ready positions are preserved by coalescing; new eligibility and retries join the FIFO tail. V2 promises equal-eligibility admission order; v1 makes no equal-time ordering promise.

Until changes from v1 transformed/retry eligibility to an inclusive v2 nominal occurrence bound. With first nominal t=10, Until t=12, and retry eligibility t=15, v1 excludes the retry; v2 permits it within the attempt budget. Both can dispatch an already-eligible in-window occurrence after t=12 when a slot becomes available. V2 rejects initial nominal times beyond Until with ErrInvalidSpec; v1 can silently drop them (including extended submission returning nil). Neither cutoff is a latest-start/latest-finish deadline. Jitter/window transformations in v1 also affect eligibility before its cutoff checks.

## Execution outcomes

| V1 | V2 / change |
| --- | --- |
| `RetryPolicy`, `WithRetry`, `ExponentialBackoff` | Fixed-data RetryPolicy. MaxAttempts remains total attempts. Capped exponential full jitter replaces arbitrary factor/backoff callbacks, custom zero-delay policy, helper ±20% jitter, and optional panic retry. |
| Error classification | Ordinary errors retry within budget; `Permanent(err)` preserves the error chain and prevents retry. |
| `PanicError` | Match the type from the correct major import. Stack changes from `[]byte` to `string`, capped at 16 KiB. Panics never retry in v2. |
| `WithTimeout` | WithTimeout retains capacity until return; v2 records TimedOut even on nil return after expiry. V1's nil return counts success. |
| Task.At during retries, Spec.Attempt | RunInfo adds ScheduleID, stable RunID/Number/ScheduledAt, and Attempt. Retries do not overwrite nominal ScheduledAt. |
| Spec/NewSpec, Option/OptionFunc, Task.Spec | ScheduleWithOptions applies helpers before admission; Schedule accepts a copied Spec. RunInfo, snapshots and results describe immutable scheduler-owned state. No mutable live task/plugin surface. |
| `ErrStopped`, `ErrInvalidTask`, `ErrNextCalledTwice` | ErrStopped; separate ErrInvalidConfig/Spec/Argument plus capacity/lifecycle sentinels. Continuation misuse disappears with scheduling middleware. Use errors.Is. |

At return, panic wins over control cancellation, which wins over timeout, then returned error, then success. Context outcome errors match context.Canceled/DeadlineExceeded and preserve distinct handler errors. A nil-returning handler may have performed effects before being classified canceled/timed out. Cancellation during retry waiting preserves the previous attempt error in the canceled run result without a fictitious additional attempt. Retrying a timed-out noncooperative handler waits for its actual return first.

## Intentionally unavailable policies

These are accepted v2.0 gaps. Wrappers below do **not** reproduce their scheduling contracts.

| V1 surface | V2 disposition |
| --- | --- |
| `WithJitter` | No occurrence jitter. Retry jitter is a different policy. |
| `WithBusinessHours` | No daily timezone window, weekends/DST processing, or calendar scheduling. A handler skipping outside hours still consumes a run; a self-resubmitting chain changes registration/admission/identity. |
| `WithKeyLock` | No cross-registration key arbitration. A handler mutex occupies workers while waiting and can starve other keys. V1 arbitration happens before dispatch within one worker, not globally across processes. |
| Queue/PriorityQueue/NewPriorityQueue/SetQueue | No public queue plugin, transfer, prefilled queue, or custom ordering. |
| Interceptor/Next, WithOnEnqueue/WithOnReschedule | No scheduling-mutating middleware. A handler filter still consumes a dispatch/run. |
| WithLabels/WithAttributes, Spec.Labels/Attrs/Key | Immutable application-owned closure data and instrumentation. Name/IDs provide correlation, without engine-carried metadata maps. |
| Pause/Resume | No reversible dispatch gate. Retain v1 if required. |

## Hooks, metrics and adapters

`WithOnRun` can become an application handler decorator for synchronous invocation work. Reliable v1 start/completion/error/panic hooks and metrics callbacks have no transparent Observer equivalent: v2 events are lossy, and decorators cannot reliably see coordinator-final timeout/cancellation classification or scheduling policy decisions. Keep reliable business work in the handler with explicit errors/idempotency. Observers should log/sample; final results and snapshots own authoritative state.

| V1 signal/helper | V2 migration |
| --- | --- |
| Scheduled/IncScheduled | V1 queue insertions include retries/recurrence; v2 Accepted counts registrations. Rename/review dashboards. |
| Started/Completed/Failed/Panicked | V1 attempt units, with panic separate from failed. Use AttemptsDispatched/AttemptsFailed/AttemptsPanicked; v2 failed attempts include timeout/panic, exclude cancellation. RunsSucceeded/Failed/Canceled are separate terminal-run units. |
| QueueLen/NextDue | Distinct ActiveSchedules/ReadySchedules/WaitingRetries/BufferedSchedules/NextWakeAt. No promised reconstruction of v1 queue length. |
| InFlight | RunningAttempts, retained until actual return/completion processing. |
| Dropped/CallbackPanicked | Review abandonment reasons separately from EventsDropped/ObserverPanics; units and coverage differ. |
| MetricsCollector/MetricsFuncs/WithMetrics | Snapshot-driven adapters for cumulative counters/gauges; best-effort event histograms or invocation decorators with documented visibility. |
| AddQueueObserver/StartQueueLengthSampler | Poll snapshots under application lifecycle; no synchronous queue-mutation callback guarantee. |
| NewStatusHandler/NewHealthHandler | Application HTTP adapters around snapshots. Running with future jobs or a busy pool is normal; readiness follows lifecycle/admission. |
| Wait/run timings | Monotonic event Lag (first dispatch) and Duration (handler invocation); samples can drop. Do not subtract reported wall timestamps across clock adjustments. |

Names/IDs can be logged/traced, not automatically exported as unbounded metric labels. If an application adds a ScheduleID registry, bound its retention and clean up through authoritative handle ownership. A dropped ScheduleFinished event cannot be the only cleanup trigger. The first invocation/event can race the caller storing its handle; define missing correlation behavior. Included adapters use no per-ID registry and unregister their instrumentation explicitly.

## Executable evidence

The independent [migration module](examples/migration) imports both major versions with separate aliases and engines. Its deterministic fixtures cover first interval defaults in both v1 APIs, finite counts, ordered options, future-work draining versus abandonment, cutoff/retry divergence, and invalid immediate repeats. Core tests cover explicit first times, failures, exact t=10/12/15 retry behavior, fixed rate/SkipOverlap/saturation, timeout/panic/cancellation precedence, transaction acknowledgment, noncooperative work, observer loss, and resource cleanup.

Run `go test -race ./...`, `go vet ./...`, and `go run -race .` from that module. It uses the published v1.0.0 dependency and the local root v2 module; v1's own tests/examples remain on `release/v1`. Prometheus/OTel examples are independent modules with their own Go floor and dependency locks; the v2 core remains standard-library-only. No v2 publication or release is implied by these local source examples.
