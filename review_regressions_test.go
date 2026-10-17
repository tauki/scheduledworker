package scheduledworker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHookTaskSnapshotsRetainTheirAttempt(t *testing.T) {
	w := New().Start()
	defer w.Stop()
	var calls atomic.Int64
	snapshots := make(chan *Task, 2)
	requireSubmit(t, w, Task{FnErr: func(context.Context) error {
		if calls.Add(1) == 1 {
			return errors.New("retry")
		}
		return nil
	}}, WithRetry(RetryPolicy{MaxAttempts: 2}), WithOnStart(func(task *Task) { snapshots <- task }))
	waitFor(t, func() bool { s := w.Status(); return s.Completed == 1 && s.InFlight == 0 && s.QueueLen == 0 })
	for _, want := range []int{1, 2} {
		select {
		case snapshot := <-snapshots:
			if got := snapshot.Spec().Attempt; got != want {
				t.Errorf("retained hook attempt=%d want=%d", got, want)
			}
		default:
			t.Fatal("hook snapshot missing")
		}
	}
}

func TestBusinessHoursAcrossHalfHourDSTGap(t *testing.T) {
	location, err := time.LoadLocation("Australia/Lord_Howe")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 4, 1, 45, 0, 0, location)
	next := businessTime(at, location, 2, 3).In(location)
	if next.Day() != 4 || next.Hour() != 2 || next.Minute() != 30 {
		t.Fatalf("next=%v want same-day 02:30", next)
	}
}

func TestBusinessHoursAcrossQuarterHourDSTBoundary(t *testing.T) {
	location, err := time.LoadLocation("Pacific/Chatham")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 27, 2, 30, 0, 0, location)
	want := time.Date(2026, 9, 27, 3, 45, 0, 0, location)
	if got := businessTime(at, location, 3, 4); !got.Equal(want) {
		t.Fatalf("next=%v want=%v", got.In(location), want)
	}
}

func TestBusinessHoursUsesFirstRepeatedWindow(t *testing.T) {
	location, err := time.LoadLocation("Pacific/Chatham")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 4, 5, 2, 30, 0, 0, location)
	want := at.Add(30 * time.Minute) // First 03:00, before the clock goes back.
	if got := businessTime(at, location, 3, 4); !got.Equal(want) {
		t.Fatalf("next=%v want first window=%v", got.In(location), want)
	}
}

func TestBusinessHoursReopensDuringRepeatedWindow(t *testing.T) {
	location, err := time.LoadLocation("Pacific/Chatham")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 4, 5, 2, 30, 0, 0, location).Add(time.Hour)
	want := at.Add(15 * time.Minute) // 03:45 turns back into 02:45.
	if got := businessTime(at, location, 2, 3); !got.Equal(want) {
		t.Fatalf("next=%v want reopened window=%v", got.In(location), want)
	}
}

func TestBusinessHoursAcrossSecondPrecisionOffsetChange(t *testing.T) {
	location, err := time.LoadLocation("Africa/Monrovia")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(1972, 1, 6, 23, 50, 0, 0, location)
	want := time.Date(1972, 1, 7, 0, 44, 30, 0, time.UTC)
	if got := businessTime(at, location, 0, 1); !got.Equal(want) {
		t.Fatalf("next=%v want partial window=%v", got.In(location), want)
	}
}

func TestBusinessHoursAcrossSkippedCivilDate(t *testing.T) {
	location, err := time.LoadLocation("Pacific/Apia")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2011, 12, 29, 23, 30, 0, 0, location)
	want := time.Date(2011, 12, 31, 9, 0, 0, 0, location)
	if got := businessTime(at, location, 9, 17); !got.Equal(want) {
		t.Fatalf("next=%v want=%v", got.In(location), want)
	}
}

func TestBusinessHoursAcrossExtrapolatedLeapYearEnd(t *testing.T) {
	location, err := time.LoadLocation("Europe/Oslo")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2040, 12, 31, 20, 0, 0, 0, location)
	want := time.Date(2041, 1, 1, 9, 0, 0, 0, location)
	if got := businessTime(at, location, 9, 17); !got.Equal(want) {
		t.Fatalf("next=%v want=%v", got.In(location), want)
	}
}

func TestRetainedHookSnapshotCanBeReadDuringRetries(t *testing.T) {
	w := New().Start()
	defer w.Stop()
	first, release := make(chan *Task, 1), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var calls atomic.Int64
	requireSubmit(t, w, Task{FnErr: func(context.Context) error {
		n := calls.Add(1)
		if n == 1 {
			<-release
		}
		if n <= 20 {
			return errors.New("retry")
		}
		return nil
	}}, WithRetry(RetryPolicy{MaxAttempts: 21}), WithOnStart(func(task *Task) {
		if task.Spec().Attempt == 1 {
			first <- task
		}
	}))
	var snapshot *Task
	select {
	case snapshot = <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("first attempt snapshot missing")
	}
	reading, stop, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var changed atomic.Bool
	var stopOnce sync.Once
	defer stopOnce.Do(func() { close(stop) })
	go func() {
		defer close(finished)
		_ = snapshot.Spec()
		close(reading)
		for {
			select {
			case <-stop:
				return
			default:
				if snapshot.Spec().Attempt != 1 {
					changed.Store(true)
				}
				runtime.Gosched()
			}
		}
	}()
	<-reading
	releaseOnce.Do(func() { close(release) })
	waitFor(t, func() bool { s := w.Status(); return s.Completed == 1 && s.InFlight == 0 && s.QueueLen == 0 })
	stopOnce.Do(func() { close(stop) })
	waitSignal(t, finished)
	if changed.Load() {
		t.Error("retained hook metadata changed during later attempts")
	}
}

func TestStatusHandlerReportsSerializationFailure(t *testing.T) {
	w := New()
	defer w.Stop()
	w.Submit(Task{At: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), Fn: func() {}})
	response := httptest.NewRecorder()
	NewStatusHandler(w).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/status", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%q want 500 for an unencodable snapshot", response.Code, response.Body.String())
	}
}

func TestPriorityQueueRejectsNilTasks(t *testing.T) {
	queue := NewPriorityQueue()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("nil task accepted without an argument panic")
			}
		}()
		queue.Push(nil)
	}()
	if queue.Len() != 0 || queue.Peek() != nil {
		t.Fatal("nil task left a nonempty queue with no dispatchable head")
	}
}
