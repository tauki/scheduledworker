<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)"
            srcset="https://user-images.githubusercontent.com/12199227/174419794-a099417b-7447-4a60-a45e-d6f5d9d1a4a6.png"
            height="250" width="250">
    <source media="(prefers-color-scheme: light)"
            srcset="https://user-images.githubusercontent.com/12199227/174419793-2678aca5-fc77-4538-b53d-25fb71804f9a.png"
            height="250" width="250">
    <img alt="ScheduledWorker logo"
         src="https://user-images.githubusercontent.com/12199227/174419789-aa234491-5eae-4247-979a-300efec24e9d.png"
         height="250" width="250">
  </picture>
</p>

![main](https://github.com/tauki/scheduledworker/actions/workflows/main.yaml/badge.svg)
[![Go Reference](https://pkg.go.dev/badge/github.com/tauki/scheduledworker/v2.svg)](https://pkg.go.dev/github.com/tauki/scheduledworker/v2)

# ScheduledWorker v2

An embedded Go scheduler for one-shot, fixed-delay, and fixed-rate work, with bounded worker concurrency and admission capacity.

Requires Go 1.25 or newer. The core uses only the standard library. Applications own scheduler lifetime, dependencies, immutable business metadata, and idempotency. State is in memory; crashes lose work, and replicas do not coordinate.

## Installation

V2 is under development. To evaluate the development branch from an existing Go module:

```sh
go get github.com/tauki/scheduledworker/v2@feat/v2-scheduler
```

Use import path `github.com/tauki/scheduledworker/v2`. Release notes are published in [GitHub Releases](https://github.com/tauki/scheduledworker/releases).

## Quick start

Save this as `main.go` in your Go module and run `go run .`. It schedules one immediate run, waits for its result, and shuts down with a bounded wait.

```go
package main

import (
	"context"
	"fmt"
	"time"

	sw "github.com/tauki/scheduledworker/v2"
)

func main() {
	s, err := sw.New(sw.Config{Workers: 4, MaxSchedules: 1000})
	if err != nil {
		panic(err)
	}
	if err := s.Start(context.Background()); err != nil {
		panic(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			panic(err)
		}
	}()

	h, err := s.Schedule(context.Background(), sw.Spec{Name: "hello"},
		func(ctx context.Context, run sw.RunInfo) error {
			fmt.Println("hello")
			return nil
		})
	if err != nil {
		panic(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := h.Wait(ctx)
	if err != nil {
		panic(err)
	}
	if result.LastRun.Err != nil {
		panic(result.LastRun.Err)
	}
	fmt.Printf("successful runs: %d\n", result.Counters.RunsSucceeded)
}
```

The [package example](example_test.go) is also checked by `go test`. For application shutdown, use a bounded shutdown context; its expiration cancels all outstanding execution and observation. Shutdown does not execute undispatched future jobs.

## Choosing a schedule

A **schedule** is a registration retained by the scheduler. A **run** is one occurrence of that schedule. An **attempt** is one handler invocation; retries belong to the same run.

| Kind | First run when `At` is zero | Later runs |
| --- | --- | --- |
| `Once` (default) | Immediately eligible. | None. |
| `FixedDelay` | Eligible after `Every`. | Eligible `Every` after the previous run's final attempt returns. |
| `FixedRate` | Eligible after `Every`. | Follow the original interval anchor; missed ticks follow the overlap policy. |

Set `At: time.Now()` for an immediate first interval run. Recurring schedules need a positive `Every`; set `MaxRuns` to a positive count to finish naturally, or leave it zero for unlimited recurrence. Runs within one schedule never overlap, including retries. Separate schedules can run concurrently.

## Public API and defaults

`New(Config) (*Scheduler, error)` constructs an idle, concrete scheduler. `Start(ctx)` starts it once; that context governs its lifetime. `Schedule(ctx, Spec, Handler) (*Handle, error)` acknowledges admission. `Shutdown(ctx)` initiates terminal shutdown and waits; `Done()` closes after all owned goroutines exit. `Status()` returns a consistent snapshot.

A handle offers `ID`, `Cancel(ctx)`, `Status`, `Done`, and `Wait(ctx)`. `Wait` returns `(Result, nil)` for a finished schedule even if execution failed; inspect `LastRun.Outcome` and `LastRun.Err`. Canceling a wait cancels only that wait. An already-visible final result takes precedence over a canceled wait context. Cancellation after terminal completion is harmless. Handles retain final snapshots without retaining retired handler closures.

| Field | Contract |
| --- | --- |
| `Config.Workers`, `MaxSchedules` | Zero selects 10 and 10,000. Negative sizes and workers greater than capacity are invalid. Configuration is immutable. |
| `Observer`, `EventBuffer` | Nil disables the observer goroutine. Zero buffer with an observer selects 256. Negative size is invalid. |
| `Spec.Kind` | `Once` (zero), `FixedDelay`, or `FixedRate`. Unknown enum values are invalid. |
| `At`, `Every` | Zero At means immediate for Once, admission + Every for intervals. Explicit At sets the first nominal time; a past At is allowed. Recurring Every must be positive; Once Every must be zero. Unrepresentable elapsed-time arithmetic is rejected. |
| `Overlap` | `BufferLatest` by default; `SkipOverlap` is valid only with FixedRate. Neither permits self-overlap. |
| `MaxRuns` | Once accepts 0/1 and runs once. Recurring zero means unlimited; positive values count first dispatches, including failed runs. Retries and suppressed ticks do not count. |
| `Until` | Inclusive upper bound on nominal occurrence times. Reject a bound preceding the resolved first occurrence. An in-window occurrence can run late; retries can occur after Until. |
| `Timeout` | Per-attempt cooperative timeout; zero disables it, negative values are invalid. Timeout retains the slot until return, then classifies even a nil return as TimedOut. |
| `Retry.MaxAttempts` | Total attempts including the first; 0/1 means one attempt, negative values are invalid. |
| `InitialBackoff`, `MaxBackoff` | With retries enabled, defaults are 1s and 1m. Nonnegative, normalized maximum >= initial; nonzero backoff without retries is invalid. |
| Handler/context | Nil handler: `ErrInvalidSpec`; nil context: `ErrInvalidArgument`, with no mutation. |

Validation errors wrap `ErrInvalidConfig` or `ErrInvalidSpec` and name the field. Before startup admission returns `ErrNotRunning`; after stop begins it returns `ErrStopped`; exhausted admission capacity returns `ErrCapacity`. Repeated Start returns `ErrAlreadyStarted` while running, `ErrStopped` after stop begins. Invalid/canceled startup leaves Created unchanged.

### Context ownership

| Context | Effect of cancellation or expiration |
| --- | --- |
| `Start(ctx)` | Stops the scheduler and cancels dispatched work and observation. |
| `Schedule(ctx, ...)` | Controls admission only. After successful admission, use `Handle.Cancel` to cancel the schedule. |
| `Handle.Cancel(ctx)` | Controls the cancellation request. An acknowledged request prevents future dispatch and cancels dispatched work. |
| `Handle.Wait(ctx)` | Ends this caller's wait; the schedule continues. |
| `Shutdown(ctx)` | Ends this caller's wait and escalates shared cancellation for all dispatched work and observation. Cleanup continues. |

For graceful application shutdown, start with `context.Background()` and call `Shutdown` with a fresh bounded context when termination is requested. Passing a signal context directly to `Start` requests cancellation as soon as the signal arrives. Handlers and observers must cooperate with cancellation; a deadline cannot forcibly terminate Go code.

## Guarantees

### Resource bounds

The coordinator owns mutable registrations, an indexed deadline heap, and a FIFO ready list. Handler execution runs separately from scheduling. A running scheduler owns `Workers` worker goroutines, one coordinator, and optionally one observer dispatcher. There is no goroutine per registration, delayed retry, or tick. At most `MaxSchedules` registrations are retained, `Workers` attempts dispatched, and one open run plus one buffered occurrence per registration. Canceled registrations retain admission capacity until dispatched work actually returns. Each registration embeds at most two timer nodes and one ready entry; removal leaves no timer tombstones. Handler closures and errors can retain application memory beyond these bookkeeping bounds.

### Timing and overlap

An eligibility time is a lower bound, with no latency SLA. Explicit wall timestamps resolve once onto an elapsed-time timeline. Later wall-clock jumps change actual event/finish timestamps but do not retarget existing schedules, cutoffs, or nominal reporting. Equal eligibility uses admission order; simultaneous completion/tick ordering follows coordinator event order. Ready entries keep position when coalesced, while newly eligible retries join the tail. A dispatch invokes its handler at most once, but retries can repeat business effects.

Fixed delay calculates the next nominal time from the worker's terminal return time, after the final retry, for success, failure, timeout, or panic. A failed run never silently deletes recurrence. Fixed rate keeps its original anchor. While a run is open, BufferLatest keeps the latest due occurrence and counts replaced ones as coalesced; SkipOverlap discards and counts due ticks. Under global saturation, either policy retains the latest due tick and coalesces older ones. Refresh occurs immediately before dispatch. Missed-tick arithmetic is constant time. For a five-second schedule blocked from t=0 to t=16, BufferLatest keeps t=15 and coalesces t=5/t=10; SkipOverlap discards all three and next runs at t=20.

### Retries and outcomes

Retries preserve RunID, Number, and ScheduledAt, increment Attempt, and rejoin the common queue. After failed attempt a, full jitter chooses `[0, min(MaxBackoff, InitialBackoff*2^(a-1))]` with overflow-safe capping. `Permanent(err)` preserves `errors.Is`/`errors.As` and prevents retry; `Permanent(nil)` is nil. Error-chain inspection runs on the worker and retains its slot; application `As`/`Unwrap` methods must return, and their panics receive the same recovery as handler panics. Recovered panics expose `*PanicError`, capture at most 16 KiB of stack, and never retry. Outcome precedence at return: panic, control cancellation, timeout, returned error, success. Timeout/cancellation errors match the corresponding context error and preserve a distinct handler error. Canceling during backoff produces one canceled run result preserving the failed attempt error, without inventing another attempt.

`RunLimitReached` wins over `UntilReached` when both natural limits apply. A one-shot uses `OneShotFinished` for any execution outcome. The first acknowledged explicit cancellation or scheduler stop fixes the termination reason; later requests do not replace it. A finished result never changes. Finish reason and last-run outcome describe different facts.

### Admission and cancellation

Before command handoff, request cancellation can prevent admission/cancellation. After handoff the coordinator checks the context before committing, then always acknowledges. A returned registration error guarantees no registration was committed. Admission can succeed just after caller cancellation if commitment won that race. After Cancel acknowledges, no new attempt can dispatch; an earlier handoff may still enter with a canceled context.

### Shutdown

Shutdown rejects admission, prevents further handoffs, abandons undispatched work with `SchedulerStopped`, removes retry waits, and lets dispatched attempts return. Lifetime cancellation or any expired Shutdown context cancels all dispatched execution and observation. Cleanup continues after a waiting caller returns. Other Shutdown callers can keep waiting. A never-started scheduler stops directly. Stopped instances cannot restart. A noncooperative handler/observer can prevent Done from closing; no replacement goroutines hide that work. Do not wait for shutdown or the current handle inside its own handler/observer. `runtime.Goexit`, process exits, and unmanaged goroutine panics are outside execution guarantees.

## Observability

Use status snapshots for metrics and health checks, and observer events for diagnostic logs and traces.

| API | What it provides |
| --- | --- |
| `Scheduler.Status()` | Current lifecycle, active/ready/running counts, retry waits, and cumulative counters across schedules. |
| `Handle.Status()` | One schedule's state, pending times, current run, counters, and latest terminal run. |
| `Handle.Wait(ctx)` | The schedule's final result, including its finish reason and latest terminal run. |
| `Config.Observer` | An optional callback receiving run, attempt, retry, suppression, and schedule-finish events. Delivery is asynchronous and best effort. |

### Snapshots and counters

Scheduler and handle snapshots are individually consistent, but are not an atomic pair. `CurrentRun` persists through execution and retry waiting/ready states. `NextScheduledAt` is the earliest retained nominal occurrence, including a buffered one; it is distinct from retry eligibility. `NextAttemptAt` reports pending first/retry eligibility, including past eligibility in Ready; it is zero while executing. Ready retries contribute to ReadySchedules, not WaitingRetries. RunningAttempts includes every dispatched attempt until its completion is processed.

Counters distinguish runs from attempts. AttemptsFailed includes timeout/panic, excludes control cancellation. For example, one failed attempt followed by a successful retry produces one successful run, two dispatched attempts, and one failed attempt. Suppressed ticks count as occurrences; Accepted counts committed registrations. Rejected counts coordinator-rejected admission transactions; validation and cancellation before handoff are caller-local and do not enter that counter. Only the latest terminal run is retained.

### Observer events

Events are sequenced, bounded, asynchronous, and best effort; full buffers drop new events and increment EventsDropped. Suppressed ranges produce one event with a count. Observer panics are isolated and counted. Graceful shutdown drains retained events. Escalation cancels the observer context and discards queued/subsequent events, counting them as dropped; an already-entered callback may finish canceled. Authoritative counters/results survive event loss. Arbitrary error/panic objects remain application-owned and must be safe to inspect concurrently.

### Integrations

The core starts no HTTP server. [Structured logging and HTTP readiness](examples/logging/main.go), [Prometheus](examples/prometheus/main.go), and [OpenTelemetry](examples/otel/main.go) show application integration. Cumulative metrics come from snapshots. Names/IDs belong in logs/traces, not automatic unbounded metric labels. Integrations retain no per-registration registry. Telemetry modules have independent dependencies and currently require Go 1.26+.

## Migrating from v1

Read [MIGRATION.md](MIGRATION.md) before moving v1 jobs. V2 uses `github.com/tauki/scheduledworker/v2` and lives at the repository root. V1 maintenance continues on [`release/v1`](https://github.com/tauki/scheduledworker/tree/release/v1); [v1.0.0](https://github.com/tauki/scheduledworker/tree/v1.0.0) remains available at `github.com/tauki/scheduledworker`. Both major import paths can coexist in an application.

Pause/resume, business windows, occurrence jitter, keyed exclusion, custom queues/backoff, absolute registration deadlines, scheduling middleware, and reliable engine hooks have no v2.0 equivalent. Keep v1 for jobs needing those contracts.

## Development

### Tests and examples

From this directory:

```sh
go test ./...
go test -race ./... -count=1
go vet ./...
go test -run '^$' -fuzz '^FuzzCoordinatorTransitions$' -fuzztime 10s -parallel 2
go test -run '^$' -fuzz '^FuzzIndexedHeap$' -fuzztime 10s -parallel 2
go run -race ./examples/logging
```

Run `go test -race ./...`, `go vet ./...`, and `go run -race .` separately inside each migration/telemetry example module. Root-module commands exercise v2. CI covers Go 1.25, 1.26, and current stable; migration fixtures compare the root v2 module with the published v1.0.0 dependency. V1's own source and test suite remain on `release/v1`.

Tests use Go's `testing/synctest`, explicit barriers, a private wall/elapsed clock seam, heap properties, and fuzzed transition sequences. They cover noncooperative handlers/observers with explicit release, clock jumps, cancellation/admission races, shutdown escalation, retry fairness, exact overlap accounting, a billion missed ticks, resource bounds, terminal cleanup, and idle/saturation behavior. A clean race run is evidence for exercised paths, not proof of arbitrary application handler safety.

### Benchmarks

```sh
go test -run '^$' -bench . -benchtime=1x -count=1
```

Initial local benchmark sample: Apple M1 Pro, darwin/arm64, Go 1.25.10, ten workers, one iteration per workload. Simultaneous short-handler bursts at 10/1,000/10,000 registrations took 0.174/7.16/69.6ms for admission through shutdown, with mean handler-entry lag 0.054/5.50/54.1ms and total allocations 64.6KB/4.68MB/46.5MB. Retained future-registration bookkeeping measured approximately 2,546/1,718/1,702 bytes per registration after GC. The benchmark advances the private elapsed clock once after admission to make all jobs due together; lag includes admission time. Slow (100µs sleep), retry, and cancellation cases are also reproducible in `benchmarks_test.go`. These single samples include runtime noise and establish no throughput/latency SLA; rerun on the deployment toolchain and hardware.
