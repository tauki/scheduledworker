package scheduledworker

import "time"

// TaskOpt configures a task at submission time. Built-in options apply in order.
type TaskOpt func(*taskOpt)

type taskOpt struct {
	repeat int
	every  time.Duration
	until  time.Time
}

// Forever is the Repeat value for an unlimited number of executions.
const Forever = -1

// Repeat sets the total number of executions, including the first. Zero retains
// the default one-shot behavior. Only Forever may be negative.
func Repeat(n int) TaskOpt {
	if n < Forever {
		panic("scheduledworker: repeat must be nonnegative or Forever")
	}
	return func(o *taskOpt) {
		o.repeat = n
	}
}

// RepeatForever requires Every with a positive interval at submission.
func RepeatForever() TaskOpt {
	return Repeat(Forever)
}

// Every sets a positive fixed-delay interval and implies unlimited repetition
// unless a nonzero repeat count has already been supplied.
func Every(d time.Duration) TaskOpt {
	if d <= 0 {
		panic("scheduledworker: interval must be positive")
	}
	return func(o *taskOpt) {
		o.every = d
		if o.repeat == 0 {
			o.repeat = Forever
		}
	}
}

// Until includes only executions with a scheduled At no later than t. A zero
// time removes the cutoff; the bound does not limit actual execution duration.
func Until(t time.Time) TaskOpt {
	return func(o *taskOpt) {
		o.until = t
	}
}
