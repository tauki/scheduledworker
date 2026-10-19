package scheduledworker

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"
)

type permanentError struct{ error }

func (e *permanentError) Unwrap() error { return e.error }

// Permanent preserves err for errors.Is/As and prevents retry. Nil stays nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err}
}

// PanicError captures at most 16 KiB of the invoking goroutine's stack.
type PanicError struct {
	Value any
	Stack string
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("scheduledworker: handler panic: %v", e.Value)
}

type assignment struct {
	r          *registration
	handler    Handler
	info       RunInfo
	ctx        context.Context
	control    context.Context
	cleanup    context.CancelFunc
	dispatched instant
	timeout    time.Duration
	canRetry   bool
}
type completion struct {
	job       assignment
	ended     instant
	duration  time.Duration
	outcome   Outcome
	err       error
	permanent bool
}

func worker(jobs <-chan assignment, results chan<- completion, clock clock, exited chan<- struct{}) {
	defer func() { exited <- struct{}{} }()
	for a := range jobs {
		c := invoke(a, clock)
		a = assignment{}
		results <- c
	}
}
func invoke(a assignment, clock clock) (c completion) {
	c.job = a
	start := clock.now()
	var returned instant
	handlerReturned := false
	normal := false
	defer func() {
		v := recover()
		c.ended = clock.now()
		if !handlerReturned {
			returned = c.ended
		}
		c.duration = time.Duration(returned.mono - start.mono)
		if !normal {
			stack := make([]byte, 16<<10)
			n := runtime.Stack(stack, false)
			c.outcome = Panicked
			c.err = &PanicError{Value: v, Stack: string(stack[:n])}
		}
		a.cleanup()
		// Completions carry identity/accounting only. An idle worker or a queued
		// completion must not retain the handler or its context's application data.
		c.job.handler = nil
		c.job.ctx = nil
		c.job.control = nil
		c.job.cleanup = nil
	}()
	c.err = a.handler(a.ctx, a.info)
	returned = clock.now()
	handlerReturned = true
	switch {
	case a.control.Err() != nil:
		c.outcome = Canceled
		c.err = joinContext(context.Canceled, c.err)
	case a.ctx.Err() == context.DeadlineExceeded || (a.timeout > 0 && uint64(returned.mono)-uint64(a.dispatched.mono) >= uint64(a.timeout)):
		// Context timer callbacks can lag the deadline under CPU starvation.
		c.outcome = TimedOut
		c.err = joinContext(context.DeadlineExceeded, c.err)
	case c.err != nil:
		c.outcome = Failed
	default:
		c.outcome = Succeeded
	}
	if a.canRetry && (c.outcome == Failed || c.outcome == TimedOut) {
		// Error As/Unwrap methods are application code. Keep their execution and
		// panic recovery on a worker, never in coordinator transactions.
		var permanent *permanentError
		c.permanent = errors.As(c.err, &permanent)
	}
	normal = true
	return
}
func joinContext(cause, err error) error {
	// cause is one of the comparable standard context sentinels. Compare by
	// identity without invoking application Is/Unwrap methods during cleanup.
	if err == nil || err == cause {
		return cause
	}
	return errors.Join(cause, err)
}

func retryCap(r RetryPolicy, attempt int) time.Duration {
	d := r.InitialBackoff
	for i := 1; i < attempt && d < r.MaxBackoff; i++ {
		if d > r.MaxBackoff/2 {
			return r.MaxBackoff
		}
		d *= 2
	}
	if d > r.MaxBackoff {
		return r.MaxBackoff
	}
	return d
}
