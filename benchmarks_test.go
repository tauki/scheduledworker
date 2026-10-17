package scheduledworker

import (
	"sync"
	"testing"
	"time"
)

func BenchmarkPriorityQueuePushPop(b *testing.B) {
	const taskCount = 1024
	tasks := make([]*Task, taskCount)
	for i := range tasks {
		tasks[i] = &Task{At: time.Unix(0, int64(i))}
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		pq := NewPriorityQueue()
		for _, task := range tasks {
			pq.Push(task)
		}
		for pq.Len() > 0 {
			pq.Pop()
		}
	}
}

func BenchmarkWorkerDispatch(b *testing.B) {
	w := New().Start()
	defer w.Stop()
	var done sync.WaitGroup
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		done.Add(1)
		w.Submit(Task{Fn: done.Done})
	}
	done.Wait()
}
