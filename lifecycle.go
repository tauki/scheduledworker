package scheduledworker

import (
	"context"
	"fmt"
	"time"
)

// StopMode controls queued work; neither mode kills running functions.
type StopMode int

const (
	Drain StopMode = iota
	Abort
)

// Status is a point-in-time snapshot. Scheduled counts successful queue inserts,
// Started/Completed/Failed/Panicked count attempts, and CallbackPanicked counts
// recovered observation/policy panics. Future queued work is normal, not unhealthy.
type Status struct {
	QueueLen         int
	InFlight         int64
	Scheduled        int64
	Started          int64
	Completed        int64
	Failed           int64
	Panicked         int64
	CallbackPanicked int64
	Dropped          int64
	NextDue          time.Time
	Running          bool
	Stopping         bool
	Stopped          bool
}

func (w *worker) Status() Status {
	w.Lock()
	defer w.Unlock()
	s := w.stats
	s.QueueLen = w.queue.Len()
	s.InFlight = int64(w.inFlight)
	s.Running = w.started && !w.terminated
	s.Stopping = w.closed && !w.terminated
	s.Stopped = w.terminated
	if head := w.queue.Peek(); head != nil {
		s.NextDue = head.At
	}
	return s
}

// StopCtx closes admission/repeats and waits up to ctx's deadline. Drain keeps
// queued work; Abort discards it. Repeated calls still wait for completion, and
// Abort can escalate an earlier Drain. A timeout does not end shutdown or free
// execution slots. CancelOnStop tasks receive cooperative cancellation in either
// mode; all other running functions are allowed to finish.
func (w *worker) StopCtx(ctx context.Context, mode StopMode) error {
	if ctx == nil || (mode != Drain && mode != Abort) {
		return fmt.Errorf("%w: invalid shutdown context or mode", ErrInvalidTask)
	}
	w.beginStop(mode)
	select {
	case <-w.done:
		return nil
	default:
	}
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *worker) beginStop(mode StopMode) {
	w.Lock()
	first := !w.closed
	w.closed = true
	if mode == Abort || !w.started {
		for w.queue.Len() > 0 {
			w.queue.Pop()
			w.stats.Dropped++
		}
	}
	if first && !w.started {
		w.terminated = true
		close(w.done)
	}
	var cancels []context.CancelFunc
	for _, active := range w.active {
		if active.onStop {
			cancels = append(cancels, active.cancel)
		}
	}
	observers, length := w.queueObserversLocked()
	w.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	w.tryWake()
	w.notifyQueue(observers, length)
}

func (w *worker) queueObserversLocked() ([]func(int), int) {
	return append([]func(int){}, w.observers...), w.queue.Len()
}
func (w *worker) notifyQueue(observers []func(int), length int) {
	for _, observer := range observers {
		w.callback(func() { observer(length) })
	}
}

// AddQueueObserver synchronously reports queue length initially and after queue
// mutations. Callbacks run outside locks, may run concurrently/out of order, must
// be prompt and must not call Stop or wait for queued work. Use Status for a
// current gauge. Observer panics are recovered and counted.
func (w *worker) AddQueueObserver(fn func(int)) {
	if fn == nil {
		return
	}
	w.Lock()
	w.observers = append(w.observers, fn)
	length := w.queue.Len()
	w.Unlock()
	w.notifyQueue([]func(int){fn}, length)
}

// AddQueueObserver attaches a queue observer through the Worker interface.
func AddQueueObserver(w Worker, fn func(int)) bool {
	if w == nil {
		return false
	}
	w.AddQueueObserver(fn)
	return true
}

// StartQueueLengthSampler reports current queue length until ctx is canceled.
// It blocks its caller; callers own any goroutine used to run it.
func StartQueueLengthSampler(ctx context.Context, w Worker, interval time.Duration, report func(int)) {
	if ctx == nil || w == nil || interval <= 0 || report == nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			report(w.Status().QueueLen)
		}
	}
}
