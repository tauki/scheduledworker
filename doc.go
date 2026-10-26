// Package scheduledworker provides bounded, in-memory scheduling for one-shot,
// fixed-delay, and fixed-rate work within one Go process.
//
// Construct with NewWithOptions or New, call Start, then register work with
// ScheduleWithOptions or Schedule. Defaults are 10 workers and 10,000 schedule
// slots; scheduling without options requests one immediate run. Retain Handles
// to cancel schedules or inspect results. Runs within a schedule never overlap,
// including retries. Handlers must implement any required business idempotency.
//
// Start's context owns the scheduler lifetime. Scheduling contexts control
// admission only; use Handle.Cancel to cancel accepted work. Cancellation and
// timeouts are cooperative: a handler retains its worker slot until it returns
// or panics.
//
// Shutdown abandons undispatched work and waits for dispatched attempts and
// observation. Expiration of any shutdown caller cancels outstanding execution
// and observation. Done closes when all owned goroutines exit; an uncooperative
// handler or observer can prevent termination. Lifecycle waits belong outside
// handlers and observers. Stopped schedulers cannot restart. Schedulers must not
// be copied.
//
// Status snapshots and final handle results are authoritative within the process.
// Observer events are asynchronous and best effort; use them for diagnostics,
// not reliable business actions or cleanup. Work is not persisted, and separate
// scheduler instances do not coordinate.
package scheduledworker
