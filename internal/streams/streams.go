package streams

import (
	"encoding/json"
	"errors"
	"net/url"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/auth"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/rs/zerolog"
)

func Init() {
	var cfg struct {
		Streams map[string]any    `yaml:"streams"`
		Publish map[string]any    `yaml:"publish"`
		Preload map[string]string `yaml:"preload"`
	}

	app.LoadConfig(&cfg)

	log = app.GetLogger("streams")

	for name, item := range cfg.Streams {
		streams[name] = NewStream(item)
		auth.RegisterStreamID(name) // register masked ID for proxy
	}
	auth.SetStreamNamesProvider(GetAllNames) // allow snapshot worker to list streams
	auth.SetStreamSourcesProvider(func(name string) []string {
		if s := Get(name); s != nil {
			return s.Sources()
		}
		return nil
	})

	api.HandleFunc("api/streams", apiStreams)
	api.HandleFunc("api/streams.dot", apiStreamsDOT)
	api.HandleFunc("api/preload", apiPreload)
	api.HandleFunc("api/schemes", apiSchemes)

	if cfg.Publish == nil && cfg.Preload == nil {
		return
	}

	time.AfterFunc(time.Second, func() {
		// range for nil map is OK
		for name, dst := range cfg.Publish {
			if stream := Get(name); stream != nil {
				Publish(stream, dst)
			}
		}
		for name, rawQuery := range cfg.Preload {
			if err := AddPreload(name, rawQuery); err != nil {
				log.Error().Err(err).Caller().Send()
			}
		}
	})
}

func New(name string, sources ...string) (*Stream, error) {
	for _, source := range sources {
		if !HasProducer(source) {
			return nil, errors.New("streams: source not supported")
		}

		if err := Validate(source); err != nil {
			return nil, err
		}
	}

	stream := NewStream(sources)

	streamsMu.Lock()
	streams[name] = stream
	streamsMu.Unlock()

	auth.RegisterStreamID(name) // register masked ID for proxy

	return stream, nil
}

func Patch(name string, source string) (*Stream, error) {
	streamsMu.Lock()
	defer streamsMu.Unlock()

	// check if source links to some stream name from go2rtc
	if u, err := url.Parse(source); err == nil && u.Scheme == "rtsp" && len(u.Path) > 1 {
		rtspName := u.Path[1:]
		if stream, ok := streams[rtspName]; ok {
			if streams[name] != stream {
				// link (alias) streams[name] to streams[rtspName]
				streams[name] = stream
			}
			return stream, nil
		}
	}

	if stream, ok := streams[source]; ok {
		if name != source {
			// link (alias) streams[name] to streams[source]
			streams[name] = stream
		}
		return stream, nil
	}

	// check if src has supported scheme
	if !HasProducer(source) {
		return nil, errors.New("streams: source not supported")
	}

	if err := Validate(source); err != nil {
		return nil, err
	}

	// check an existing stream with this name
	if stream, ok := streams[name]; ok {
		stream.SetSource(source)
		return stream, nil
	}

	// create new stream with this name
	stream := NewStream(source)
	streams[name] = stream
	return stream, nil
}

func GetOrPatch(query url.Values) (*Stream, error) {
	// check if src param exists
	source := query.Get("src")
	if source == "" {
		return nil, errors.New("streams: source empty")
	}

	// check if src is stream name
	if stream := Get(source); stream != nil {
		return stream, nil
	}

	// check if name param provided
	if name := query.Get("name"); name != "" {
		return Patch(name, source)
	}

	// return new stream with src as name
	return Patch(source, source)
}

var log zerolog.Logger

// streams map

var streams = map[string]*Stream{}
var streamsMu sync.Mutex

func Get(name string) *Stream {
	streamsMu.Lock()
	defer streamsMu.Unlock()
	return streams[name]
}

// GetByAny resolves both real stream names AND masked camera IDs (12-char hex).
// Used by the RTSP server so clients can connect with rtsp://host:8554/CAMERA_ID
// without knowing the real stream name.
func GetByAny(name string) *Stream {
	if s := Get(name); s != nil {
		return s
	}
	// Try resolving as a masked ID
	if realName, ok := auth.StreamNameByID(name); ok && realName != name {
		return Get(realName)
	}
	return nil
}

func Delete(name string) {
	streamsMu.Lock()
	defer streamsMu.Unlock()
	delete(streams, name)
}

func GetAllNames() []string {
	streamsMu.Lock()
	names := make([]string, 0, len(streams))
	for name := range streams {
		names = append(names, name)
	}
	streamsMu.Unlock()
	return names
}

// GetStreamStats returns monitoring counters:
//   - total:     number of configured streams
//   - active:    streams currently being watched (>=1 consumer)
//   - consumers: total consumer connections (viewing sessions) across all streams
func GetStreamStats() (total, active, consumers int) {
	streamsMu.Lock()
	defer streamsMu.Unlock()
	total = len(streams)
	for _, s := range streams {
		n := s.consumerCount()
		if n > 0 {
			active++
		}
		consumers += n
	}
	return
}

// SessionDetail holds per-viewing-session info extracted from a consumer.
type SessionDetail struct {
	RemoteAddr string `json:"remote_addr"` // IP:port of the viewer
	FormatName string `json:"format_name"` // rtsp, webrtc, mp4, mjpeg…
	Protocol   string `json:"protocol"`    // tcp, udp, http, ws…
	UserAgent  string `json:"user_agent"`
}

// StreamDetail holds per-stream info for the activity API.
type StreamDetail struct {
	Name      string          `json:"name"`
	Consumers int             `json:"consumers"`
	Sessions  []SessionDetail `json:"sessions"`
}

// GetStreamDetails returns all streams with per-consumer session info,
// sorted so streams with viewers appear first.
func GetStreamDetails() []StreamDetail {
	streamsMu.Lock()
	// Snapshot consumers under lock, release before marshaling.
	type snap struct {
		name string
		cons []core.Consumer
	}
	snaps := make([]snap, 0, len(streams))
	for name, s := range streams {
		s.mu.Lock()
		cp := make([]core.Consumer, len(s.consumers))
		copy(cp, s.consumers)
		s.mu.Unlock()
		snaps = append(snaps, snap{name, cp})
	}
	streamsMu.Unlock()

	out := make([]StreamDetail, 0, len(snaps))
	for _, sn := range snaps {
		sessions := make([]SessionDetail, 0, len(sn.cons))
		for _, cons := range sn.cons {
			// All consumer types embed core.Connection which serialises its fields.
			if data, err := json.Marshal(cons); err == nil {
				var sd SessionDetail
				_ = json.Unmarshal(data, &sd)
				sessions = append(sessions, sd)
			}
		}
		out = append(out, StreamDetail{
			Name:      sn.name,
			Consumers: len(sn.cons),
			Sessions:  sessions,
		})
	}

	// Sort: active first, then alphabetically.
	for i := 0; i < len(out)-1; i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Consumers > out[i].Consumers ||
				(out[j].Consumers == out[i].Consumers && out[j].Name < out[i].Name) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func GetAllSources() map[string][]string {
	streamsMu.Lock()
	sources := make(map[string][]string, len(streams))
	for name, stream := range streams {
		sources[name] = stream.Sources()
	}
	streamsMu.Unlock()
	return sources
}
