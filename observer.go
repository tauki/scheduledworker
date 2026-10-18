package scheduledworker

import (
	"context"
	"sync/atomic"
)

type eventDispatcher struct {
	fn              Observer
	queue           chan Event
	ctx             context.Context
	cancel          context.CancelFunc
	done            chan struct{}
	dropped, panics atomic.Uint64
}

func newEventDispatcher(fn Observer, size int) *eventDispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &eventDispatcher{fn: fn, queue: make(chan Event, size), ctx: ctx, cancel: cancel, done: make(chan struct{})}
}
func (o *eventDispatcher) run() {
	defer close(o.done)
	defer o.cancel()
	for event := range o.queue {
		if o.ctx.Err() != nil {
			o.dropped.Add(1)
			continue
		}
		func() {
			normal := false
			defer func() {
				if !normal {
					_ = recover()
					o.panics.Add(1)
				}
			}()
			o.fn(o.ctx, event)
			normal = true
		}()
	}
}
func (o *eventDispatcher) seal()  { close(o.queue) }
func (o *eventDispatcher) abort() { o.cancel() }
func (c *coordinator) emit(event Event) {
	if c.obs == nil {
		return
	}
	if event.At.IsZero() {
		event.At = c.s.clock.now().wall
	}
	if c.aborted {
		c.status.EventsDropped++
		return
	}
	select {
	case c.obs.queue <- event:
	default:
		c.status.EventsDropped++
	}
}
