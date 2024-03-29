package scheduledworker

import (
	"sync"
	"time"
)

const (
	defaultScheduleDuration = time.Second * 30
	defaultMaxWorker        = 10
)

type Worker interface {
	Submit(Task, ...TaskOpt)
	SetDuration(time.Duration) Worker
	SetMaxWorker(int) Worker
	SetQueue(Queue) Worker
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
	queue     Queue
	close     chan bool
	closed    bool
	ticker    *time.Ticker
	sync.Mutex
	sync.Once
}

var _ Worker = &worker{}

func New() Worker {
	return &worker{
		queue:     NewPriorityQueue(),
		close:     make(chan bool),
		maxWorker: defaultMaxWorker,
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

	w.insertTask(&task)
}

func (w *worker) Start() Worker {
	w.Do(func() { go w.run() })
	return w
}

func (w *worker) run() {
	for {
		select {
		case <-w.close:
			w.closed = true
		case <-w.ticker.C:
			w.process(w.getTasks())
			if w.closed && w.queue.Len() == 0 {
				w.ticker.Stop()
				w.close <- true
				return
			}
		}
	}
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

func (w *worker) SetQueue(queue Queue) Worker {
	w.Lock()
	defer w.Unlock()
	for w.queue.Len() > 0 {
		item := w.queue.Pop()
		queue.Push(item)
	}
	w.queue = queue
	return w
}

func (w *worker) insertTask(task *Task) {
	if w.closed {
		return
	}

	w.Lock()
	defer w.Unlock()

	w.queue.Push(task)
}

func (w *worker) getTasks() []*Task {
	tasks := make([]*Task, 0)
	w.Lock()
	defer w.Unlock()

	currentTime := time.Now()
	for w.queue.Peek() != nil && currentTime.After(w.queue.Peek().At) {
		task := w.queue.Pop()
		if task == nil {
			break
		}
		tasks = append(tasks, task)
	}
	return tasks
}

func (w *worker) process(tasks []*Task) {
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

func (w *worker) postProcess(task *Task) {
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
