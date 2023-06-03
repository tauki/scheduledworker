package scheduledworker

import (
	"testing"
	"time"
)

func TestPriorityQueue(t *testing.T) {
	items := []time.Time{
		time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	pq := NewPriorityQueue()
	for _, priority := range items {
		pq.Push(&Item{priority: priority})
	}

	t.Run("Len", func(t *testing.T) {
		if pq.Len() != len(items) {
			t.Errorf("pq.Len() = %d, want %d", pq.Len(), len(items))
		}
	})

	t.Run("Less", func(t *testing.T) {
		if pq.queue.Less(0, 1) {
			t.Errorf("pq.Less(0, 1) = true, want false")
		}
		if !pq.queue.Less(1, 2) {
			t.Errorf("pq.Less(1, 2) = false, want true")
		}
	})

	t.Run("Swap", func(t *testing.T) {
		pq.queue.Swap(0, 1)
		if pq.queue[0].priority != items[1] {
			t.Errorf("pq[0].priority = %v, want %v", pq.queue[0].priority, items[1])
		}
		if pq.queue[1].priority != items[0] {
			t.Errorf("pq[1].priority = %v, want %v", pq.queue[1].priority, items[0])
		}
	})

	newItem := &Item{priority: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
	t.Run("Push", func(t *testing.T) {
		pq.Push(newItem)
		if pq.Peek().priority != newItem.priority {
			t.Errorf("pq.Peek() = %v, want %v", pq.Peek().priority.String(), newItem.priority.String())
		}
	})

	t.Run("Pop", func(t *testing.T) {
		l := pq.Len()
		item := pq.Pop()
		if item.priority != newItem.priority {
			t.Errorf("item.priority = %v, want %v", item.priority, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
		}
		if pq.Len() != l-1 {
			t.Errorf("pq.Len() = %d, want %d", pq.Len(), 3)
		}
	})

	t.Run("Peek", func(t *testing.T) {
		l := pq.Len()
		item := pq.Peek()
		if item.priority != pq.queue[0].priority {
			t.Errorf("item.priority = %v, want %v", item.priority, time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC))
		}
		if pq.Len() != l {
			t.Errorf("pq.Len() = %d, want %d", pq.Len(), 3)
		}
	})

	t.Run("Pop All", func(t *testing.T) {
		for _, i := range items {
			pq.Push(&Item{priority: i})
		}

		previousTime := pq.Peek().priority
		for pq.Len() > 0 {
			item := pq.Pop()
			if item.priority.Before(previousTime) {
				t.Errorf("item.priority = %v, want after or equal to %v", item.priority, previousTime)
			}
		}
	})
}
