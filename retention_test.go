package scheduledworker

import (
	"bytes"
	"context"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"weak"
)

type retainedPayload struct{ bytes [4096]byte }

func submitPayload(t *testing.T, s *Scheduler) (*Handle, weak.Pointer[retainedPayload]) {
	p := &retainedPayload{}
	ref := weak.Make(p)
	h := schedule(t, s, Spec{}, func(context.Context, RunInfo) error { p.bytes[0]++; return nil })
	return h, ref
}
func TestTerminalHandlerReferencesReleasedWhileWorkerIdle(t *testing.T) {
	s := newTestScheduler(t, Config{Workers: 1})
	defer shutdown(t, s)
	h, ref := submitPayload(t, s)
	result(t, h)
	for i := 0; i < 3 && ref.Value() != nil; i++ {
		runtime.GC()
	}
	if ref.Value() != nil {
		t.Fatal("completed handler retained by idle scheduler")
	}
	runtime.KeepAlive(h)
	runtime.KeepAlive(s)
}
func TestOwnedGoroutinesRemainBoundedAndExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newTestScheduler(t, Config{Workers: 3, MaxSchedules: 1000, Observer: func(context.Context, Event) {}})
		for i := 0; i < 1000; i++ {
			schedule(t, s, Spec{At: time.Now().Add(time.Hour)}, func(context.Context, RunInfo) error { return nil })
		}
		synctest.Wait()
		check := func(workers, coordinators, observers int) {
			var buf bytes.Buffer
			_ = pprof.Lookup("goroutine").WriteTo(&buf, 2)
			stacks := buf.String()
			for signature, want := range map[string]int{"scheduledworker/v2.worker(": workers, "scheduledworker/v2.(*coordinator).run(": coordinators, "scheduledworker/v2.(*eventDispatcher).run(": observers} {
				if got := strings.Count(stacks, signature); got != want {
					t.Fatalf("%s: got %d want %d", signature, got, want)
				}
			}
		}
		check(3, 1, 1)
		shutdown(t, s)
		synctest.Wait()
		check(0, 0, 0)
	})
}
