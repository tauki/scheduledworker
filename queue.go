package scheduledworker

import (
	"container/heap"
	"time"
)

type Item struct {
	task     Task
	priority time.Time
}

type PriorityQueue []*Item

func (pq *PriorityQueue) Len() int { return len(*pq) }

func (pq *PriorityQueue) Less(i, j int) bool {
	return (*pq)[i].priority.Before((*pq)[j].priority)
}

func (pq *PriorityQueue) Swap(i, j int) {
	(*pq)[i], (*pq)[j] = (*pq)[j], (*pq)[i]
}

func (pq *PriorityQueue) PushItem(x *Item) {
	heap.Push(pq, x)
}

func (pq *PriorityQueue) PopItem() *Item {
	return heap.Pop(pq).(*Item)
}

func (pq *PriorityQueue) Push(x interface{}) {
	item := x.(*Item)
	*pq = append(*pq, item)
}

func (pq *PriorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil // avoid memory leak
	*pq = old[:n-1]
	return item
}

func (pq *PriorityQueue) Peek() *Item {
	if len(*pq) == 0 {
		return nil
	}
	return (*pq)[0]
}
