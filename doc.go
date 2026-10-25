// Package scheduledworker is a bounded, process-local scheduler for one-shot,
// fixed-delay, and fixed-rate work. Construct with NewWithOptions, call Start,
// and retain registration Handles to cancel work or inspect terminal results.
//
// NewWithOptions and ScheduleWithOptions accept variadic options. Defaults are
// 10 workers, 10,000 schedule slots, and one immediate handler invocation.
// New(Config) and Schedule(ctx, Spec, Handler) accept structs instead. Both API
// styles apply the same defaults, validation, and scheduling policies.
//
// One coordinator owns schedule/run/attempt transitions. WithWorkers bounds
// dispatched attempts, WithMaxSchedules bounds retained registrations,
// and each registration has one open run, one pending occurrence, at most two
// indexed deadlines, and one ready-list entry. Retries never overlap their run.
// Construction starts no goroutine. Running uses the configured workers, one coordinator, and
// optionally one observer dispatcher; no goroutine is created per registration.
//
// The context passed to Schedule or ScheduleWithOptions controls admission only.
// Start's context owns the entire scheduler lifetime. Handle.Cancel prevents
// further dispatch and requests cancellation of dispatched work.
// Cancellation and timeouts are cooperative:
// a handler keeps its worker slot until it returns or panics. A dispatch invokes
// the handler at most once, but retries can repeat effects. Handlers requiring
// business deduplication must implement their own idempotency.
//
// Shutdown abandons undispatched work and waits for dispatched attempts and
// observation to finish. Expiration of any Shutdown caller escalates shared
// cancellation. Done closes only when all owned goroutines have exited. A
// handler or observer that ignores cancellation can prevent full termination.
// Do not wait for scheduler termination from one of its handlers/observers, or
// wait inside a handler for work queued behind the same saturated worker pool.
// runtime.Goexit, process exits, and unmanaged goroutine panics are outside the
// result guarantee.
//
// Timing is elapsed-time based, not calendar scheduling. Eligibility is a lower
// bound. Wall times are resolved once on admission; later clock adjustments do
// not retarget registrations. Fixed-rate missed ticks coalesce to the latest
// permitted tick. WithUntil bounds nominal times inclusively, not retries or actual
// execution times. WithMaxRuns counts first dispatches, including failed runs.
//
// Snapshots and final handle results are authoritative within the process.
// Observer events are ordered, asynchronous, bounded, and best effort. Never
// depend on observer delivery for business actions or registry cleanup. Errors
// and panic values may reference application objects; synchronize those objects
// before inspecting them concurrently. Schedulers must not be copied.
//
// There is no persistence, coordination between processes, public queue plugin,
// mutable configuration, pause/resume, cron, business windows, occurrence jitter,
// or keyed exclusion.
package scheduledworker
