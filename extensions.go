package scheduledworker

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrStopped indicates closed admission.
	ErrStopped = errors.New("scheduledworker: worker is stopping or stopped")
	// ErrInvalidTask indicates invalid handlers or options.
	ErrInvalidTask = errors.New("scheduledworker: invalid task")
	// ErrNextCalledTwice indicates repeated middleware continuation.
	ErrNextCalledTwice = errors.New("scheduledworker: next called more than once")
)

// ScheduleMode controls the next occurrence of a repeating task.
type ScheduleMode int

const (
	FixedDelay ScheduleMode = iota
	// FixedRate retains the original grid. After a slow run, only the latest
	// missed occurrence is eligible; earlier missed ticks are discarded.
	FixedRate
)

// RetryPolicy bounds attempts per occurrence, including the first attempt.
// Zero and one disable retry. Exhaustion consumes one Repeat occurrence and
// permits future occurrences. Retry waiting does not occupy an execution slot.
// Negative custom backoff delays normalize to zero. A backoff panic is reported
// to OnError and exhausts the current occurrence, allowing future occurrences.
type RetryPolicy struct {
	MaxAttempts  int
	Backoff      func(attempt int) time.Duration // retry number, starting at 1
	RetryOnPanic bool
}

// Interceptor wraps a synchronous phase. Call next zero or one times,
// synchronously, and do not retain its task. Enqueue/reschedule middleware may
// defer At or drop work but cannot bypass eligibility or repeat/cutoff checks.
// Run middleware wraps the handler. Callbacks must not call Stop or wait for
// work queued behind the same worker. They may run concurrently.
type Interceptor func(context.Context, *Task, Next) error

// Next continues a middleware phase.
type Next func(context.Context, *Task) error

// Spec is copied at admission. Maps/slices are copied; objects inside Attrs
// remain caller-owned and must be immutable or synchronized. Attempt is supplied
// in callback snapshots only. Context nil means context.Background().
type Spec struct {
	At           time.Time
	Every        time.Duration
	Until        time.Time
	Repeat       int
	Mode         ScheduleMode
	Labels       map[string]string
	Attrs        map[string]any
	OnPanic      func(*Task, any)
	OnError      func(*Task, error)
	OnComplete   func(*Task)
	OnStart      func(*Task)
	Retry        RetryPolicy
	Context      context.Context
	Timeout      time.Duration
	Deadline     time.Time
	CancelOnStop bool
	Attempt      int
	Key          string
	OnEnqueue    []Interceptor
	OnRun        []Interceptor
	OnReschedule []Interceptor
	collectors   []MetricsCollector
	err          error
}

// NewSpec returns a one-shot, fixed-delay specification.
func NewSpec() *Spec { return &Spec{Labels: make(map[string]string), Attrs: make(map[string]any)} }

// Option configures a specification before admission.
type Option interface{ Apply(*Spec) }

// OptionFunc adapts custom configuration. It runs synchronously in submission
// and must not retain the supplied Spec.
type OptionFunc func(*Spec)

func (f OptionFunc) Apply(s *Spec) {
	if f != nil {
		f(s)
	}
}

// Spec returns a defensive snapshot, or nil for a legacy task.
func (t *Task) Spec() *Spec {
	if t == nil || t.spec == nil {
		return nil
	}
	s := cloneSpec(t.spec)
	if t.state != nil {
		s.Attempt = t.state.attempt
	}
	return s
}

func cloneSpec(s *Spec) *Spec {
	c := *s
	c.Labels = make(map[string]string, len(s.Labels))
	for k, v := range s.Labels {
		c.Labels[k] = v
	}
	c.Attrs = make(map[string]any, len(s.Attrs))
	for k, v := range s.Attrs {
		c.Attrs[k] = v
	}
	c.OnEnqueue = append([]Interceptor(nil), s.OnEnqueue...)
	c.OnRun = append([]Interceptor(nil), s.OnRun...)
	c.OnReschedule = append([]Interceptor(nil), s.OnReschedule...)
	c.collectors = append([]MetricsCollector(nil), s.collectors...)
	return &c
}

func validateSpec(s *Spec) error {
	if s.err != nil {
		return s.err
	}
	if s.Every < 0 || s.Repeat < Forever || s.Timeout < 0 || s.Retry.MaxAttempts < 0 {
		return fmt.Errorf("%w: negative scheduling value", ErrInvalidTask)
	}
	if s.Repeat == Forever && s.Every == 0 {
		return fmt.Errorf("%w: unlimited repetition requires an interval", ErrInvalidTask)
	}
	if s.Mode != FixedDelay && s.Mode != FixedRate {
		return fmt.Errorf("%w: unknown schedule mode", ErrInvalidTask)
	}
	if s.Mode == FixedRate && s.Every <= 0 {
		return fmt.Errorf("%w: fixed rate requires an interval", ErrInvalidTask)
	}
	return nil
}

// SubmitWithOptions registers exactly one of Fn and FnErr. Explicit Task.At
// takes precedence over Spec.At. Invalid options and closed admission return
// errors. A middleware drop or a time beyond Until returns nil without admission.
func (w *worker) SubmitWithOptions(task Task, opts ...Option) error {
	w.Lock()
	closed := w.closed
	w.Unlock()
	if closed {
		return ErrStopped
	}
	if (task.Fn == nil) == (task.FnErr == nil) {
		return fmt.Errorf("%w: supply exactly one of Fn and FnErr", ErrInvalidTask)
	}
	s := NewSpec()
	for _, o := range opts {
		if o != nil {
			o.Apply(s)
		}
	}
	s = cloneSpec(s)
	if err := validateSpec(s); err != nil {
		return err
	}
	if s.Context == nil {
		s.Context = context.Background()
	}
	if task.At.IsZero() {
		task.At = s.At
	}
	if task.At.IsZero() {
		task.At = time.Now().Add(s.Every)
	}
	task.opt, task.spec = nil, s
	task.dispatching = false
	task.state = &taskState{remaining: s.Repeat, attempt: 1, nominal: task.At}
	keep, err := transform(s.OnEnqueue, s.Context, &task, task.At)
	if err != nil {
		return err
	}
	if !keep || (!s.Until.IsZero() && task.At.After(s.Until)) {
		return nil
	}
	if !w.enqueue(&task) {
		return ErrStopped
	}
	return nil
}

// WithEvery sets the interval and implies unlimited repeats unless a nonzero
// repeat count was supplied. WithRepeat(0) after it selects a one-shot.
func WithEvery(d time.Duration) Option {
	return OptionFunc(func(s *Spec) {
		s.Every = d
		if s.Repeat == 0 {
			s.Repeat = Forever
		}
		if d <= 0 {
			s.err = fmt.Errorf("%w: interval must be positive", ErrInvalidTask)
		}
	})
}

// WithRepeat sets the number of occurrences, including the first; zero selects one-shot.
func WithRepeat(n int) Option { return OptionFunc(func(s *Spec) { s.Repeat = n }) }

// WithUntil sets an inclusive bound on queued eligibility times; zero removes it.
func WithUntil(t time.Time) Option { return OptionFunc(func(s *Spec) { s.Until = t }) }

// WithFixedRate selects the original time grid with at most one latest missed tick.
func WithFixedRate() Option { return OptionFunc(func(s *Spec) { s.Mode = FixedRate }) }

// WithFixedDelay measures the next interval from handler completion.
func WithFixedDelay() Option { return OptionFunc(func(s *Spec) { s.Mode = FixedDelay }) }

// WithRetry sets the bounded per-occurrence retry policy.
func WithRetry(r RetryPolicy) Option { return OptionFunc(func(s *Spec) { s.Retry = r }) }

// WithTimeout sets a cooperative timeout for each attempt; zero disables it.
func WithTimeout(d time.Duration) Option { return OptionFunc(func(s *Spec) { s.Timeout = d }) }

// WithDeadline sets an absolute context deadline; zero disables it.
func WithDeadline(t time.Time) Option { return OptionFunc(func(s *Spec) { s.Deadline = t }) }

// WithContext sets the parent context; nil uses context.Background().
func WithContext(ctx context.Context) Option { return OptionFunc(func(s *Spec) { s.Context = ctx }) }

// WithCancelOnStop cancels attempt contexts when shutdown begins.
func WithCancelOnStop() Option { return OptionFunc(func(s *Spec) { s.CancelOnStop = true }) }

// WithOnPanic observes recovered execution panics.
func WithOnPanic(h func(*Task, any)) Option { return OptionFunc(func(s *Spec) { s.OnPanic = h }) }

// WithOnError observes handler and retry/reschedule policy errors.
func WithOnError(h func(*Task, error)) Option { return OptionFunc(func(s *Spec) { s.OnError = h }) }

// WithOnComplete observes successful attempts.
func WithOnComplete(h func(*Task)) Option { return OptionFunc(func(s *Spec) { s.OnComplete = h }) }

// WithOnStart observes each attempt before its execution middleware.
func WithOnStart(h func(*Task)) Option { return OptionFunc(func(s *Spec) { s.OnStart = h }) }

// WithLabels copies labels when applied.
func WithLabels(labels map[string]string) Option {
	return OptionFunc(func(s *Spec) {
		if s.Labels == nil {
			s.Labels = make(map[string]string)
		}
		for k, v := range labels {
			s.Labels[k] = v
		}
	})
}

// WithAttributes copies the map; its values must be immutable or synchronized.
func WithAttributes(attrs map[string]any) Option {
	return OptionFunc(func(s *Spec) {
		if s.Attrs == nil {
			s.Attrs = make(map[string]any)
		}
		for k, v := range attrs {
			s.Attrs[k] = v
		}
	})
}

// WithOnEnqueue appends middleware that may defer or drop initial admission.
func WithOnEnqueue(i Interceptor) Option {
	return OptionFunc(func(s *Spec) { s.OnEnqueue = append(s.OnEnqueue, i) })
}

// WithOnRun appends execution middleware.
func WithOnRun(i Interceptor) Option {
	return OptionFunc(func(s *Spec) { s.OnRun = append(s.OnRun, i) })
}

// WithOnReschedule appends middleware that may defer or drop later occurrences.
func WithOnReschedule(i Interceptor) Option {
	return OptionFunc(func(s *Spec) { s.OnReschedule = append(s.OnReschedule, i) })
}
