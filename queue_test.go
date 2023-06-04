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
		pq.Push(&Task{At: priority})
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
		if pq.queue[0].At != items[1] {
			t.Errorf("pq[0].priority = %v, want %v", pq.queue[0].At, items[1])
		}
		if pq.queue[1].At != items[0] {
			t.Errorf("pq[1].priority = %v, want %v", pq.queue[1].At, items[0])
		}
	})

	newItem := &Task{At: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
	t.Run("Push", func(t *testing.T) {
		pq.Push(newItem)
		if pq.Peek().At != newItem.At {
			t.Errorf("pq.Peek() = %v, want %v", pq.Peek().At.String(), newItem.At.String())
		}
	})

	t.Run("Pop", func(t *testing.T) {
		l := pq.Len()
		item := pq.Pop()
		if item.At != newItem.At {
			t.Errorf("item.priority = %v, want %v", item.At, newItem.At)
		}
		if pq.Len() != l-1 {
			t.Errorf("pq.Len() = %d, want %d", pq.Len(), 3)
		}
	})

	t.Run("Peek", func(t *testing.T) {
		l := pq.Len()
		item := pq.Peek()
		if item.At != pq.queue[0].At {
			t.Errorf("item.priority = %v, want %v", item.At, pq.queue[0].At)
		}
		if pq.Len() != l {
			t.Errorf("pq.Len() = %d, want %d", pq.Len(), 3)
		}
	})

	t.Run("Pop All", func(t *testing.T) {
		for _, i := range items {
			pq.Push(&Task{At: i})
		}

		previousTime := pq.Peek().At
		for pq.Len() > 0 {
			item := pq.Pop()
			if item.At.Before(previousTime) {
				t.Errorf("item.priority = %v, want after or equal to %v", item.At, previousTime)
			}
		}
	})
}
