package mjpeg

import (
	"sync"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
)

func TestLiveKeyframeLatestUpdatesAndCopies(t *testing.T) {
	k := newLiveKeyframe()

	if b, _ := k.latest(); b != nil {
		t.Fatalf("expected no data before first set, got %d bytes", len(b))
	}

	select {
	case <-k.ready:
		t.Fatal("ready closed before any frame was set")
	default:
	}

	first := []byte{1, 2, 3}
	k.set(first, core.CodecH264)

	select {
	case <-k.ready:
	default:
		t.Fatal("ready not closed after first set")
	}

	b, codec := k.latest()
	if codec != core.CodecH264 || len(b) != 3 || b[0] != 1 {
		t.Fatalf("unexpected latest() after first set: %v %q", b, codec)
	}

	// Mutating the source slice after set() must not corrupt the cache —
	// set() has to copy, since the RTP handler reuses packet buffers.
	first[0] = 99
	if b2, _ := k.latest(); b2[0] != 1 {
		t.Fatalf("latest() aliased the caller's slice: got %v", b2)
	}

	// A later frame overwrites the cache in place.
	k.set([]byte{4, 5}, core.CodecJPEG)
	b3, codec3 := k.latest()
	if codec3 != core.CodecJPEG || len(b3) != 2 || b3[0] != 4 {
		t.Fatalf("unexpected latest() after second set: %v %q", b3, codec3)
	}

	// ready only ever closes once, even across many sets — must not panic.
	k.set([]byte{6}, core.CodecJPEG)
	select {
	case <-k.ready:
	default:
		t.Fatal("ready should stay closed")
	}
}

func TestLiveKeyframeConcurrentSetLatest(t *testing.T) {
	k := newLiveKeyframe()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			k.set([]byte{byte(i)}, core.CodecH264)
		}(i)
		go func() {
			defer wg.Done()
			k.latest()
		}()
	}
	wg.Wait() // must finish without -race flagging a data race
}

func TestKeepAliveIdleTimeoutFloor(t *testing.T) {
	// No settings configured in this test process → SnapshotIntervalSec
	// defaults to 0, which keepAliveIdleTimeout must treat as the same
	// 15s default the scheduler itself falls back to (proxy.go's
	// snapshotInterval), not a near-zero timeout that would sweep entries
	// out from under an active poller.
	got := keepAliveIdleTimeout()
	if got < 2*time.Minute {
		t.Fatalf("expected floor of 2m, got %s", got)
	}
}
