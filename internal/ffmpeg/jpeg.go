package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"time"

	"github.com/AlexxIT/go2rtc/internal/ffmpeg/hardware"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/ffmpeg"
	"github.com/AlexxIT/go2rtc/pkg/shell"
)

func JPEGWithQuery(b []byte, query url.Values) ([]byte, error) {
	return JPEGWithQueryContext(context.Background(), b, query)
}

// JPEGWithQueryContext is JPEGWithQuery that gives up (and kills ffmpeg)
// when ctx is cancelled, e.g. because the HTTP client went away.
func JPEGWithQueryContext(ctx context.Context, b []byte, query url.Values) ([]byte, error) {
	args := parseQuery(query)
	return transcode(ctx, b, args.String())
}

func JPEGWithScale(b []byte, width, height int) ([]byte, error) {
	args := defaultArgs()
	args.AddFilter(fmt.Sprintf("scale=%d:%d", width, height))
	return transcode(context.Background(), b, args.String())
}

// jpegSlots caps how many ffmpeg JPEG transcodes run at once. Snapshot
// schedulers polling hundreds of cameras used to spawn one ffmpeg.exe per
// camera simultaneously, saturating the CPU; excess requests now queue.
var jpegSlots = make(chan struct{}, max(2, runtime.NumCPU()))

const (
	jpegQueueTimeout = 20 * time.Second // max wait for a free slot
	jpegRunTimeout   = 15 * time.Second // max ffmpeg run time (kills hung ffmpeg)
)

func transcode(parent context.Context, b []byte, args string) ([]byte, error) {
	queue := time.NewTimer(jpegQueueTimeout)
	defer queue.Stop()
	select {
	case jpegSlots <- struct{}{}:
		defer func() { <-jpegSlots }()
	case <-queue.C:
		return nil, errors.New("ffmpeg: jpeg transcode queue is full")
	case <-parent.Done():
		return nil, parent.Err()
	}

	ctx, cancel := context.WithTimeout(parent, jpegRunTimeout)
	defer cancel()

	cmdArgs := shell.QuoteSplit(args)
	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	cmd.Stdin = bytes.NewBuffer(b)
	cmd.WaitDelay = time.Second // don't hang on inherited pipes after kill
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, errors.New("ffmpeg: jpeg transcode timeout")
	}
	return out, err
}

func defaultArgs() *ffmpeg.Args {
	return &ffmpeg.Args{
		Bin:    defaults["bin"],
		Global: defaults["global"],
		Input:  "-i -",
		Codecs: []string{defaults["mjpeg"]},
		Output: defaults["output/mjpeg"],
	}
}

func parseQuery(query url.Values) *ffmpeg.Args {
	args := defaultArgs()

	var width = -1
	var height = -1
	var r, hw string

	for k, v := range query {
		switch k {
		case "width", "w":
			width = core.Atoi(v[0])
		case "height", "h":
			height = core.Atoi(v[0])
		case "rotate":
			r = v[0]
		case "hardware", "hw":
			hw = v[0]
		}
	}

	if width > 0 || height > 0 {
		args.AddFilter(fmt.Sprintf("scale=%d:%d", width, height))
	}

	if r != "" {
		switch r {
		case "90":
			args.AddFilter("transpose=1") // 90 degrees clockwise
		case "180":
			args.AddFilter("transpose=1,transpose=1")
		case "-90", "270":
			args.AddFilter("transpose=2") // 90 degrees counterclockwise
		}
	}

	if hw != "" {
		hardware.MakeHardware(args, hw, defaults)
	}

	return args
}
