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
// Pause suspends new dispatch while retaining queued work and accepting new
// submissions. Already-dispatched invocations continue. Resume permits dispatch
// at the original eligibility times; neither method starts a never-started worker.
// Start remains idempotent and does not implicitly resume a paused worker.
//
// Stop closes admission and prevents further repeats/retries, but drains work
// already queued at its scheduled times, overriding Pause. WithCancelOnStop
// requests cooperative context cancellation for opted-in tasks; other tasks
// continue normally.
// Call Stop or StopCtx from the owning application; calling either inside an
// executing function can wait on itself. StopCtx can discard queued work and
// bound the caller's wait. Context cancellation never kills a function or releases
// its execution slot before it returns. A context-aware handler must return an
// error to report cancellation or timeout as a failure.
package scheduledworker
