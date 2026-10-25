package scheduledworker

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

type advancingClock struct {
	clock
	advance atomic.Int64
}

func (c *advancingClock) now() instant {
	n := c.clock.now()
	d := time.Duration(c.advance.Load())
	n.mono += int64(d)
	n.wall = n.wall.Add(d)
	return n
}

// Advance the private elapsed clock once, so all registrations become due
// together without a wall-clock sleep proportional to the requested backlog.
func BenchmarkWorkloads(b *testing.B) {
	for _, n := range []int{10, 1000, 10000} {
		for _, mode := range []string{"burst", "slow", "retry", "cancel"} {
			b.Run(fmt.Sprintf("%s/%d", mode, n), func(b *testing.B) {
				b.ReportAllocs()
				var totalLag atomic.Int64
				var calls atomic.Int64
				for k := 0; k < b.N; k++ {
					s, _ := NewWithOptions(WithWorkers(10), WithMaxSchedules(n))
					clock := &advancingClock{clock: s.clock}
					s.clock = clock
					s.jitter = func(uint64) uint64 { return 0 }
					_ = s.Start(background)
					at := time.Now().Add(time.Hour)
					handles := make([]*Handle, 0, n)
					opts := []ScheduleOption{WithAt(at)}
					if mode == "retry" {
						opts = append(opts, WithRetry(RetryPolicy{MaxAttempts: 2}))
					}
					for i := 0; i < n; i++ {
						h, err := s.ScheduleWithOptions(background, func(_ context.Context, r RunInfo) error {
							if r.Attempt == 1 {
								totalLag.Add(int64(clock.now().wall.Sub(r.ScheduledAt)))
								calls.Add(1)
							}
							if mode == "slow" {
								time.Sleep(100 * time.Microsecond)
							}
							if mode == "retry" && r.Attempt == 1 {
								return context.DeadlineExceeded
							}
							return nil
						}, opts...)
						if err != nil {
							b.Fatal(err)
						}
						handles = append(handles, h)
					}
					if mode == "cancel" {
						for _, h := range handles {
							_ = h.Cancel(background)
						}
					} else {
						clock.advance.Store(int64(time.Hour))
						cmd := command{ctx: background, cancel: true, reply: make(chan response, 1)}
						s.commands <- cmd
						<-cmd.reply
						for _, h := range handles {
							_, _ = h.Wait(background)
						}
					}
					_ = s.Shutdown(background)
				}
				if count := calls.Load(); count > 0 {
					b.ReportMetric(float64(totalLag.Load())/float64(count), "entry-lag-ns/run")
				}
			})
		}
	}
}
func BenchmarkRetainedRegistrations(b *testing.B) {
	for _, n := range []int{10, 1000, 10000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			var bytes uint64
			for k := 0; k < b.N; k++ {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				s, _ := NewWithOptions(WithWorkers(10), WithMaxSchedules(n))
				_ = s.Start(background)
				for i := 0; i < n; i++ {
					_, err := s.ScheduleWithOptions(background, func(context.Context, RunInfo) error { return nil }, WithAt(time.Now().Add(time.Hour)))
					if err != nil {
						b.Fatal(err)
					}
				}
				// The acknowledgment precedes the scheduler snapshot publication.
				cmd := command{ctx: background, cancel: true, reply: make(chan response, 1)}
				s.commands <- cmd
				<-cmd.reply
				runtime.GC()
				runtime.ReadMemStats(&after)
				if after.HeapAlloc > before.HeapAlloc {
					bytes += after.HeapAlloc - before.HeapAlloc
				}
				_ = s.Shutdown(background)
			}
			b.ReportMetric(float64(bytes)/float64(b.N*n), "retained-B/registration")
		})
	}
}
