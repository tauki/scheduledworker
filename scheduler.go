package scheduledworker

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"
)

// Scheduler must be constructed with New or NewWithOptions, and must not be copied.
type Scheduler struct {
	mu       sync.Mutex // arbitrates startup, stop, and the dispatch boundary only
	state    State
	cfg      config
	clock    clock
	jitter   func(uint64) uint64
	commands chan command
	stop     chan struct{}
	abort    chan struct{}
	done     chan struct{}
	snapshot atomic.Pointer[Status]
}
type command struct {
	ctx     context.Context
	spec    spec
	handler Handler
	id      ScheduleID
	cancel  bool
	reply   chan response
}
type response struct {
	handle *Handle
	err    error
}

// New validates and copies configuration without starting goroutines or timers.
// NewWithOptions provides the same configuration through variadic options.
func New(cfg Config) (*Scheduler, error) {
	return newScheduler(config{workers: cfg.Workers, maxSchedules: cfg.MaxSchedules,
		observer: cfg.Observer, eventBuffer: cfg.EventBuffer})
}

// NewWithOptions applies options and validates configuration without starting goroutines
// or timers. With no options it uses 10 workers, 10,000 schedule slots, and no
// observer. Configuration is immutable after construction.
func NewWithOptions(opts ...Option) (*Scheduler, error) {
	var c config
	for i, opt := range opts {
		if opt == nil {
			return nil, fmt.Errorf("%w: option %d is nil", ErrInvalidConfig, i+1)
		}
		opt(&c)
	}
	return newScheduler(c)
}

func newScheduler(c config) (*Scheduler, error) {
	c, err := normalizeConfig(c)
	if err != nil {
		return nil, err
	}
	s := &Scheduler{cfg: c, clock: systemClock{time.Now()}, jitter: rand.Uint64N,
		commands: make(chan command), stop: make(chan struct{}), abort: make(chan struct{}), done: make(chan struct{})}
	s.snapshot.Store(&Status{State: Created})
	return s, nil
}

// Start starts a terminal, single-use lifecycle. ctx owns its entire lifetime.
func (s *Scheduler) Start(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidArgument
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == Stopping || s.state == Stopped {
		return ErrStopped
	}
	if s.state == Running {
		return ErrAlreadyStarted
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.state = Running
	s.snapshot.Store(&Status{State: Running})
	c := newCoordinator(s, ctx)
	go c.run()
	<-c.started
	return nil
}

// Schedule commits a registration while Running. An error guarantees that no
// registration was committed. ctx controls admission only, not job lifetime.
// The specification is copied. ScheduleWithOptions provides the same scheduling
// policies through variadic options.
func (s *Scheduler) Schedule(ctx context.Context, input Spec, h Handler) (*Handle, error) {
	return s.schedule(ctx, spec(input), h)
}

// ScheduleWithOptions commits a registration while Running. An error guarantees
// that no registration was committed. ctx controls admission only, not job lifetime.
// With no options the handler is scheduled once, immediately. Options apply in
// order, with the last value for a setting winning. A nil option is invalid.
func (s *Scheduler) ScheduleWithOptions(ctx context.Context, h Handler, opts ...ScheduleOption) (*Handle, error) {
	if ctx == nil {
		return nil, ErrInvalidArgument
	}
	var input spec
	for i, opt := range opts {
		if opt == nil {
			return nil, fmt.Errorf("%w: option %d is nil", ErrInvalidSpec, i+1)
		}
		opt(&input)
	}
	return s.schedule(ctx, input, h)
}

func (s *Scheduler) schedule(ctx context.Context, input spec, h Handler) (*Handle, error) {
	if ctx == nil {
		return nil, ErrInvalidArgument
	}
	spec, err := normalizeSpec(input, h)
	if err != nil {
		return nil, err
	}
	if _, _, err = resolveSpec(spec, s.clock.now()); err != nil {
		return nil, err
	}
	s.mu.Lock()
	state := s.state
	s.mu.Unlock()
	if state == Created {
		return nil, ErrNotRunning
	}
	if state != Running {
		return nil, ErrStopped
	}
	cmd := command{ctx: ctx, spec: spec, handler: h, reply: make(chan response, 1)}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.stop:
		return nil, ErrStopped
	case s.commands <- cmd:
		// After handoff, only the acknowledgment determines acceptance.
		r := <-cmd.reply
		return r.handle, r.err
	}
}

// Shutdown stops admission and dispatch, abandons undispatched work, and waits
// for owned goroutines. Expiration escalates shared cancellation, then returns.
func (s *Scheduler) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidArgument
	}
	s.requestStop(ctx.Err() != nil)
	select {
	case <-s.done:
		return nil
	default:
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		s.requestStop(true)
		return ctx.Err()
	}
}
func (s *Scheduler) requestStop(abort bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == Stopped {
		return
	}
	if s.state == Created {
		s.state = Stopped
		s.snapshot.Store(&Status{State: Stopped})
		close(s.stop)
		close(s.done)
		return
	}
	if s.state == Running {
		s.state = Stopping
		close(s.stop)
		// Publish the admission gate immediately, even when an already-expired
		// Shutdown context returns before the coordinator processes the signal.
		for {
			old := s.snapshot.Load()
			if old.State >= Stopping {
				break
			}
			next := *old
			next.State = Stopping
			if s.snapshot.CompareAndSwap(old, &next) {
				break
			}
		}
	}
	if abort {
		select {
		case <-s.abort:
		default:
			close(s.abort)
		}
	}
}
func (s *Scheduler) Done() <-chan struct{} { return s.done }
func (s *Scheduler) Status() Status        { return *s.snapshot.Load() }

func (s *Scheduler) publish(status Status) {
	for {
		old := s.snapshot.Load()
		// A coordinator publication racing the lifecycle gate cannot make
		// readiness appear Running again after stop has been acknowledged.
		if old.State > status.State {
			status.State = old.State
		}
		if s.snapshot.CompareAndSwap(old, &status) {
			return
		}
	}
}

// Handle retains snapshots/results, not its retired handler closure.
type Handle struct {
	id       ScheduleID
	owner    atomic.Pointer[Scheduler]
	snapshot atomic.Pointer[ScheduleStatus]
	result   atomic.Pointer[Result]
	done     chan struct{}
}

func (h *Handle) ID() ScheduleID         { return h.id }
func (h *Handle) Done() <-chan struct{}  { return h.done }
func (h *Handle) Status() ScheduleStatus { return *h.snapshot.Load() }

// Cancel acknowledges removal of future attempts; running handlers must return
// before Done closes and admission capacity is released.
func (h *Handle) Cancel(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidArgument
	}
	s := h.owner.Load()
	if s == nil {
		return nil
	}
	cmd := command{ctx: ctx, id: h.id, cancel: true, reply: make(chan response, 1)}
	select {
	case <-h.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return nil
	case s.commands <- cmd:
		return (<-cmd.reply).err
	}
}

// Wait returns execution errors inside Result.LastRun, not as its method error.
func (h *Handle) Wait(ctx context.Context) (Result, error) {
	if ctx == nil {
		return Result{}, ErrInvalidArgument
	}
	select {
	case <-h.done:
		return *h.result.Load(), nil
	default:
	}
	select {
	case <-h.done:
		return *h.result.Load(), nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}
func scheduleID(n uint64) ScheduleID { return ScheduleID(fmt.Sprintf("s%d", n)) }
func runID(n uint64) RunID           { return RunID(fmt.Sprintf("r%d", n)) }
