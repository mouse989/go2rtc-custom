package streams

import (
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
)

// LimitConsumer force-removes cons from s once d elapses — used to enforce
// a browser view-session time limit (internal/auth.ViewSessionLimit). A
// zero or negative d means unlimited and does nothing.
//
// The returned cancel func must be called once cons is removed through its
// normal path (client disconnected, handler returned, etc.) so the pending
// timer doesn't fire on an already-gone consumer.
func (s *Stream) LimitConsumer(cons core.Consumer, d time.Duration) (cancel func()) {
	if d <= 0 {
		return func() {}
	}
	timer := time.AfterFunc(d, func() {
		s.RemoveConsumer(cons)
	})
	return func() { timer.Stop() }
}
