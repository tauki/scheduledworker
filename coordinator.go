package scheduledworker

import (
	"container/heap"
	"container/list"
	"context"
	"math"
	"time"
)

type registration struct {
	h                                *Handle
	handler                          Handler
	spec                             Spec
	base                             instant
	until                            int64
	order                            uint64
	status                           ScheduleStatus
	tick, retry                      deadline
	future, pending, open, executing bool
	nominal, eligible                int64
	ready                            *list.Element
	run                              RunInfo
	lastAttempt                      RunResult
	control                          context.CancelFunc
	forced                           bool
	reason                           FinishReason
}

func (r *registration) wall(at int64) time.Time {
	return r.base.wall.Add(time.Duration(at - r.base.mono))
}

type coordinator struct {
	s                                       *Scheduler
	lifetime                                context.Context
	regs                                    map[ScheduleID]*registration
	timers                                  deadlineHeap
	ready                                   list.List
	jobs                                    chan assignment
	completions                             chan completion
	exits                                   chan struct{}
	started                                 chan struct{}
	workersExited                           int
	jobsClosed                              bool
	status                                  Status
	sequence, runSequence                   uint64
	stopping, aborted                       bool
	stopSignal, abortSignal, lifetimeSignal <-chan struct{}
	obs                                     *eventDispatcher
	observerExited                          bool
}

func newCoordinator(s *Scheduler, ctx context.Context) *coordinator {
	return &coordinator{s: s, lifetime: ctx, regs: make(map[ScheduleID]*registration),
		jobs: make(chan assignment, s.cfg.Workers), completions: make(chan completion, s.cfg.Workers),
		exits: make(chan struct{}, s.cfg.Workers), started: make(chan struct{}), status: Status{State: Running},
		stopSignal: s.stop, abortSignal: s.abort, lifetimeSignal: ctx.Done()}
}
func (c *coordinator) run() {
	defer close(c.s.done)
	for i := 0; i < c.s.cfg.Workers; i++ {
		go worker(c.jobs, c.completions, c.s.clock, c.exits)
	}
	if c.s.cfg.Observer != nil {
		c.obs = newEventDispatcher(c.s.cfg.Observer, c.s.cfg.EventBuffer)
		go c.obs.run()
	}
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	close(c.started)
	for {
		// Check lifecycle before due work or handoff, even under continuous load.
		if c.lifetimeSignal != nil && c.lifetime.Err() != nil {
			c.s.requestStop(true)
			c.lifetimeSignal = nil
		}
		select {
		case <-c.abortSignal:
			c.escalate()
			c.abortSignal = nil
		default:
		}
		select {
		case <-c.stopSignal:
			c.observeStop()
			c.stopSignal = nil
		default:
		}
		// One queued mutation/completion per turn prevents a due burst monopolizing
		// the coordinator. Due processing itself is bounded to 64 nodes.
		select {
		case cmd := <-c.s.commands:
			c.command(cmd)
		case result := <-c.completions:
			c.complete(result)
		default:
		}
		now := c.s.clock.now()
		for i := 0; i < 64 && len(c.timers) > 0 && c.timers[0].at <= now.mono; i++ {
			c.due(heap.Pop(&c.timers).(*deadline), now)
		}
		if !c.stopping && c.status.RunningAttempts < c.s.cfg.Workers && c.ready.Len() > 0 {
			if c.dispatch(c.ready.Front().Value.(*registration)) {
				c.publish()
				continue
			}
		}
		if c.stopping && c.status.RunningAttempts == 0 && !c.jobsClosed {
			close(c.jobs)
			c.jobsClosed = true
			if c.obs != nil {
				c.obs.seal()
			}
		}
		if c.jobsClosed && c.workersExited == c.s.cfg.Workers && (c.obs == nil || c.observerExited) {
			c.status.State = Stopped
			c.s.mu.Lock()
			c.s.state = Stopped
			c.publish()
			c.s.mu.Unlock()
			return
		}
		c.publish()
		var wake <-chan time.Time
		if len(c.timers) > 0 {
			d := delayUntil(c.timers[0].at, c.s.clock.now().mono)
			timer.Reset(d)
			wake = timer.C
		}
		var observerDone <-chan struct{}
		if c.obs != nil && !c.observerExited {
			observerDone = c.obs.done
		}
		select {
		case cmd := <-c.s.commands:
			c.command(cmd)
		case result := <-c.completions:
			c.complete(result)
		case <-c.stopSignal:
			c.observeStop()
			c.stopSignal = nil
		case <-c.abortSignal:
			c.escalate()
			c.abortSignal = nil
		case <-c.lifetimeSignal:
			c.s.requestStop(true)
			c.lifetimeSignal = nil
		case <-c.exits:
			c.workersExited++
		case <-observerDone:
			c.observerExited = true
		case <-wake:
		}
		timer.Stop()
	}
}
func delayUntil(at, now int64) time.Duration {
	if at <= now {
		return 0
	}
	d := uint64(at) - uint64(now)
	if d > math.MaxInt64 {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(d)
}
func (c *coordinator) command(cmd command) {
	c.observeStop()
	if err := cmd.ctx.Err(); err != nil {
		if !cmd.cancel {
			c.status.Rejected++
		}
		cmd.reply <- response{err: err}
		return
	}
	if cmd.cancel {
		if r := c.regs[cmd.id]; r != nil {
			c.cancel(r, ExplicitlyCanceled, true)
		}
		cmd.reply <- response{}
		return
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if c.s.state != Running {
		cmd.reply <- response{err: ErrStopped}
		c.status.Rejected++
		return
	}
	if len(c.regs) >= c.s.cfg.MaxSchedules {
		cmd.reply <- response{err: ErrCapacity}
		c.status.Rejected++
		return
	}
	now := c.s.clock.now()
	first, until, err := resolveSpec(cmd.spec, now)
	if err != nil {
		cmd.reply <- response{err: err}
		c.status.Rejected++
		return
	}
	// This is the last cancellation check before the registration commits.
	if err = cmd.ctx.Err(); err != nil {
		cmd.reply <- response{err: err}
		c.status.Rejected++
		return
	}
	c.sequence++
	h := &Handle{id: scheduleID(c.sequence), done: make(chan struct{})}
	h.owner.Store(c.s)
	r := &registration{h: h, handler: cmd.handler, spec: cmd.spec, base: now, until: until, order: c.sequence, future: true}
	r.status = ScheduleStatus{ID: h.id, Name: cmd.spec.Name, State: Scheduled}
	r.tick = deadline{at: first, order: r.order, r: r, index: -1}
	r.retry = deadline{order: r.order, r: r, retry: true, index: -1}
	c.regs[h.id] = r
	c.status.ActiveSchedules++
	c.status.Accepted++
	heap.Push(&c.timers, &r.tick)
	c.update(r)
	cmd.reply <- response{handle: h}
}
func (c *coordinator) queue(r *registration) {
	if r.ready == nil {
		r.ready = c.ready.PushBack(r)
	}
}
func (c *coordinator) unqueue(r *registration) {
	if r.ready != nil {
		c.ready.Remove(r.ready)
		r.ready = nil
	}
}
func (c *coordinator) removeTimers(r *registration) {
	if r.tick.index >= 0 {
		heap.Remove(&c.timers, r.tick.index)
	}
	if r.retry.index >= 0 {
		heap.Remove(&c.timers, r.retry.index)
	}
}
func (c *coordinator) due(d *deadline, now instant) {
	r := d.r
	if d.retry {
		c.queue(r)
		c.update(r)
		return
	}
	if r.spec.Kind != FixedRate {
		r.future = false
		r.pending = true
		r.nominal = d.at
		r.eligible = d.at
		c.queue(r)
		c.update(r)
		return
	}
	c.advanceRate(r, now.mono)
	if r.future {
		heap.Push(&c.timers, &r.tick)
	}
	if r.pending && !r.open {
		r.eligible = r.nominal
		c.queue(r)
	}
	c.update(r)
	c.maybeFinish(r)
}

// advanceRate consumes all due ticks in O(1), even across millions of intervals.
func (c *coordinator) advanceRate(r *registration, now int64) {
	if !r.future || r.tick.at > now {
		return
	}
	end := now
	if !r.spec.Until.IsZero() && end > r.until {
		end = r.until
	}
	if end < r.tick.at {
		r.future = false
		return
	}
	steps := (uint64(end) - uint64(r.tick.at)) / uint64(r.spec.Every)
	latest := int64(uint64(r.tick.at) + steps*uint64(r.spec.Every))
	count := steps + 1
	if r.open && r.spec.Overlap == SkipOverlap {
		c.suppressed(r, count, true)
	} else {
		suppressed := steps
		if r.pending {
			suppressed++
		}
		r.pending = true
		r.nominal = latest
		if suppressed > 0 {
			c.suppressed(r, suppressed, false)
		}
	}
	next, ok := addTime(latest, r.spec.Every)
	r.future = ok && (r.spec.Until.IsZero() || next <= r.until)
	if r.future {
		r.tick.at = next
	}
}
func (c *coordinator) suppressed(r *registration, n uint64, skip bool) {
	if skip {
		c.count(r, Counters{OccurrencesSkipped: n})
	} else {
		c.count(r, Counters{OccurrencesCoalesced: n})
	}
	c.emit(Event{Kind: OccurrencesSuppressed, ScheduleID: r.h.id, Count: n, Skipped: skip})
}

func (c *coordinator) refreshRate(r *registration, through int64) {
	if r.spec.Kind != FixedRate || !r.future || r.tick.at > through {
		return
	}
	if r.tick.index >= 0 {
		heap.Remove(&c.timers, r.tick.index)
	}
	c.advanceRate(r, through)
	if r.future {
		heap.Push(&c.timers, &r.tick)
	}
}

func (c *coordinator) dispatch(r *registration) bool {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if c.s.state != Running || c.lifetime.Err() != nil {
		return false
	}
	now := c.s.clock.now()
	// Refresh even if a due burst has not yet reached this schedule's node.
	c.refreshRate(r, now.mono)
	c.unqueue(r)
	if !r.open {
		c.runSequence++
		r.run = RunInfo{ScheduleID: r.h.id, RunID: runID(c.runSequence), Name: r.spec.Name,
			Number: r.status.Counters.RunsStarted + 1, Attempt: 1, ScheduledAt: r.wall(r.nominal)}
		r.open = true
		r.pending = false
		c.count(r, Counters{RunsStarted: 1})
		if r.spec.MaxRuns > 0 && r.run.Number >= r.spec.MaxRuns {
			r.future = false
			if r.tick.index >= 0 {
				heap.Remove(&c.timers, r.tick.index)
			}
		}
		c.emit(Event{Kind: RunStarted, ScheduleID: r.h.id, Run: r.run, Lag: delayUntil(now.mono, r.nominal)})
	} else {
		r.run.Attempt++
	}
	control, cancel := context.WithCancel(context.WithoutCancel(c.lifetime))
	ctx := context.Context(control)
	cleanup := context.CancelFunc(func() {})
	if r.spec.Timeout > 0 {
		ctx, cleanup = context.WithTimeout(control, r.spec.Timeout)
	}
	r.control = cancel
	r.executing = true
	c.status.RunningAttempts++
	c.count(r, Counters{AttemptsDispatched: 1})
	c.update(r)
	// Each buffered handoff is already counted as dispatched. Outstanding
	// assignments, including those not yet entered by a worker, never exceed W.
	c.jobs <- assignment{r: r, handler: r.handler, info: r.run, ctx: ctx, control: control,
		cleanup: func() { cleanup(); cancel() }, dispatched: now, timeout: r.spec.Timeout, canRetry: r.run.Attempt < r.spec.Retry.MaxAttempts}
	return true
}
func (c *coordinator) complete(done completion) {
	// A completion selected alongside a stop signal must not create a retry or
	// recurrence after the application's lifecycle gate has closed.
	c.observeStop()
	r := done.job.r
	// Ticks strictly before this completion belonged to the still-open run,
	// even when its completion message wins a race with a delayed timer wake.
	// A tick exactly at completion keeps the documented event-order behavior.
	if done.ended.mono > math.MinInt64 {
		c.refreshRate(r, done.ended.mono-1)
	}
	c.status.RunningAttempts--
	r.executing = false
	r.control = nil
	r.lastAttempt = RunResult{Run: done.job.info, DispatchedAt: done.job.dispatched.wall,
		FinishedAt: done.ended.wall, Outcome: done.outcome, Err: done.err}
	delta := Counters{}
	if done.outcome == Failed || done.outcome == TimedOut || done.outcome == Panicked {
		delta.AttemptsFailed = 1
	}
	if done.outcome == TimedOut {
		delta.AttemptsTimedOut = 1
	}
	if done.outcome == Panicked {
		delta.AttemptsPanicked = 1
	}
	c.count(r, delta)
	c.emit(Event{Kind: AttemptFinished, ScheduleID: r.h.id, Run: r.run, Outcome: done.outcome, Err: done.err, Duration: done.duration, At: done.ended.wall})
	if !r.forced && !c.stopping && (done.outcome == Failed || done.outcome == TimedOut) &&
		r.run.Attempt < r.spec.Retry.MaxAttempts && !done.permanent {
		cap := retryCap(r.spec.Retry, r.run.Attempt)
		delay := time.Duration(c.s.jitter(uint64(cap) + 1))
		if at, ok := addTime(done.ended.mono, delay); ok {
			r.eligible = at
			r.retry.at = at
			heap.Push(&c.timers, &r.retry)
			c.count(r, Counters{RetriesScheduled: 1})
			c.emit(Event{Kind: RetryScheduled, ScheduleID: r.h.id, Run: r.run, Outcome: done.outcome, Err: done.err})
			c.update(r)
			return
		}
	}
	c.endRun(r, r.lastAttempt)
	if !r.forced && !c.stopping && r.spec.Kind == FixedDelay && !c.limitReached(r) {
		next, ok := addTime(done.ended.mono, r.spec.Every)
		r.future = ok && (r.spec.Until.IsZero() || next <= r.until)
		if r.future {
			r.tick.at = next
			heap.Push(&c.timers, &r.tick)
		}
	}
	if r.pending && !r.forced && !c.stopping {
		r.eligible = r.nominal
		c.queue(r)
	}
	c.update(r)
	c.maybeFinish(r)
}

func (c *coordinator) observeStop() {
	if c.lifetime.Err() != nil {
		c.s.requestStop(true)
	}
	select {
	case <-c.s.abort:
		c.escalate()
	default:
	}
	select {
	case <-c.s.stop:
		c.beginStop()
	default:
	}
}
func (c *coordinator) endRun(r *registration, result RunResult) {
	r.status.LastRun = result
	r.open = false
	r.run = RunInfo{}
	delta := Counters{}
	switch result.Outcome {
	case Succeeded:
		delta.RunsSucceeded = 1
	case Canceled:
		delta.RunsCanceled = 1
	default:
		delta.RunsFailed = 1
	}
	c.count(r, delta)
	c.emit(Event{Kind: RunFinished, ScheduleID: r.h.id, Run: result.Run, Outcome: result.Outcome, Err: result.Err})
}
func (c *coordinator) limitReached(r *registration) bool {
	return r.spec.MaxRuns > 0 && r.status.Counters.RunsStarted >= r.spec.MaxRuns
}
func (c *coordinator) maybeFinish(r *registration) {
	if r.open || r.pending || r.future {
		return
	}
	reason := r.reason
	if !r.forced {
		switch {
		case r.spec.Kind == Once:
			reason = OneShotFinished
		case c.limitReached(r):
			reason = RunLimitReached
		default:
			reason = UntilReached
		}
	}
	c.finish(r, reason)
}
func (c *coordinator) cancel(r *registration, reason FinishReason, cancelExecution bool) {
	if !r.forced {
		r.forced = true
		r.reason = reason
	}
	r.future = false
	r.pending = false
	c.removeTimers(r)
	c.unqueue(r)
	if r.executing {
		if cancelExecution {
			r.control()
		}
		c.update(r)
		return
	}
	if r.open {
		result := r.lastAttempt
		result.Outcome = Canceled
		result.Err = joinContext(context.Canceled, result.Err)
		result.FinishedAt = c.s.clock.now().wall
		c.endRun(r, result)
	}
	c.update(r)
	c.maybeFinish(r)
}
func (c *coordinator) beginStop() {
	if c.stopping {
		return
	}
	c.stopping = true
	c.status.State = Stopping
	for _, r := range c.regs {
		c.cancel(r, SchedulerStopped, false)
	}
}
func (c *coordinator) escalate() {
	if c.aborted {
		return
	}
	c.aborted = true
	if c.obs != nil {
		c.obs.abort()
	}
	c.beginStop()
	for _, r := range c.regs {
		if r.executing {
			r.control()
		}
	}
}
func (c *coordinator) finish(r *registration, reason FinishReason) {
	r.status.State = Finished
	r.status.CurrentRun = RunInfo{}
	r.status.NextScheduledAt = time.Time{}
	r.status.NextAttemptAt = time.Time{}
	r.status.BufferedAt = time.Time{}
	c.storeHandle(r)
	result := Result{ID: r.h.id, Reason: reason, Counters: r.status.Counters, LastRun: r.status.LastRun}
	r.h.result.Store(&result)
	r.h.owner.Store(nil)
	c.emit(Event{Kind: ScheduleFinished, ScheduleID: r.h.id, Reason: reason, Run: result.LastRun.Run, Outcome: result.LastRun.Outcome, Err: result.LastRun.Err})
	delete(c.regs, r.h.id)
	c.status.ActiveSchedules--
	r.handler = nil
	close(r.h.done)
}
func (c *coordinator) update(r *registration) {
	r.status.CurrentRun = r.run
	r.status.NextScheduledAt = time.Time{}
	r.status.NextAttemptAt = time.Time{}
	r.status.BufferedAt = time.Time{}
	if r.future {
		r.status.NextScheduledAt = r.wall(r.tick.at)
	}
	if r.pending {
		r.status.NextScheduledAt = r.wall(r.nominal)
		if r.open {
			r.status.BufferedAt = r.wall(r.nominal)
		}
	}
	switch {
	case r.forced:
		r.status.State = Canceling
	case r.executing:
		r.status.State = Executing
	case r.ready != nil:
		r.status.State = Ready
	case r.open:
		r.status.State = RetryWaiting
	default:
		r.status.State = Scheduled
	}
	if !r.executing && !r.forced {
		if r.open {
			r.status.NextAttemptAt = r.wall(r.eligible)
		} else if r.pending {
			r.status.NextAttemptAt = r.wall(r.nominal)
		} else if r.future {
			r.status.NextAttemptAt = r.wall(r.tick.at)
		}
	}
	c.storeHandle(r)
}
func (c *coordinator) storeHandle(r *registration) {
	old := r.h.snapshot.Load()
	if old != nil {
		if old.State == Ready {
			c.status.ReadySchedules--
		}
		if old.State == RetryWaiting {
			c.status.WaitingRetries--
		}
		if !old.BufferedAt.IsZero() {
			c.status.BufferedSchedules--
		}
	}
	if r.status.State == Ready {
		c.status.ReadySchedules++
	}
	if r.status.State == RetryWaiting {
		c.status.WaitingRetries++
	}
	if !r.status.BufferedAt.IsZero() {
		c.status.BufferedSchedules++
	}
	snapshot := r.status
	r.h.snapshot.Store(&snapshot)
}
func (c *coordinator) publish() {
	c.status.NextWakeAt = time.Time{}
	if len(c.timers) > 0 {
		d := c.timers[0]
		c.status.NextWakeAt = d.r.wall(d.at)
	}
	snapshot := c.status
	if c.obs != nil {
		snapshot.EventsDropped += c.obs.dropped.Load()
		snapshot.ObserverPanics = c.obs.panics.Load()
	}
	c.s.publish(snapshot)
}
func (c *coordinator) count(r *registration, d Counters) {
	addCounters(&r.status.Counters, d)
	addCounters(&c.status.Counters, d)
}
func addCounters(a *Counters, b Counters) {
	a.RunsStarted += b.RunsStarted
	a.RunsSucceeded += b.RunsSucceeded
	a.RunsFailed += b.RunsFailed
	a.RunsCanceled += b.RunsCanceled
	a.AttemptsDispatched += b.AttemptsDispatched
	a.AttemptsFailed += b.AttemptsFailed
	a.AttemptsTimedOut += b.AttemptsTimedOut
	a.AttemptsPanicked += b.AttemptsPanicked
	a.RetriesScheduled += b.RetriesScheduled
	a.OccurrencesSkipped += b.OccurrencesSkipped
	a.OccurrencesCoalesced += b.OccurrencesCoalesced
}
