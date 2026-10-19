package scheduledworker

// Each registration embeds two indexed nodes. Removal never leaves tombstones.
type deadline struct {
	at    int64
	order uint64
	retry bool
	r     *registration
	index int
}
type deadlineHeap []*deadline

func (h deadlineHeap) Len() int { return len(h) }
func (h deadlineHeap) Less(i, j int) bool {
	if h[i].at != h[j].at {
		return h[i].at < h[j].at
	}
	if h[i].order != h[j].order {
		return h[i].order < h[j].order
	}
	return !h[i].retry && h[j].retry
}
func (h deadlineHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *deadlineHeap) Push(x any)   { d := x.(*deadline); d.index = len(*h); *h = append(*h, d) }
func (h *deadlineHeap) Pop() any {
	a := *h
	d := a[len(a)-1]
	a[len(a)-1] = nil
	*h = a[:len(a)-1]
	d.index = -1
	return d
}
