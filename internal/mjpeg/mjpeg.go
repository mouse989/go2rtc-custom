package mjpeg

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/accesslog"
	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/api/ws"
	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/auth"
	"github.com/AlexxIT/go2rtc/internal/ffmpeg"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/ascii"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/magic"
	"github.com/AlexxIT/go2rtc/pkg/mjpeg"
	"github.com/AlexxIT/go2rtc/pkg/mpjpeg"
	"github.com/AlexxIT/go2rtc/pkg/y4m"
	"github.com/rs/zerolog"
)

func Init() {
	api.HandleFunc("api/frame.jpeg", handlerKeyframe)
	api.HandleFunc("api/stream.mjpeg", handlerStream)
	api.HandleFunc("api/stream.ascii", handlerStream)
	api.HandleFunc("api/stream.y4m", apiStreamY4M)

	ws.HandleFunc("mjpeg", handlerWS)

	log = app.GetLogger("mjpeg")
}

var log zerolog.Logger

func handlerKeyframe(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if src := query.Get("src"); src != "" && !auth.CanAccessStreamRequest(r, src) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	stream, _ := streams.GetOrPatch(query)
	if stream == nil {
		http.Error(w, api.StreamNotFound, http.StatusNotFound)
		return
	}

	var b []byte

	if s := query.Get("cache"); s != "" {
		if timeout, err := time.ParseDuration(s); err == nil {
			src := query.Get("src")

			cacheMu.Lock()
			entry, found := cache[src]
			cacheMu.Unlock()

			if found && time.Since(entry.timestamp) < timeout {
				writeJPEGResponse(w, entry.payload)
				return
			}

			defer func() {
				if b == nil {
					return
				}
				entry = cacheEntry{payload: b, timestamp: time.Now()}
				cacheMu.Lock()
				if cache == nil {
					cache = map[string]cacheEntry{src: entry}
				} else {
					cache[src] = entry
				}
				cacheMu.Unlock()
			}()
		}
	}

	cons := magic.NewKeyframe()
	cons.WithRequest(r)

	if err := stream.AddConsumer(cons); err != nil {
		// Expected/transient (source offline, reconnecting, refused, etc.) —
		// this endpoint is polled frequently by UI thumbnails/snapshots, so
		// logging at Error level here floods the log for a non-actionable
		// per-camera connectivity condition rather than a code defect.
		log.Warn().Err(err).Caller().Send()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Wait for the first keyframe, but never forever: a camera that is
	// connected yet not sending video (or reconnecting in a loop) used to
	// park this goroutine indefinitely, keeping the consumer attached and
	// therefore the RTSP connection to the camera open. With ~1500 cameras
	// polled for snapshots these leaked sessions piled up until the whole
	// process stalled. Now the wait is bounded by the client's context and
	// a hard timeout; removing the consumer closes its WriteBuffer, which
	// releases WriteTo and lets the producer disconnect.
	once := &core.OnceBuffer{} // init and first frame
	done := make(chan struct{})
	go func() {
		_, _ = cons.WriteTo(once)
		close(done)
	}()

	timer := time.NewTimer(keyframeTimeout(query))
	var waitErr string
	select {
	case <-done:
	case <-r.Context().Done():
		waitErr = "client gone"
	case <-timer.C:
		waitErr = "keyframe timeout"
	}
	timer.Stop()

	stream.RemoveConsumer(cons)
	<-done

	if waitErr != "" {
		log.Debug().Str("src", query.Get("src")).Msgf("[mjpeg] %s", waitErr)
		http.Error(w, waitErr, http.StatusGatewayTimeout)
		return
	}

	b = once.Buffer()
	if len(b) == 0 {
		http.Error(w, "no keyframe", http.StatusBadGateway)
		return
	}

	switch cons.CodecName() {
	case core.CodecH264, core.CodecH265:
		if r.Context().Err() != nil {
			return // caller gave up; don't spend an ffmpeg slot on it
		}
		ts := time.Now()
		var err error
		if b, err = ffmpeg.JPEGWithQueryContext(r.Context(), b, query); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Debug().Msgf("[mjpeg] transcoding time=%s", time.Since(ts))
	case core.CodecJPEG:
		b = mjpeg.FixJPEG(b)
	}

	writeJPEGResponse(w, b)
}

// keyframeTimeout bounds how long /api/frame.jpeg waits for a keyframe.
// Default 15s (covers cameras with long GOPs); callers may lower or raise
// it with ?timeout=10s (clamped to 1s..60s).
func keyframeTimeout(query url.Values) time.Duration {
	if s := query.Get("timeout"); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			if d < time.Second {
				d = time.Second
			} else if d > time.Minute {
				d = time.Minute
			}
			return d
		}
	}
	return 15 * time.Second
}

var cache map[string]cacheEntry
var cacheMu sync.Mutex

// cacheEntry represents a cached keyframe with its timestamp
type cacheEntry struct {
	payload   []byte
	timestamp time.Time
}

func writeJPEGResponse(w http.ResponseWriter, b []byte) {
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "close")
	h.Set("Pragma", "no-cache")

	if _, err := w.Write(b); err != nil {
		log.Error().Err(err).Caller().Send()
	}
}

func handlerStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		outputMjpeg(w, r)
	} else {
		inputMjpeg(w, r)
	}
}

func outputMjpeg(w http.ResponseWriter, r *http.Request) {
	src := r.URL.Query().Get("src")
	if !auth.CanAccessStreamRequest(r, src) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	stream := streams.Get(src)
	if stream == nil {
		http.Error(w, api.StreamNotFound, http.StatusNotFound)
		return
	}

	cons := mjpeg.NewConsumer()
	cons.WithRequest(r)

	if err := stream.AddConsumer(cons); err != nil {
		log.Warn().Err(err).Msg("[api.mjpeg] add consumer")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	user, _ := auth.UserFromContext(r.Context())
	cancelLimit := stream.LimitConsumer(cons, auth.ViewSessionLimit(user))
	if user != nil {
		kind := "ascii"
		if strings.HasSuffix(r.URL.Path, "mjpeg") {
			kind = "mjpeg"
		}
		accesslog.Record(user.Username, src, kind)
	}

	h := w.Header()
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "close")
	h.Set("Pragma", "no-cache")

	if strings.HasSuffix(r.URL.Path, "mjpeg") {
		wr := mjpeg.NewWriter(w)
		_, _ = cons.WriteTo(wr)
	} else {
		cons.FormatName = "ascii"

		query := r.URL.Query()
		wr := ascii.NewWriter(w, query.Get("color"), query.Get("back"), query.Get("text"))
		_, _ = cons.WriteTo(wr)
	}

	cancelLimit()
	stream.RemoveConsumer(cons)
}

func inputMjpeg(w http.ResponseWriter, r *http.Request) {
	dst := r.URL.Query().Get("dst")
	if !auth.CanAccessStreamRequest(r, dst) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	stream := streams.Get(dst)
	if stream == nil {
		http.Error(w, api.StreamNotFound, http.StatusNotFound)
		return
	}

	prod, _ := mpjpeg.Open(r.Body)
	prod.WithRequest(r)

	stream.AddProducer(prod)

	if err := prod.Start(); err != nil && err != io.EOF {
		log.Warn().Err(err).Caller().Send()
	}

	stream.RemoveProducer(prod)
}

func handlerWS(tr *ws.Transport, _ *ws.Message) error {
	stream, _ := streams.GetOrPatch(tr.Request.URL.Query())
	if stream == nil {
		return errors.New(api.StreamNotFound)
	}

	cons := mjpeg.NewConsumer()
	cons.WithRequest(tr.Request)

	if err := stream.AddConsumer(cons); err != nil {
		log.Debug().Err(err).Msg("[mjpeg] add consumer")
		return err
	}

	tr.Write(&ws.Message{Type: "mjpeg"})

	go cons.WriteTo(tr.Writer())

	user, _ := auth.UserFromContext(tr.Request.Context())
	cancelLimit := stream.LimitConsumer(cons, auth.ViewSessionLimit(user))
	if user != nil {
		accesslog.Record(user.Username, tr.Request.URL.Query().Get("src"), "mjpeg-ws")
	}

	tr.OnClose(func() {
		cancelLimit()
		stream.RemoveConsumer(cons)
	})

	return nil
}

func apiStreamY4M(w http.ResponseWriter, r *http.Request) {
	src := r.URL.Query().Get("src")
	if !auth.CanAccessStreamRequest(r, src) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	stream := streams.Get(src)
	if stream == nil {
		http.Error(w, api.StreamNotFound, http.StatusNotFound)
		return
	}

	cons := y4m.NewConsumer()
	cons.WithRequest(r)

	if err := stream.AddConsumer(cons); err != nil {
		log.Warn().Err(err).Caller().Send()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	user, _ := auth.UserFromContext(r.Context())
	cancelLimit := stream.LimitConsumer(cons, auth.ViewSessionLimit(user))
	if user != nil {
		accesslog.Record(user.Username, src, "y4m")
	}

	_, _ = cons.WriteTo(w)

	cancelLimit()
	stream.RemoveConsumer(cons)
}
