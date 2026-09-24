package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// Log lines go to a memory ring (shown on the admin page), a size-capped
// file next to the config, and the console. Console writes are
// asynchronous and dropped when the console is stalled, so a frozen
// console window can never block request handling.

const ringSize = 500

var (
	logMu   sync.Mutex
	logRing []string
	logFile *os.File
	logPath string
	logSize int64
	console = make(chan string, 1024)
)

func initLogging(path string) {
	logPath = path
	openLogFile()
	go func() {
		for line := range console {
			_, _ = io.WriteString(os.Stdout, line)
		}
	}()
}

func openLogFile() {
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	logFile = f
	if fi, err := f.Stat(); err == nil {
		logSize = fi.Size()
	}
}

func logf(format string, args ...any) {
	line := time.Now().Format("2006-01-02 15:04:05 ") + fmt.Sprintf(format, args...)
	line = strings.TrimRight(line, "\n") + "\n"

	logMu.Lock()
	logRing = append(logRing, line)
	if len(logRing) > ringSize {
		logRing = logRing[len(logRing)-ringSize:]
	}
	if logFile != nil {
		n, _ := logFile.WriteString(line)
		logSize += int64(n)
		if logSize > 10<<20 { // rotate at 10 MB, keep one old file
			_ = logFile.Close()
			_ = os.Rename(logPath, logPath+".1")
			logFile = nil
			logSize = 0
			openLogFile()
		}
	}
	logMu.Unlock()

	select {
	case console <- line:
	default:
	}
}

func recentLog() []string {
	logMu.Lock()
	defer logMu.Unlock()
	return append([]string(nil), logRing...)
}

// serverErrorWriter routes net/http server errors into logf, dropping the
// constant background noise of scanners and failed TLS handshakes.
type serverErrorWriter struct{}

func (serverErrorWriter) Write(p []byte) (int, error) {
	s := string(p)
	if strings.Contains(s, "TLS handshake error") || strings.Contains(s, "http2: server: error reading preface") {
		return len(p), nil
	}
	logf("[http] %s", strings.TrimSpace(s))
	return len(p), nil
}

func newServerErrorLog() *log.Logger {
	return log.New(serverErrorWriter{}, "", 0)
}
