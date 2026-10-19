package scheduledworker

import (
	"context"
	"errors"
	"time"
)

// ScheduleID identifies a registration within one scheduler; it is not durable.
type ScheduleID string

// RunID identifies an occurrence across all its attempts within one scheduler.
type RunID string

// Handler must return or panic to release its worker slot. Cancellation is cooperative.
type Handler func(context.Context, RunInfo) error

// Kind selects one-shot, completion-relative, or anchored interval scheduling.
type Kind uint8

const (
	Once Kind = iota
	FixedDelay
	FixedRate
)

// OverlapPolicy controls ticks due while a fixed-rate run (including retries) is open.
type OverlapPolicy uint8

const (
	BufferLatest OverlapPolicy = iota
	SkipOverlap
)

// spec is copied into a registration after its options are applied and validated.
type spec struct {
	Name    string
	Kind    Kind
	At      time.Time     // First nominal time; zero means now, or now+Every for intervals.
	Every   time.Duration // Positive for recurring schedules; zero for Once.
	Overlap OverlapPolicy
	MaxRuns uint64        // First dispatches, including failures; 0 means unlimited for intervals.
	Until   time.Time     // Inclusive nominal cutoff, not a retry or execution deadline.
	Timeout time.Duration // Cooperative per-attempt timeout; 0 disables it.
	Retry   RetryPolicy
}

// RetryPolicy uses capped exponential full jitter; panics and Permanent errors never retry.
type RetryPolicy struct {
	MaxAttempts    int           // Includes the first attempt; 0/1 means no retry.
	InitialBackoff time.Duration // 0 selects 1s when retries are enabled.
	MaxBackoff     time.Duration // 0 selects 1m when retries are enabled.
}

// RunInfo is immutable invocation metadata. Retries retain identity and nominal time.
type RunInfo struct {
	ScheduleID  ScheduleID
	RunID       RunID
	Name        string
	Number      uint64    // First-dispatch count starting at 1.
	Attempt     int       // Starts at 1 within a run.
	ScheduledAt time.Time // Unchanged across retries.
}

// State describes the terminal, one-way scheduler lifecycle.
type State uint8

const (
	Created State = iota
	Running
	Stopping
	Stopped
)

// ScheduleState distinguishes nominal waiting, eligibility, execution, and cleanup.
type ScheduleState uint8

const (
	Scheduled ScheduleState = iota
	Ready
	Executing
	RetryWaiting
	Canceling
	Finished
)

// Outcome records a run or attempt result separately from schedule termination.
type Outcome uint8

const (
	NoOutcome Outcome = iota
	Succeeded
	Failed
	TimedOut
	Panicked
	Canceled
)

// FinishReason explains schedule termination independently of the last run's outcome.
type FinishReason uint8

const (
	OneShotFinished FinishReason = iota
	RunLimitReached
	UntilReached
	ExplicitlyCanceled
	SchedulerStopped
)

// Counters separate first-dispatch runs, attempts, and suppressed nominal occurrences.
type Counters struct {
	RunsStarted          uint64 // First attempts dispatched.
	RunsSucceeded        uint64
	RunsFailed           uint64 // Terminal error, timeout, or panic; excludes cancellation.
	RunsCanceled         uint64
	AttemptsDispatched   uint64 // Includes retries.
	AttemptsFailed       uint64 // Includes timeout/panic; excludes control cancellation.
	AttemptsTimedOut     uint64
	AttemptsPanicked     uint64
	RetriesScheduled     uint64
	OccurrencesSkipped   uint64
	OccurrencesCoalesced uint64
}

// RunResult describes the terminal attempt, or a run canceled during retry waiting.
type RunResult struct {
	Run          RunInfo   // Attempt is the final dispatched attempt number.
	DispatchedAt time.Time // Final attempt's dispatch wall time.
	FinishedAt   time.Time // Worker return or cancellation during backoff.
	Outcome      Outcome
	Err          error
}

// ScheduleStatus is a consistent snapshot; error/panic objects remain application-owned.
type ScheduleStatus struct {
	ID              ScheduleID
	Name            string
	State           ScheduleState
	NextScheduledAt time.Time // Earliest retained nominal occurrence, or next tick.
	BufferedAt      time.Time // Pending occurrence alongside an open run, otherwise zero.
	CurrentRun      RunInfo   // Retained during retry waiting/ready; zero when no run is open.
	NextAttemptAt   time.Time // Eligibility, including a past ready time; zero while executing.
	Counters        Counters
	LastRun         RunResult // Latest terminal run; NoOutcome until one exists.
}

// Result remains available on the handle after admission capacity is released.
type Result struct {
	ID       ScheduleID
	Reason   FinishReason
	Counters Counters
	LastRun  RunResult
}

// Status is a consistent process-local snapshot. It is not atomic with Handle.Status.
type Status struct {
	State             State
	ActiveSchedules   int // Registrations still holding admission capacity.
	ReadySchedules    int // Eligible first attempts and eligible retries.
	RunningAttempts   int // Dispatched attempts whose completion is not yet processed.
	WaitingRetries    int // Backoff has not expired; excludes ready retries.
	BufferedSchedules int
	NextWakeAt        time.Time
	Counters          Counters
	Accepted          uint64 // Committed registrations.
	Rejected          uint64 // Coordinator-rejected admissions; excludes caller-local validation.
	EventsDropped     uint64
	ObserverPanics    uint64
}

// Observer receives best-effort events serially; it must cooperate with cancellation.
type Observer func(context.Context, Event)

// EventKind identifies a coordinator transition; suppressed ranges are aggregated.
type EventKind uint8

const (
	RunStarted EventKind = iota
	AttemptFinished
	RetryScheduled
	RunFinished
	OccurrencesSuppressed
	ScheduleFinished
)

// Event is immutable transition metadata. Delivery may drop without affecting results.
type Event struct {
	Kind       EventKind
	At         time.Time
	ScheduleID ScheduleID
	Run        RunInfo
	Outcome    Outcome
	Reason     FinishReason // Meaningful for ScheduleFinished.
	Err        error
	Count      uint64        // Suppressed occurrences; zero on other events.
	Skipped    bool          // Distinguishes skipped from coalesced occurrences.
	Lag        time.Duration // RunStarted only: first dispatch minus nominal eligibility.
	Duration   time.Duration // AttemptFinished only: handler invocation duration.
}

var (
	ErrInvalidConfig   = errors.New("scheduledworker: invalid config")
	ErrInvalidSpec     = errors.New("scheduledworker: invalid spec")
	ErrInvalidArgument = errors.New("scheduledworker: invalid argument")
	ErrNotRunning      = errors.New("scheduledworker: not running")
	ErrAlreadyStarted  = errors.New("scheduledworker: already started")
	ErrStopped         = errors.New("scheduledworker: stopped")
	ErrCapacity        = errors.New("scheduledworker: capacity exhausted")
)
