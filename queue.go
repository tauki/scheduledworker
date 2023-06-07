package scheduledworker

import (
	"container/heap"
)

type Queue interface {
	Push(*Task)
	Pop() *Task
	Len() int
	Peek() *Task
}

type queue []*Task

type PriorityQueue struct {
	queue queue
}

var _ Queue = &PriorityQueue{}
var _ heap.Interface = &queue{}

func NewPriorityQueue() *PriorityQueue {
	q := make(queue, 0)
	heap.Init(&q)
	return &PriorityQueue{queue: q}
}

func (pq *PriorityQueue) Push(x *Task) {
	heap.Push(&pq.queue, x)
}

func (pq *PriorityQueue) Pop() *Task {
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
