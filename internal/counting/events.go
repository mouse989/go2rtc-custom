package counting

import "sync"

const ringSize = 500

// eventRing is a thread-safe in-memory ring buffer of recent CountEvents.
type eventRing struct {
	mu    sync.Mutex
	buf   [ringSize]CountEvent
	head  int
	count int
}

var evRing = &eventRing{}

func (r *eventRing) add(ev CountEvent) {
	r.mu.Lock()
	r.buf[r.head] = ev
	r.head = (r.head + 1) % ringSize
	if r.count < ringSize {
		r.count++
	}
	r.mu.Unlock()
}

// recent returns up to n events, newest first.
func (r *eventRing) recent(n int) []CountEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > r.count {
		n = r.count
	}
	out := make([]CountEvent, n)
	for i := 0; i < n; i++ {
		idx := (r.head - 1 - i + ringSize) % ringSize
		out[i] = r.buf[idx]
	}
	return out
}
