package scheduledworker

import (
	"reflect"
	"sync"
	"time"
)

const (
	defaultScheduleDuration = 30 * time.Second
	defaultMaxWorker        = 10
)

// Worker schedules process-local functions. Create an instance with New.
// Its methods are safe to call concurrently. A stopped worker cannot restart.
// Stop must be called by the owner, not from a function running on this worker.
type Worker interface {
	Submit(Task, ...TaskOpt)
	SetDuration(time.Duration) Worker
	SetMaxWorker(int) Worker
	SetQueue(Queue) Worker
	Start() Worker
	Stop()
}

// Task is a function and its earliest scheduled time. A zero At means now, or
// one interval from now when Every is supplied. Fn must return for Stop to finish.
type Task struct {
	At  time.Time
	Fn  func()
	opt *taskOpt
}

type worker struct {
	sync.Mutex
	queue        Queue
	maxWorker    int
	idleDuration time.Duration
	started      bool
	closed       bool
	inFlight     int
	wake         chan struct{}
	done         chan struct{}
}

var _ Worker = (*worker)(nil)

// New creates an idle worker with ten execution slots and a priority queue.
// It starts no goroutines or timers until Start is called.
func New() Worker {
	return &worker{
		queue:        NewPriorityQueue(),
		maxWorker:    defaultMaxWorker,
		idleDuration: defaultScheduleDuration,
		wake:         make(chan struct{}, 1),
		done:         make(chan struct{}),
	}
}

// Submit registers a task. Nil functions and submissions after Stop are ignored.
// Options apply left to right. An unlimited repeat requires a positive interval.
func (w *worker) Submit(task Task, opts ...TaskOpt) {
	if task.Fn == nil {
		return
	}
	opt := new(taskOpt)
	for _, apply := range opts {
		if apply != nil {
			apply(opt)
		}
	}
	if opt.repeat == Forever && opt.every <= 0 {
		panic("scheduledworker: unlimited repeat requires a positive interval")
	}
	task.opt = opt
	if task.At.IsZero() {
		task.At = time.Now().Add(opt.every)
	}
	if !opt.until.IsZero() && task.At.After(opt.until) {
		return
	}
	w.Lock()
	if !w.closed {
		w.queue.Push(&task)
	}
	w.Unlock()
	w.tryWake()
}

// Start starts scheduling once. Repeated calls, including after Stop, are no-ops.
func (w *worker) Start() Worker {
	w.Lock()
	if !w.started && !w.closed {
		w.started = true
		go w.run()
	}
	w.Unlock()
	return w
}

func (w *worker) run() {
	// Only this goroutine owns the timer. Tracking whether its value was consumed
	// makes Stop/drain/Reset safe with pre-Go-1.23 asynchronous timer channels too.
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	armed := false
	stopTimer := func() {
		if armed && !timer.Stop() {
			<-timer.C
		}
		armed = false
	}
	defer close(w.done)
	defer stopTimer()

	for {
		w.Lock()
		if w.closed && w.queue.Len() == 0 && w.inFlight == 0 {
			w.Unlock()
			return
		}
		wait := w.idleDuration
		saturated := w.inFlight >= w.maxWorker
		if head := w.queue.Peek(); head != nil && !saturated {
			wait = time.Until(head.At)
			if wait <= 0 {
				task := w.queue.Pop()
				w.inFlight++
				w.Unlock()
				go w.execute(task)
				continue
			}
		}
		w.Unlock()

		stopTimer()
		var timerC <-chan time.Time
		if !saturated {
			timer.Reset(wait)
			armed = true
			timerC = timer.C
		}
		// Under saturation only completion/configuration/submission wakes us.
		// An already-due queue head must not produce a zero-duration busy loop.
		select {
		case <-timerC:
			armed = false
		case <-w.wake:
		}
	}
}

func (w *worker) execute(task *Task) {
	defer func() {
		// Preserve the legacy contract: a panic ends this invocation, while
		// repeat accounting and slot cleanup still happen. No retry is implied.
		_ = recover()
		w.Lock()
		w.reschedule(task)
		w.inFlight--
		w.Unlock()
		w.tryWake()
	}()
	task.Fn()
}

// reschedule runs under the worker lock, after the function has returned/panicked.
func (w *worker) reschedule(task *Task) {
	if w.closed || task.opt == nil {
		return
	}
	if task.opt.repeat > 0 {
		task.opt.repeat--
	}
	if task.opt.repeat == 0 {
		return
	}
	task.At = time.Now().Add(task.opt.every)
	if !task.opt.until.IsZero() && task.At.After(task.opt.until) {
		return
	}
	w.queue.Push(task)
}

// SetDuration sets the idle wake interval. Scheduled deadlines and submissions
// wake the worker independently. Nonpositive durations panic, as in v0.9.
func (w *worker) SetDuration(duration time.Duration) Worker {
	if duration <= 0 {
		panic("scheduledworker: duration must be positive")
	}
	w.Lock()
	w.idleDuration = duration
	w.Unlock()
	w.tryWake()
	return w
}

// SetMaxWorker sets the dispatch limit; nonpositive counts are normalized to one.
// Reducing the limit does not interrupt already-dispatched functions.
func (w *worker) SetMaxWorker(count int) Worker {
	if count <= 0 {
		count = 1
	}
	w.Lock()
	w.maxWorker = count
	w.Unlock()
	w.tryWake()
	return w
}

// SetQueue transfers queued work to queue. The worker must exclusively own it;
// callers must not mutate it concurrently. A nil queue panics. Queue methods must
// return promptly and must not call back into the worker.
func (w *worker) SetQueue(queue Queue) Worker {
	if queue == nil {
		panic("scheduledworker: queue must not be nil")
	}
	value := reflect.ValueOf(queue)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			panic("scheduledworker: queue must not be nil")
		}
	}
	w.Lock()
	defer w.Unlock()
	// A fixed count also handles setting the existing queue without looping
	// forever as each popped task is pushed back into the same queue.
	for n := w.queue.Len(); n > 0; n-- {
		queue.Push(w.queue.Pop())
	}
	w.queue = queue
	w.tryWake()
	return w
}

// Stop rejects new submissions and repeats, drains previously queued tasks at
// their scheduled times, and waits for dispatched functions. It is idempotent.
// Before Start it discards queued tasks and returns without executing them.
// A future task or nonreturning function can therefore make a draining Stop wait.
func (w *worker) Stop() {
	w.Lock()
	if !w.closed {
		w.closed = true
		if !w.started {
			for w.queue.Len() > 0 {
				w.queue.Pop()
			}
			close(w.done)
		}
	}
	w.Unlock()
	w.tryWake()
	<-w.done
}

func (w *worker) tryWake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}
