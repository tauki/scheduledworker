package scheduledworker

import (
	"sync"
	"time"
)

const (
	defaultScheduleDuration = time.Second * 30
)

type Worker interface {
	Submit(Task, ...TaskOpt)
	SetDuration(time.Duration) Worker
	SetMaxWorker(int) Worker
	Start() Worker
	Stop()
}

type Task struct {
	At  time.Time
	Fn  func()
	opt *taskOpt
}

type worker struct {
	maxWorker int
	tasks     []Task
	close     chan bool
	closed    bool
	ticker    *time.Ticker
	sync.Mutex
	sync.Once
}

var _ Worker = &worker{}

func New() Worker {
	return &worker{
		tasks:     make([]Task, 0),
		close:     make(chan bool),
		maxWorker: 10,
		closed:    false,
		ticker:    time.NewTicker(defaultScheduleDuration),
	}
}

func (w *worker) Submit(task Task, opts ...TaskOpt) {
	opt := new(taskOpt)
	for _, fn := range opts {
		fn(opt)
	}
	task.opt = opt

	if task.At.IsZero() {
		task.At = time.Now()
		if task.opt.every != 0 {
			task.At.Add(task.opt.every)
		}
	}

	w.insertTask(task)
}

func (w *worker) Start() Worker {

	w.Do(func() {
		go func() {
			for {
				select {
				case <-w.close:
					w.closed = true
				case <-w.ticker.C:
					w.process(w.getTasks())
					if w.closed && len(w.tasks) == 0 {
						w.ticker.Stop()
						w.close <- true
						return
					}
				}
			}
		}()
	})
	return w
}

func (w *worker) SetDuration(duration time.Duration) Worker {
	w.ticker.Reset(duration)
	return w
}

func (w *worker) SetMaxWorker(count int) Worker {
	w.Lock()
	defer w.Unlock()
	w.maxWorker = count
	return w
}

func (w *worker) insertTask(task Task) {
	if w.closed {
		return
	}

	w.Lock()
	defer w.Unlock()

	i := 0
	for ; i < len(w.tasks); i++ {
		if w.tasks[i].At.After(task.At) {
			break
		}
	}

	if i == len(w.tasks) {
		w.tasks = append(w.tasks, task)
	} else {
		w.tasks = append(w.tasks[:i+1], w.tasks[i:]...)
		w.tasks[i] = task
	}
}

func (w *worker) getTasks() []Task {
	tasks := make([]Task, 0)
	count := 0
	w.Lock()
	defer w.Unlock()

	for i := 0; i < len(w.tasks); i++ {
		if time.Now().After(w.tasks[i].At) {
			count++
			continue
		}

		break
	}

	tasks = append(tasks, w.tasks[:count]...)
	w.tasks = w.tasks[count:]
	return tasks
}

func (w *worker) process(tasks []Task) {
	wg := sync.WaitGroup{}
	count := 0
	for count < len(tasks) {
		for i := 0; i < w.maxWorker && count < len(tasks); i++ {
			wg.Add(1)
			go func(fn func()) {
				defer wg.Done()
				defer func() {
					recover()
				}()
				fn()
			}(tasks[count].Fn)
			w.postProcess(tasks[count])
			count++
		}
		wg.Wait()
	}
}

func (w *worker) postProcess(task Task) {
	if task.opt.repeat != Forever && task.opt.repeat != 0 {
		task.opt.repeat--
	}

	if task.opt.repeat == 0 {
		return
	}

	task.At = time.Now().Add(task.opt.every)

	if !task.opt.until.IsZero() && task.At.After(task.opt.until) {
		return
	}

	w.insertTask(task)
}

func (w *worker) Stop() {
	w.close <- true
	<-w.close
	close(w.close)
}
