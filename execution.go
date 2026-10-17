package scheduledworker

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync/atomic"
	"time"
)

type taskState struct {
	remaining int
	attempt   int
	nominal   time.Time
}
type activeTask struct {
	cancel context.CancelFunc
	onStop bool
}

// PanicError describes a recovered handler or middleware panic. Stack is capped
// at 16 KiB. A nil Value can represent panic(nil) on older Go versions.
type PanicError struct {
	Value any
	Stack []byte
}

func (p *PanicError) Error() string { return fmt.Sprintf("scheduledworker: panic: %v", p.Value) }

func protected(fn func() error) (err error, panicked bool, value any) {
	returned := false
	defer func() {
		if !returned {
			value = recover()
			stack := debug.Stack()
			if len(stack) > 16*1024 {
				stack = stack[:16*1024]
			}
			err, panicked = &PanicError{Value: value, Stack: stack}, true
		}
	}()
	err = fn()
	returned = true
	return
}

func chain(inters []Interceptor, terminal Next) Next {
	return func(ctx context.Context, task *Task) error {
		var violation atomic.Bool
		next := terminal
		for i := len(inters) - 1; i >= 0; i-- {
			current, following := inters[i], next
			if current == nil {
				continue
			}
			next = func(ctx context.Context, task *Task) error {
				var used atomic.Bool
				return current(ctx, task, func(ctx context.Context, task *Task) error {
					if !used.CompareAndSwap(false, true) {
						violation.Store(true)
						return ErrNextCalledTwice
					}
					if ctx == nil || task == nil {
						return fmt.Errorf("%w: nil middleware argument", ErrInvalidTask)
					}
					return following(ctx, task)
				})
			}
		}
		err := next(ctx, task)
		if violation.Load() {
			return ErrNextCalledTwice
		}
		return err
	}
}

func transform(inters []Interceptor, ctx context.Context, task *Task, earliest time.Time) (bool, error) {
	view := task.snapshot()
	keep := false
	err, _, _ := protected(func() error {
		return chain(inters, func(_ context.Context, t *Task) error {
			view.At = t.At
			keep = true
			return nil
		})(ctx, &view)
	})
	if err != nil {
		return false, err
	}
	if view.At.Before(earliest) {
		view.At = earliest
	}
	task.At = view.At
	return keep, nil
}

// nextTask runs under the worker lock. Key contention is handled before
// dispatch, so unrelated due tasks can use otherwise free execution slots.
func (w *worker) nextTask(now time.Time) (*Task, time.Duration, bool) {
	wait := w.idleDuration
	var blocked []*Task
	var selected *Task
	future := false
	for head := w.queue.Peek(); head != nil; head = w.queue.Peek() {
		if head.At.After(now) {
			wait = head.At.Sub(now)
			future = true
			break
		}
		task := w.queue.Pop()
		if task.dispatching || (task.spec != nil && task.spec.Key != "" && w.keys[task.spec.Key]) {
			blocked = append(blocked, task)
			continue
		}
		selected = task
		break
	}
	for _, task := range blocked {
		w.queue.Push(task)
	}
	return selected, wait, selected == nil && len(blocked) > 0 && !future
}

func (w *worker) enqueue(task *Task) bool {
	w.Lock()
	if w.closed {
		w.Unlock()
		return false
	}
	w.queue.Push(task)
	w.stats.Scheduled++
	observers, length := w.queueObserversLocked()
	w.Unlock()
	w.tryWake()
	w.metric(task, func(m MetricsCollector) { m.IncScheduled() })
	w.notifyQueue(observers, length)
	return true
}

func (w *worker) execute(task *Task) {
	var next *Task
	// Always release dispatch/key ownership, including runtime.Goexit. Goexit and
	// process exit are outside the handler result/repetition contract.
	defer func() {
		defer func() {
			w.Lock()
			delete(w.active, task)
			task.dispatching = false
			if task.spec != nil && task.spec.Key != "" {
				delete(w.keys, task.spec.Key)
			}
			w.inFlight--
			w.Unlock()
			w.tryWake()
		}()
		w.Lock()
		before := w.queue.Len()
		if task.spec == nil {
			w.reschedule(task)
		} else if next != nil && !w.closed {
			w.queue.Push(next)
		}
		enqueued := w.queue.Len() > before
		if enqueued {
			w.stats.Scheduled++
		}
		observers, length := w.queueObserversLocked()
		w.Unlock()
		if enqueued {
			w.metric(task, func(m MetricsCollector) { m.IncScheduled() })
		}
		w.notifyQueue(observers, length)
	}()

	started := time.Now()
	w.Lock()
	w.stats.Started++
	w.Unlock()
	w.metric(task, func(m MetricsCollector) {
		m.IncStarted()
		d := started.Sub(task.At)
		if d < 0 {
			d = 0
		}
		m.ObserveWait(d)
	})
	var err error
	var panicked bool
	var pan any
	if task.spec == nil {
		err, panicked, pan = protected(func() error {
			if task.FnErr != nil {
				return task.FnErr(context.Background())
			}
			task.Fn()
			return nil
		})
	} else {
		ctx, cancel := w.taskContext(task)
		defer cancel()
		w.callback(func() {
			if task.spec.OnStart != nil {
				view := task.snapshot()
				task.spec.OnStart(&view)
			}
		})
		view := task.snapshot()
		err, panicked, pan = protected(func() error {
			return chain(task.spec.OnRun, func(ctx context.Context, t *Task) error {
				if t.FnErr != nil {
					return t.FnErr(ctx)
				}
				if t.Fn == nil {
					return fmt.Errorf("%w: middleware removed handler", ErrInvalidTask)
				}
				t.Fn()
				return nil
			})(ctx, &view)
		})
	}
	finished := time.Now()
	w.Lock()
	switch {
	case panicked:
		w.stats.Panicked++
	case err != nil:
		w.stats.Failed++
	default:
		w.stats.Completed++
	}
	w.Unlock()
	w.metric(task, func(m MetricsCollector) {
		m.ObserveRun(finished.Sub(started))
		switch {
		case panicked:
			m.IncPanicked()
		case err != nil:
			m.IncFailed()
		default:
			m.IncCompleted()
		}
	})
	if task.spec == nil {
		return
	}
	s := task.spec
	w.callback(func() {
		view := task.snapshot()
		switch {
		case panicked:
			if s.OnPanic != nil {
				s.OnPanic(&view, pan)
			}
		case err != nil:
			if s.OnError != nil {
				s.OnError(&view, err)
			}
		default:
			if s.OnComplete != nil {
				s.OnComplete(&view)
			}
		}
	})
	next = w.nextOccurrence(task, err, panicked, finished)
}

func (w *worker) taskContext(task *Task) (context.Context, context.CancelFunc) {
	s := task.spec
	ctx, cancel := context.WithCancel(s.Context)
	if s.Timeout > 0 {
		inner, stop := context.WithTimeout(ctx, s.Timeout)
		outer := cancel
		ctx, cancel = inner, func() { stop(); outer() }
	}
	if !s.Deadline.IsZero() {
		inner, stop := context.WithDeadline(ctx, s.Deadline)
		outer := cancel
		ctx, cancel = inner, func() { stop(); outer() }
	}
	w.Lock()
	w.active[task] = activeTask{cancel: cancel, onStop: s.CancelOnStop}
	closed := w.closed
	w.Unlock()
	if closed && s.CancelOnStop {
		cancel()
	}
	return ctx, cancel
}

func (w *worker) nextOccurrence(task *Task, runErr error, panicked bool, finished time.Time) *Task {
	s, state := task.spec, task.state
	w.Lock()
	closed := w.closed
	w.Unlock()
	if closed || s.Context.Err() != nil {
		return nil
	}
	if runErr != nil && state.attempt < s.Retry.MaxAttempts && (!panicked || s.Retry.RetryOnPanic) {
		upcoming := state.attempt + 1
		var delay time.Duration
		var policyErr error
		if s.Retry.Backoff != nil {
			policyErr, _, _ = protected(func() error { delay = s.Retry.Backoff(upcoming - 1); return nil })
		}
		if policyErr != nil {
			// A broken retry policy exhausts this occurrence, rather than silently
			// destroying the recurring registration.
			w.policyError(task, policyErr)
		} else {
			if delay < 0 {
				delay = 0
			}
			task.At = finished.Add(delay)
			if !s.Until.IsZero() && task.At.After(s.Until) {
				return nil
			}
			state.attempt = upcoming
			return task
		}
	}
	if state.remaining > 0 {
		state.remaining--
	}
	if state.remaining == 0 {
		return nil
	}
	state.attempt = 1
	nominal := finished.Add(s.Every)
	if s.Mode == FixedRate {
		nominal = state.nominal.Add(s.Every)
		if nominal.Before(finished) {
			missed := finished.Sub(nominal) / s.Every
			nominal = nominal.Add(missed * s.Every)
		}
	}
	state.nominal = nominal
	task.At = nominal
	if !s.Until.IsZero() && nominal.After(s.Until) {
		return nil
	}
	keep, err := transform(s.OnReschedule, s.Context, task, nominal)
	if err != nil {
		w.policyError(task, err)
		return nil
	}
	if !keep || (!s.Until.IsZero() && task.At.After(s.Until)) {
		return nil
	}
	return task
}

func (w *worker) policyError(task *Task, err error) {
	w.callbackPanicIf(err)
	if task.spec.OnError != nil {
		w.callback(func() { view := task.snapshot(); task.spec.OnError(&view, err) })
	}
}

func (w *worker) callbackPanic() { w.Lock(); w.stats.CallbackPanicked++; w.Unlock() }
func (w *worker) callbackPanicIf(err error) {
	if _, ok := err.(*PanicError); ok {
		w.callbackPanic()
	}
}
func (w *worker) callback(fn func()) {
	_, panicked, _ := protected(func() error { fn(); return nil })
	if panicked {
		w.callbackPanic()
	}
}
