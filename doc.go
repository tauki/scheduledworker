// Package scheduledworker provides process-local one-shot and fixed-delay
// function scheduling. It limits concurrent invocations and drains queued tasks
// on Stop. Queued work is held in memory and is lost when the process exits.
//
// Recurring functions run serially within their registration, with the next
// interval measured after completion or panic. Panics are recovered; applications
// that need logging or error reporting should wrap their functions explicitly.
// Distinct registrations can run concurrently, even if they use the same Fn.
//
// Stop does not cancel Go functions or skip future queued tasks. Call it from the
// owning application; calling it inside one of its functions waits on itself.
// New v2 guarantees and the preserved extension prototype are separate from v1.
package scheduledworker
