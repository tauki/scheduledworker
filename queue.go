package scheduledworker

import (
	"container/heap"
	"time"
)

type Queue interface {
	Push(*Item)
	Pop() *Item
	Len() int
	Peek() *Item
}

type Item struct {
	task     Task
	priority time.Time
}

type items []*Item

type PriorityQueue struct {
	queue items
}

func NewPriorityQueue() *PriorityQueue {
	q := make(items, 0)
	heap.Init(&q)
	return &PriorityQueue{queue: q}
}

func (pq *PriorityQueue) Push(x *Item) {
	heap.Push(&pq.queue, x)
}

func (pq *PriorityQueue) Pop() *Item {
	return heap.Pop(&pq.queue).(*Item)
}

func (pq *PriorityQueue) Peek() *Item {
	if len(pq.queue) == 0 {
		return nil
	}
	return (pq.queue)[0]
}

func (pq *PriorityQueue) Len() int {
	return pq.queue.Len()
}

func (pq *items) Len() int { return len(*pq) }

func (pq *items) Less(i, j int) bool {
	return (*pq)[i].priority.Before((*pq)[j].priority)
}

func (pq *items) Swap(i, j int) {
	(*pq)[i], (*pq)[j] = (*pq)[j], (*pq)[i]
}

func (pq *items) Push(x interface{}) {
	item := x.(*Item)
	*pq = append(*pq, item)
}

func (pq *items) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	*pq = old[:n-1]
	return item
}
