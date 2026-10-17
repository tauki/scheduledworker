// Package scheduledworker provides process-local one-shot, fixed-delay and fixed-rate
// function scheduling. It limits concurrent invocations and drains queued tasks
// on Stop. Queued work is held in memory and is lost when the process exits.
//
// Recurring functions run serially within their registration, with the next
// interval measured after completion or panic for fixed-delay work. Extended
// options add bounded retries, context-aware handlers, middleware and observation.
// Panics are recovered; optional hooks report failures and panics.
// Distinct registrations can run concurrently, even if they use the same Fn.
//
// Stop does not cancel Go functions or skip future queued tasks. Call it from the
// owning application; calling it inside one of its functions waits on itself.
// StopCtx can discard queued work and bound the caller's wait. Context cancellation
// is cooperative and never releases an execution slot before its function returns.
package scheduledworker
