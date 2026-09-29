package mjpeg

// keepalive.go — persistent keyframe cache for the snapshot scheduler.
//
// At small camera counts, redialing the RTSP source on every snapshot poll
// (magic.Keyframe: AddConsumer → wait for keyframe → RemoveConsumer, see
// handlerKeyframe below) is cheap enough not to matter. At thousands of
// cameras it means the whole fleet's RTSP connections are torn down and
// re-established every snapshot_interval_sec forever — constant TCP/RTSP
// handshake churn instead of steady-state polling.
//
// liveKeyframe is the same sendonly consumer as magic.Keyframe, except it
// never errors itself out after the first frame (core.OnceBuffer's trick):
// it just keeps overwriting one cached "latest keyframe" as new ones arrive
// from the GOP. A keepAliveEntry attaches one of these once per stream and
// keeps it attached; every subsequent poll is a cheap in-memory read, and
// go2rtc's own producer reconnect-with-backoff (internal/streams/producer.go)
// keeps the underlying connection alive across camera drops without any
// extra retry logic here. Entries a scheduler stops polling (camera removed,
// retyped away from RTSP) age out via keepAliveSweeper.

import (
	"errors"
	"net/url"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/auth"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h264"
	"github.com/AlexxIT/go2rtc/pkg/h264/annexb"
	"github.com/AlexxIT/go2rtc/pkg/h265"
	"github.com/AlexxIT/go2rtc/pkg/mjpeg"
	"github.com/pion/rtp"
)

type liveKeyframe struct {
	core.Connection

	mu    sync.Mutex
	data  []byte
	codec string

	ready     chan struct{}
	readyOnce sync.Once
}

func newLiveKeyframe() *liveKeyframe {
	medias := []*core.Media{
		{
			Kind:      core.KindVideo,
			Direction: core.DirectionSendonly,
			Codecs: []*core.Codec{
				{Name: core.CodecJPEG},
				{Name: core.CodecRAW},
				{Name: core.CodecH264},
				{Name: core.CodecH265},
			},
		},
	}
	return &liveKeyframe{
		Connection: core.Connection{ID: core.NewID(), FormatName: "keyframe-keepalive", Medias: medias},
		ready:      make(chan struct{}),
	}
}

func (k *liveKeyframe) set(b []byte, codec string) {
	cp := make([]byte, len(b))
	copy(cp, b)

	k.mu.Lock()
	k.data = cp
	k.codec = codec
	k.mu.Unlock()

	k.readyOnce.Do(func() { close(k.ready) })
}

func (k *liveKeyframe) latest() ([]byte, string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.data, k.codec
}

func (k *liveKeyframe) CodecName() string {
	if len(k.Senders) != 1 {
		return ""
	}
	return k.Senders[0].Codec.Name
}

// AddTrack mirrors pkg/magic.Keyframe's per-codec keyframe extraction, but
// writes into the long-lived cache above instead of a one-shot WriteBuffer.
func (k *liveKeyframe) AddTrack(media *core.Media, _ *core.Codec, track *core.Receiver) error {
	sender := core.NewSender(media, track.Codec)

	switch track.Codec.Name {
	case core.CodecH264:
		sender.Handler = func(packet *rtp.Packet) {
			if !h264.IsKeyframe(packet.Payload) {
				return
			}
			k.set(annexb.DecodeAVCC(packet.Payload, true), core.CodecH264)
		}
		if track.Codec.IsRTP() {
			sender.Handler = h264.RTPDepay(track.Codec, sender.Handler)
		} else {
			sender.Handler = h264.RepairAVCC(track.Codec, sender.Handler)
		}

	case core.CodecH265:
		sender.Handler = func(packet *rtp.Packet) {
			if !h265.IsKeyframe(packet.Payload) {
				return
			}
			k.set(annexb.DecodeAVCC(packet.Payload, true), core.CodecH265)
		}
		if track.Codec.IsRTP() {
			sender.Handler = h265.RTPDepay(track.Codec, sender.Handler)
		}

	case core.CodecJPEG:
		sender.Handler = func(packet *rtp.Packet) {
			k.set(packet.Payload, core.CodecJPEG)
		}
		if track.Codec.IsRTP() {
			sender.Handler = mjpeg.RTPDepay(sender.Handler)
		}

	case core.CodecRAW:
		sender.Handler = func(packet *rtp.Packet) {
			k.set(packet.Payload, core.CodecRAW)
		}
		sender.Handler = mjpeg.Encoder(track.Codec, 5, sender.Handler)
	}

	sender.HandleRTP(track)
	k.Senders = append(k.Senders, sender)
	return nil
}

// ── Registry ─────────────────────────────────────────────────────

type keepAliveEntry struct {
	stream *streams.Stream
	cons   *liveKeyframe

	mu       sync.Mutex
	lastPoll time.Time
}

func (e *keepAliveEntry) touch() {
	e.mu.Lock()
	e.lastPoll = time.Now()
	e.mu.Unlock()
}

func (e *keepAliveEntry) idleSince() time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return time.Since(e.lastPoll)
}

var (
	keepMu  sync.Mutex
	keepMap = map[string]*keepAliveEntry{}
)

// keepAliveIdleTimeout sizes the sweeper's grace period off the configured
// snapshot interval, so a slower-than-default polling cadence never races
// its own entries out from under it.
func keepAliveIdleTimeout() time.Duration {
	iv := auth.GetSettings().SnapshotIntervalSec
	if iv < 1 {
		iv = 15
	}
	d := time.Duration(iv) * 4 * time.Second
	if d < 2*time.Minute {
		d = 2 * time.Minute
	}
	return d
}

var sweeperOnce sync.Once

func startKeepAliveSweeper() {
	sweeperOnce.Do(func() {
		go func() {
			for {
				time.Sleep(30 * time.Second)

				var stale []*keepAliveEntry
				timeout := keepAliveIdleTimeout()

				keepMu.Lock()
				for name, e := range keepMap {
					if e.idleSince() > timeout {
						stale = append(stale, e)
						delete(keepMap, name)
					}
				}
				keepMu.Unlock()

				for _, e := range stale {
					e.stream.RemoveConsumer(e.cons)
				}
			}
		}()
	})
}

// getOrStartKeepAlive returns the cached-keyframe entry for query["src"],
// attaching a new liveKeyframe consumer (dialing the camera) on first poll
// and reusing it on every poll after that.
func getOrStartKeepAlive(query url.Values) (*keepAliveEntry, error) {
	startKeepAliveSweeper()

	src := query.Get("src")

	keepMu.Lock()
	if entry, ok := keepMap[src]; ok {
		keepMu.Unlock()
		entry.touch()
		return entry, nil
	}
	keepMu.Unlock()

	stream, _ := streams.GetOrPatch(query)
	if stream == nil {
		return nil, errors.New(api.StreamNotFound)
	}

	cons := newLiveKeyframe()
	if err := stream.AddConsumer(cons); err != nil {
		return nil, err
	}

	entry := &keepAliveEntry{stream: stream, cons: cons, lastPoll: time.Now()}

	keepMu.Lock()
	if existing, ok := keepMap[src]; ok {
		// Lost a race against a concurrent first poll for the same camera —
		// keep the winner's consumer, drop ours.
		keepMu.Unlock()
		stream.RemoveConsumer(cons)
		existing.touch()
		return existing, nil
	}
	keepMap[src] = entry
	keepMu.Unlock()

	return entry, nil
}
