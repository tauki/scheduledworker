package scheduledworker

import "time"

type TaskOpt func(*taskOpt)

type taskOpt struct {
	repeat int
	every  time.Duration
	until  time.Time
}

const Forever = -1

func Repeat(n int) TaskOpt {
	return func(o *taskOpt) {
		o.repeat = n
	}
}

func RepeatForever() TaskOpt {
	return Repeat(Forever)
}

func Every(d time.Duration) TaskOpt {
	return func(o *taskOpt) {
		o.every = d
		if o.repeat == 0 {
			o.repeat = Forever
		}
	}
}

func Until(t time.Time) TaskOpt {
	return func(o *taskOpt) {
		o.until = t
	}
}
