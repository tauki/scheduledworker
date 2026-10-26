package scheduledworker_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	sw "github.com/tauki/scheduledworker/v2"
)

// Existing interfaces, function values, and method expressions must compile
// without adapting to the additional options API.
type schedulerV200 interface {
	Start(context.Context) error
	Schedule(context.Context, sw.Spec, sw.Handler) (*sw.Handle, error)
	Shutdown(context.Context) error
	Status() sw.Status
	Done() <-chan struct{}
}

var (
	_ func(sw.Config) (*sw.Scheduler, error)                                        = sw.New
	_ func(*sw.Scheduler, context.Context, sw.Spec, sw.Handler) (*sw.Handle, error) = (*sw.Scheduler).Schedule
	_ schedulerV200                                                                 = (*sw.Scheduler)(nil)
)

// Unchanged v2.0.0 consumer example; only its test name is disambiguated.
func ExampleScheduler_v200() {
	s, err := sw.New(sw.Config{Workers: 4, MaxSchedules: 1000})
	if err != nil {
		panic(err)
	}
	if err = s.Start(context.Background()); err != nil {
		panic(err)
	}
	h, err := s.Schedule(context.Background(), sw.Spec{Name: "refresh", Kind: sw.FixedDelay, At: time.Now(), Every: time.Minute, MaxRuns: 1, Retry: sw.RetryPolicy{MaxAttempts: 3}}, func(ctx context.Context, r sw.RunInfo) error { return nil })
	if err != nil {
		panic(err)
	}
	result, err := h.Wait(context.Background())
	if err != nil {
		panic(err)
	}
	if err = s.Shutdown(context.Background()); err != nil {
		panic(err)
	}
	fmt.Println(result.Counters.RunsSucceeded)
	// Output: 1
}

func TestV200AndOptionsAPIsShareAdmissionAndLifecycle(t *testing.T) {
	for _, tt := range []struct {
		name string
		new  func() (*sw.Scheduler, error)
	}{
		{"config", func() (*sw.Scheduler, error) { return sw.New(sw.Config{Workers: 1, MaxSchedules: 1}) }},
		{"options", func() (*sw.Scheduler, error) { return sw.NewWithOptions(sw.WithWorkers(1), sw.WithMaxSchedules(1)) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s, err := tt.new()
			if err != nil {
				t.Fatal(err)
			}
			defer s.Shutdown(ctx)
			var old schedulerV200 = s
			if err = old.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			input := sw.Spec{At: time.Now().Add(time.Hour)}
			future, err := old.Schedule(ctx, input, func(context.Context, sw.RunInfo) error {
				t.Error("mutating the caller's Spec changed its registration")
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			input.At = time.Now()
			if h, err := s.ScheduleWithOptions(ctx, func(context.Context, sw.RunInfo) error { return nil }); h != nil || !errors.Is(err, sw.ErrCapacity) {
				t.Fatal("APIs do not share capacity", h, err)
			}
			if err = future.Cancel(ctx); err != nil {
				t.Fatal(err)
			}
			if r, err := future.Wait(ctx); err != nil || r.Reason != sw.ExplicitlyCanceled || r.LastRun.Outcome != sw.NoOutcome {
				t.Fatal(r, err)
			}
			h, err := s.ScheduleWithOptions(ctx, func(_ context.Context, run sw.RunInfo) error {
				if run.Attempt == 1 {
					return errors.New("retry")
				}
				return nil
			}, sw.WithFixedDelay(time.Millisecond), sw.WithMaxRuns(2),
				sw.WithRetry(sw.RetryPolicy{MaxAttempts: 2, InitialBackoff: time.Nanosecond, MaxBackoff: time.Nanosecond}))
			if err != nil {
				t.Fatal(err)
			}
			if r, err := h.Wait(ctx); err != nil || r.Reason != sw.RunLimitReached || r.Counters.RunsSucceeded != 2 || r.Counters.AttemptsDispatched != 4 {
				t.Fatal(r, err)
			}
			if err = old.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			if st := old.Status(); st.State != sw.Stopped || st.ActiveSchedules != 0 || st.Accepted != 2 {
				t.Fatal(st)
			}
		})
	}
}

func TestV200ValidationIsPreserved(t *testing.T) {
	for _, cfg := range []sw.Config{{Workers: -1}, {MaxSchedules: -1}, {EventBuffer: -1}, {Workers: 2, MaxSchedules: 1}} {
		if s, err := sw.New(cfg); s != nil || !errors.Is(err, sw.ErrInvalidConfig) {
			t.Fatal(s, err)
		}
	}
	s, err := sw.New(sw.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown(context.Background())
	noop := func(context.Context, sw.RunInfo) error { return nil }
	for _, input := range []sw.Spec{{Kind: 99}, {Every: 1}, {MaxRuns: 2}, {Timeout: -1}} {
		if h, err := s.Schedule(context.Background(), input, noop); h != nil || !errors.Is(err, sw.ErrInvalidSpec) {
			t.Fatal(h, err)
		}
	}
	if _, err = s.Schedule(nil, sw.Spec{}, noop); !errors.Is(err, sw.ErrInvalidArgument) {
		t.Fatal(err)
	}
	if _, err = s.Schedule(context.Background(), sw.Spec{}, nil); !errors.Is(err, sw.ErrInvalidSpec) {
		t.Fatal(err)
	}
	if _, err = s.Schedule(context.Background(), sw.Spec{}, noop); !errors.Is(err, sw.ErrNotRunning) {
		t.Fatal(err)
	}
}
