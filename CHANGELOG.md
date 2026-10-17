# Changelog

## Unreleased — v1.0.0 stabilization candidate

No v1.0.0 tag or release has been published by this work.

### Fixed

- Dispatch newly due work when a slot is free instead of waiting for a whole batch to finish.
- Wake immediately for earlier submissions and worker-limit changes; use a deadline timer owned only by the scheduling loop.
- Serialize submission, configuration, completion, and stop state so shutdown cannot leave a late accepted task stranded.
- Make Start/Stop races and repeated Stop calls safe; Stop before Start discards pending tasks and returns.
- Measure repeat intervals from function completion, including recovered panics. Apply Until to initial and subsequent scheduled times.
- Correct zero-At first-run interval arithmetic and include tasks whose deadline equals now.
- Handle empty priority queue Pop and replacement with the same queue safely.

### Release scope and compatibility

- Retain the current main branch's Worker/Task/TaskOpt/Queue API. SetQueue and the priority queue were added on main after v0.9.0; custom Worker implementations need that method.
- Retain the working tree's Go 1.20 minimum (v0.9.0 declared Go 1.17).
- Every requires a positive duration; unsupported negative repeat counts and unlimited repetition without an interval panic rather than causing an immediate infinite loop. Finite Repeat without Every remains supported.
- Stop preserves draining behavior, including future queued tasks. It does not interrupt functions.
- Defer middleware, retries, context tasks, metrics, HTTP status, and calendar helpers to a future version.
- Add fast regression tests, executable package examples, fuzz seeds for queue ordering, and Go 1.20/stable CI with race detection and vet.
