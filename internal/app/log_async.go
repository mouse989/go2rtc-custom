package app

import (
	"io"
	"sync/atomic"
)

// asyncWriter hands log lines to a background goroutine so callers never
// block on the underlying writer. When the queue is full (output stalled),
// lines are dropped and counted instead of freezing the caller.
type asyncWriter struct {
	out     io.Writer
	ch      chan []byte
	dropped atomic.Int64
}

func newAsyncWriter(out io.Writer) io.Writer {
	w := &asyncWriter{out: out, ch: make(chan []byte, 4096)}
	go w.run()
	return w
}

func (w *asyncWriter) Write(p []byte) (int, error) {
	b := make([]byte, len(p)) // zerolog reuses p after Write returns
	copy(b, p)
	select {
	case w.ch <- b:
	default:
		w.dropped.Add(1)
	}
	return len(p), nil
}

func (w *asyncWriter) run() {
	for b := range w.ch {
		if n := w.dropped.Swap(0); n > 0 {
			Logger.Warn().Int64("lines", n).Msg("[log] output stalled, lines dropped")
		}
		_, _ = w.out.Write(b)
	}
}
