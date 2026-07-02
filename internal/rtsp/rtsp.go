package rtsp

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/auth"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/rtsp"
	"github.com/AlexxIT/go2rtc/pkg/tcp"
	"github.com/rs/zerolog"
)

func Init() {
	var conf struct {
		Mod struct {
			Listen       string `yaml:"listen" json:"listen"`
			Username     string `yaml:"username" json:"-"`
			Password     string `yaml:"password" json:"-"`
			DefaultQuery string `yaml:"default_query" json:"default_query"`
			PacketSize   uint16 `yaml:"pkt_size" json:"pkt_size,omitempty"`

			// RTSPS (RTSP over TLS) — a separate, independent listener from
			// Listen above, so a WAN-facing rtsps:// port can run alongside
			// (or instead of) a LAN-only plain rtsp:// port. Cert/key accept
			// either a file path or raw inline PEM, same convention as
			// api.tls_cert/api.tls_key.
			TLSListen string `yaml:"tls_listen" json:"tls_listen,omitempty"`
			TLSCert   string `yaml:"tls_cert" json:"-"`
			TLSKey    string `yaml:"tls_key" json:"-"`
		} `yaml:"rtsp"`
	}

	// default config
	conf.Mod.Listen = ":8554"
	conf.Mod.DefaultQuery = "video&audio"

	app.LoadConfig(&conf)
	app.Info["rtsp"] = conf.Mod

	log = app.GetLogger("rtsp")

	// RTSP client support
	streams.HandleFunc("rtsp", rtspHandler)
	streams.HandleFunc("rtsps", rtspHandler)
	streams.HandleFunc("rtspx", rtspHandler)

	// RTSP server support
	address := conf.Mod.Listen
	if address == "" {
		return
	}

	ln, err := net.Listen("tcp", address)
	if err != nil {
		log.Error().Err(err).Msg("[rtsp] listen")
		return
	}

	_, Port, _ = net.SplitHostPort(address)
	auth.SetRTSPListenPort(Port) // expose RTSP port to proxy layer for URL building

	// Fail closed: an RTSP server with no credentials lets anyone with the
	// stream URL (which is often just a short ID, not a real secret) pull
	// video with zero authentication — even though the Web UI/API requires a
	// JWT. If no rtsp.username is configured, auto-generate one and persist
	// it so external (non-loopback) RTSP clients must authenticate.
	if conf.Mod.Username == "" {
		conf.Mod.Username = "go2rtc"
		conf.Mod.Password = genRTSPSecret()
		if err := app.PatchConfig([]string{"rtsp", "username"}, conf.Mod.Username); err != nil {
			log.Warn().Err(err).Msg("[rtsp] failed to persist auto-generated username (will regenerate on next restart)")
		}
		if err := app.PatchConfig([]string{"rtsp", "password"}, conf.Mod.Password); err != nil {
			log.Warn().Err(err).Msg("[rtsp] failed to persist auto-generated password (will regenerate on next restart)")
		}
		log.Warn().Str("username", conf.Mod.Username).Str("password", conf.Mod.Password).
			Msg("[rtsp] no rtsp.username configured — auto-generated credentials for non-loopback RTSP clients; view/rotate them in Admin → Settings")
	}

	credMu.Lock()
	currentUsername, currentPassword = conf.Mod.Username, conf.Mod.Password
	credMu.Unlock()

	registerHandlers()

	log.Info().Str("addr", address).Msg("[rtsp] listen")

	if query, err := url.ParseQuery(conf.Mod.DefaultQuery); err == nil {
		defaultMedias = ParseQuery(query)
	}

	go serveRTSP(ln, conf.Mod.PacketSize)

	// RTSPS: same auth/authorization, just wrapped in TLS on its own port.
	// Point NAT/firewall forwarding here (not the plain port) when exposing
	// the stream to the WAN, so credentials aren't sent as near-plaintext.
	if conf.Mod.TLSListen != "" {
		var tlsCfg *tls.Config

		switch {
		case conf.Mod.TLSCert != "" && conf.Mod.TLSKey != "":
			cert, err := loadRTSPCert(conf.Mod.TLSCert, conf.Mod.TLSKey)
			if err != nil {
				log.Error().Err(err).Msg("[rtsps] load certificate")
			} else {
				tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}}
			}

		case api.ACMEManager() != nil:
			// No manual cert configured — reuse the same auto-renewing
			// Let's Encrypt certificate already set up for the Web UI
			// (api.acme_domain), so RTSPS "just works" with zero extra
			// certificate management.
			tlsCfg = api.ACMEManager().TLSConfig()
			tlsCfg.MinVersion = tls.VersionTLS12
			log.Info().Msg("[rtsps] reusing api.acme_domain certificate")

		default:
			log.Error().Msg("[rtsps] rtsp.tls_listen is set but no rtsp.tls_cert/tls_key and no api.acme_domain configured — RTSPS disabled")
		}

		if tlsCfg != nil {
			go tlsServeRTSP(conf.Mod.TLSListen, tlsCfg, conf.Mod.PacketSize)
		}
	}
}

// serveRTSP runs the accept loop for either a plain or TLS-wrapped listener.
func serveRTSP(ln net.Listener, packetSize uint16) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}

		c := rtsp.NewServer(conn)
		c.PacketSize = packetSize
		// skip check auth for localhost (same-host workers/services)
		if !conn.RemoteAddr().(*net.TCPAddr).IP.IsLoopback() {
			c.AuthValidator(validateRTSPCredentials)
		}
		go tcpHandler(c)
	}
}

// TLSPort is the RTSPS listen port, set once tlsServeRTSP starts successfully
// (empty if RTSPS isn't configured).
var TLSPort string

// loadRTSPCert accepts either a file path or raw inline PEM for cert/key,
// same convention as api.tls_cert/api.tls_key.
func loadRTSPCert(certFile, keyFile string) (tls.Certificate, error) {
	if strings.IndexByte(certFile, '\n') < 0 && strings.IndexByte(keyFile, '\n') < 0 {
		return tls.LoadX509KeyPair(certFile, keyFile)
	}
	return tls.X509KeyPair([]byte(certFile), []byte(keyFile))
}

func tlsServeRTSP(address string, tlsCfg *tls.Config, packetSize uint16) {
	ln, err := net.Listen("tcp", address)
	if err != nil {
		log.Error().Err(err).Msg("[rtsps] listen")
		return
	}

	_, TLSPort, _ = net.SplitHostPort(address)

	log.Info().Str("addr", address).Msg("[rtsps] listen")

	tlsLn := tls.NewListener(ln, tlsCfg)
	serveRTSP(tlsLn, packetSize)
}

var (
	credMu                           sync.RWMutex
	currentUsername, currentPassword string
)

// CurrentCredentials returns the RTSP server's active Basic-auth
// username/password required from non-loopback clients.
func CurrentCredentials() (string, string) {
	credMu.RLock()
	defer credMu.RUnlock()
	return currentUsername, currentPassword
}

// WithCredentials embeds the shared service credentials into base (an RTSP
// base URL like "rtsp://main-server-ip:8554") if it doesn't already carry
// userinfo, so callers building a pull URL for a non-loopback client (e.g. a
// remote counting worker's RTSPBase) keep working without every admin having
// to hand-edit each worker's config after auth became mandatory for
// non-loopback RTSP connections. Returns base unchanged if it's empty,
// already has "user:pass@", or the RTSP server has no credentials set yet.
func WithCredentials(base string) string {
	if base == "" {
		return base
	}
	u, err := url.Parse(base)
	if err != nil || u.User != nil {
		return base
	}
	user, pass := CurrentCredentials()
	if user == "" {
		return base
	}
	u.User = url.UserPassword(user, pass)
	return u.String()
}

// RotateCredentials generates a fresh random password (keeping the existing
// username) and persists it, so it takes effect immediately for any new
// connection without a server restart.
func RotateCredentials() (string, string, error) {
	credMu.Lock()
	if currentUsername == "" {
		currentUsername = "go2rtc"
	}
	currentPassword = genRTSPSecret()
	username, password := currentUsername, currentPassword
	credMu.Unlock()

	if err := app.PatchConfig([]string{"rtsp", "username"}, username); err != nil {
		return "", "", err
	}
	if err := app.PatchConfig([]string{"rtsp", "password"}, password); err != nil {
		return "", "", err
	}
	return username, password, nil
}

func genRTSPSecret() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// validateRTSPCredentials accepts either the shared service credential
// (CurrentCredentials, useful for remote workers/scripts that aren't tied to
// one person) or any enabled account from Admin → Users — the very same
// username/password used to log into the Web UI. Per-stream authorization
// (viewers only see their assigned streams) is enforced separately in
// tcpHandler via rtspStreamAllowed, using the authenticated username.
func validateRTSPCredentials(user, pass, _ string) bool {
	if su, sp := CurrentCredentials(); su != "" && user == su && pass == sp {
		return true
	}
	_, ok := auth.Authenticate(user, pass)
	return ok
}

// rtspStreamAllowed reports whether the connection (already authenticated,
// or exempt via loopback) may access the given stream name.
func rtspStreamAllowed(conn *rtsp.Conn, name string) bool {
	username := conn.AuthUser()
	if username == "" {
		return true // loopback connection: no auth was required
	}
	if su, _ := CurrentCredentials(); su != "" && username == su {
		return true // shared service credential: full access
	}
	u, ok := auth.GetUser(username)
	if !ok {
		return false
	}
	return auth.UserCanAccessStream(u, name)
}

type Handler func(conn *rtsp.Conn) bool

func HandleFunc(handler Handler) {
	handlers = append(handlers, handler)
}

var Port string

// internal

var log zerolog.Logger
var handlers []Handler
var defaultMedias []*core.Media

func rtspHandler(rawURL string) (core.Producer, error) {
	rawURL, rawQuery, _ := strings.Cut(rawURL, "#")

	conn := rtsp.NewClient(rawURL)
	conn.Backchannel = true
	conn.UserAgent = app.UserAgent

	if rawQuery != "" {
		query := streams.ParseQuery(rawQuery)
		conn.Backchannel = query.Get("backchannel") == "1"
		conn.Media = query.Get("media")
		conn.Timeout = core.Atoi(query.Get("timeout"))
		conn.Transport = query.Get("transport")
	}

	if log.Trace().Enabled() {
		conn.Listen(func(msg any) {
			switch msg := msg.(type) {
			case *tcp.Request:
				log.Trace().Msgf("[rtsp] client request:\n%s", msg)
			case *tcp.Response:
				log.Trace().Msgf("[rtsp] client response:\n%s", msg)
			case string:
				log.Trace().Msgf("[rtsp] client msg: %s", msg)
			}
		})
	}

	if err := conn.Dial(); err != nil {
		return nil, err
	}

	if err := conn.Describe(); err != nil {
		if !conn.Backchannel {
			return nil, err
		}
		log.Trace().Msgf("[rtsp] describe (backchannel=%t) err: %v", conn.Backchannel, err)

		// second try without backchannel, we need to reconnect
		conn.Backchannel = false
		if err = conn.Dial(); err != nil {
			return nil, err
		}
		if err = conn.Describe(); err != nil {
			return nil, err
		}
	}

	return conn, nil
}

func tcpHandler(conn *rtsp.Conn) {
	var name string
	var closer func()

	trace := log.Trace().Enabled()
	level := zerolog.WarnLevel

	conn.Listen(func(msg any) {
		if trace {
			switch msg := msg.(type) {
			case *tcp.Request:
				log.Trace().Msgf("[rtsp] server request:\n%s", msg)
			case *tcp.Response:
				log.Trace().Msgf("[rtsp] server response:\n%s", msg)
			}
		}

		switch msg {
		case rtsp.MethodDescribe:
			if len(conn.URL.Path) == 0 {
				log.Warn().Msg("[rtsp] server empty URL on DESCRIBE")
				return
			}

			name = conn.URL.Path[1:]

			stream := streams.GetByAny(name)
			if stream == nil {
				return
			}

			if !rtspStreamAllowed(conn, name) {
				log.Warn().Str("stream", name).Str("user", conn.AuthUser()).Msg("[rtsp] forbidden stream")
				return
			}

			log.Debug().Str("stream", name).Msg("[rtsp] new consumer")

			conn.SessionName = app.UserAgent

			query := conn.URL.Query()
			conn.Medias = ParseQuery(query)
			if conn.Medias == nil {
				for _, media := range defaultMedias {
					conn.Medias = append(conn.Medias, media.Clone())
				}
			}

			if query.Get("backchannel") == "1" {
				conn.Medias = append(conn.Medias, &core.Media{
					Kind:      core.KindAudio,
					Direction: core.DirectionRecvonly,
					Codecs: []*core.Codec{
						{Name: core.CodecOpus, ClockRate: 48000, Channels: 2},
						{Name: core.CodecPCM, ClockRate: 16000},
						{Name: core.CodecPCMA, ClockRate: 16000},
						{Name: core.CodecPCMU, ClockRate: 16000},
						{Name: core.CodecPCM, ClockRate: 8000},
						{Name: core.CodecPCMA, ClockRate: 8000},
						{Name: core.CodecPCMU, ClockRate: 8000},
						{Name: core.CodecAAC, ClockRate: 8000},
						{Name: core.CodecAAC, ClockRate: 16000},
					},
				})
			}

			if s := query.Get("pkt_size"); s != "" {
				conn.PacketSize = uint16(core.Atoi(s))
			}

			// param name like ffmpeg style https://ffmpeg.org/ffmpeg-protocols.html
			if s := query.Get("log_level"); s != "" {
				if lvl, err := zerolog.ParseLevel(s); err == nil {
					level = lvl
				}
			}

			// will help to protect looping requests to same source
			conn.Connection.Source = query.Get("source")

			if err := stream.AddConsumer(conn); err != nil {
				log.WithLevel(level).Err(err).Str("stream", name).Msg("[rtsp]")
				return
			}

			closer = func() {
				stream.RemoveConsumer(conn)
			}

		case rtsp.MethodAnnounce:
			if len(conn.URL.Path) == 0 {
				log.Warn().Msg("[rtsp] server empty URL on ANNOUNCE")
				return
			}

			name = conn.URL.Path[1:]

			stream := streams.GetByAny(name)
			if stream == nil {
				return
			}

			if !rtspStreamAllowed(conn, name) {
				log.Warn().Str("stream", name).Str("user", conn.AuthUser()).Msg("[rtsp] forbidden stream")
				return
			}

			query := conn.URL.Query()
			if s := query.Get("timeout"); s != "" {
				conn.Timeout = core.Atoi(s)
			}

			log.Debug().Str("stream", name).Msg("[rtsp] new producer")

			stream.AddProducer(conn)

			closer = func() {
				stream.RemoveProducer(conn)
			}
		}
	})

	if err := conn.Accept(); err != nil {
		if errors.Is(err, rtsp.FailedAuth) {
			log.Warn().Str("remote_addr", conn.Connection.RemoteAddr).Msg("[rtsp] failed authentication")
		} else if err != io.EOF {
			log.WithLevel(level).Err(err).Caller().Send()
		}
		if closer != nil {
			closer()
		}
		_ = conn.Close()
		return
	}

	for _, handler := range handlers {
		if handler(conn) {
			return
		}
	}

	if closer != nil {
		if err := conn.Handle(); err != nil {
			log.Debug().Err(err).Msg("[rtsp] handle")
		}

		closer()

		log.Debug().Str("stream", name).Msg("[rtsp] disconnect")
	}

	_ = conn.Close()
}

func ParseQuery(query map[string][]string) []*core.Media {
	if v := query["mp4"]; v != nil {
		return []*core.Media{
			{
				Kind:      core.KindVideo,
				Direction: core.DirectionSendonly,
				Codecs: []*core.Codec{
					{Name: core.CodecH264},
					{Name: core.CodecH265},
				},
			},
			{
				Kind:      core.KindAudio,
				Direction: core.DirectionSendonly,
				Codecs: []*core.Codec{
					{Name: core.CodecAAC},
				},
			},
		}
	}

	return core.ParseQuery(query)
}
