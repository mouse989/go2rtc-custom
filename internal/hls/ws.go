package hls

import (
	"errors"

	"github.com/AlexxIT/go2rtc/internal/accesslog"
	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/api/ws"
	"github.com/AlexxIT/go2rtc/internal/auth"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/mp4"
)

func handlerWSHLS(tr *ws.Transport, msg *ws.Message) error {
	stream, _ := streams.GetOrPatch(tr.Request.URL.Query())
	if stream == nil {
		return errors.New(api.StreamNotFound)
	}

	codecs := msg.String()
	medias := mp4.ParseCodecs(codecs, true)
	cons := mp4.NewConsumer(medias)
	cons.FormatName = "hls/fmp4"
	cons.WithRequest(tr.Request)

	log.Trace().Msgf("[hls] new ws consumer codecs=%s", codecs)

	if err := stream.AddConsumer(cons); err != nil {
		log.Error().Err(err).Caller().Send()
		return err
	}

	user, _ := auth.UserFromContext(tr.Request.Context())
	session := registerSession(stream, cons, auth.ViewSessionLimit(user))
	if user != nil {
		accesslog.Record(user.Username, tr.Request.URL.Query().Get("src"), "hls-ws")
	}

	go session.Run()

	main := session.Main()
	tr.Write(&ws.Message{Type: "hls", Value: string(main)})

	return nil
}
