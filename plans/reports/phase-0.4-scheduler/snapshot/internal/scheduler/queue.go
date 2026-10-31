package scheduler

import (
	"RaiseMyFish/internal/event"
	"time"
)

type queued struct {
	claim    event.Claim
	priority int
	index    int
	renewAt  time.Time
}
type priorityQueue []*queued

func (q priorityQueue) Len() int { return len(q) }
func (q priorityQueue) Less(i, j int) bool {
	a, b := q[i], q[j]
	if a.priority != b.priority {
		return a.priority < b.priority
	}
	if !a.claim.Event.At.Equal(b.claim.Event.At) {
		return a.claim.Event.At.Before(b.claim.Event.At)
	}
	return a.claim.Event.ID < b.claim.Event.ID
}
func (q priorityQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i]; q[i].index = i; q[j].index = j }
func (q *priorityQueue) Push(x any)   { item := x.(*queued); item.index = len(*q); *q = append(*q, item) }
func (q *priorityQueue) Pop() any {
	old := *q
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	*q = old[:n-1]
	return item
}
