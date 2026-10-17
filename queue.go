package scheduledworker

import (
	"container/heap"
)

// Queue orders tasks for a worker. A queue installed with SetQueue is exclusively
// owned by that worker; its methods need not provide their own synchronization.
// Peek must return nil when empty. Pop must remove the same item as Peek.
type Queue interface {
	Push(*Task)
	Pop() *Task
	Len() int
	Peek() *Task
}

type queue []*Task

// PriorityQueue orders tasks by their earliest execution time. Equal deadlines
// have no guaranteed relative order. It is not safe for concurrent direct use.
type PriorityQueue struct {
	queue queue
}

var _ Queue = &PriorityQueue{}
var _ heap.Interface = &queue{}

// NewPriorityQueue creates an empty queue ordered by Task.At.
func NewPriorityQueue() *PriorityQueue {
	q := make(queue, 0)
	heap.Init(&q)
	return &PriorityQueue{queue: q}
}

// Push inserts a non-nil task. A nil task panics without changing the queue.
func (pq *PriorityQueue) Push(x *Task) {
	if x == nil {
		panic("scheduledworker: task must not be nil")
	}
	heap.Push(&pq.queue, x)
}

func (pq *PriorityQueue) Pop() *Task {
	if pq.Len() == 0 {
		return nil
	}
	return heap.Pop(&pq.queue).(*Task)
}

func (pq *PriorityQueue) Peek() *Task {
	if len(pq.queue) == 0 {
		return nil
	}
	return pq.queue[0]
}

func (pq *PriorityQueue) Len() int {
	return pq.queue.Len()
}

func (pq *queue) Len() int { return len(*pq) }

func (pq *queue) Less(i, j int) bool {
	return (*pq)[i].At.Before((*pq)[j].At)
}

func (pq *queue) Swap(i, j int) {
	(*pq)[i], (*pq)[j] = (*pq)[j], (*pq)[i]
}

func (pq *queue) Push(x interface{}) {
	item := x.(*Task)
	*pq = append(*pq, item)
}

func (pq *queue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	*pq = old[:n-1]
	return item
}
