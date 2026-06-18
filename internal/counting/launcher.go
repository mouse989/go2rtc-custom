package counting

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

var (
	yoloProcMu     sync.Mutex
	yoloProc       *os.Process
	yoloRestarting bool
)

// restartYolo kills the currently-running yolo_counter subprocess, if any.
// supervisedYolo's loop notices the exit, re-reads config (picking up any
// new --model), and relaunches it — this is how a model change made in the
// UI takes effect without restarting the whole go2rtc binary.
func restartYolo() bool {
	yoloProcMu.Lock()
	proc := yoloProc
	yoloRestarting = true
	yoloProcMu.Unlock()
	if proc == nil {
		return false
	}
	killYoloProc(proc) // kills entire process group on Unix
	return true
}

// autoLaunchYolo finds yolo_counter[.exe] next to the running binary and
// starts it as a supervised subprocess. Restarts on crash with exponential backoff.
// If the file is not found, logs an info message and returns.
func autoLaunchYolo() {
	exeName := "yolo_counter"
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}

	selfPath, err := os.Executable()
	if err != nil {
		return
	}
	selfPath, _ = filepath.EvalSymlinks(selfPath)
	yoloPath := filepath.Join(filepath.Dir(selfPath), exeName)

	if _, err := os.Stat(yoloPath); os.IsNotExist(err) {
		log.Info().Str("path", yoloPath).
			Msg("[counting] yolo_counter not found — place it next to go2rtc for auto-launch")
		return
	}

	log.Info().Str("path", yoloPath).Msg("[counting] found yolo_counter, launching")
	go supervisedYolo(yoloPath)
}

func supervisedYolo(yoloPath string) {
	backoff := 3 * time.Second
	for {
		c := getConfig()
		args := yoloArgs(c)
		port := yoloPort(c)

		// Kill any stale process that is still holding our port.
		// This covers PyInstaller bundles whose child processes survive the parent's
		// death, and uvicorn workers that didn't receive the previous SIGKILL.
		ensurePortFree(port)

		cmd := exec.Command(yoloPath, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Dir = filepath.Dir(yoloPath)
		setSysProcAttr(cmd) // platform-specific: assign own process group

		log.Info().Strs("args", args).Msg("[counting] starting yolo_counter subprocess")
		if err := cmd.Start(); err != nil {
			log.Warn().Err(err).Msg("[counting] failed to start yolo_counter")
			time.Sleep(backoff)
			if backoff < 60*time.Second {
				backoff *= 2
			}
			continue
		}
		yoloProcMu.Lock()
		yoloProc = cmd.Process
		yoloProcMu.Unlock()

		err := cmd.Wait()

		yoloProcMu.Lock()
		yoloProc = nil
		deliberate := yoloRestarting
		yoloRestarting = false
		yoloProcMu.Unlock()

		if deliberate {
			log.Info().Msg("[counting] yolo_counter restarted (config change)")
			backoff = 3 * time.Second
		} else if err != nil {
			log.Warn().Err(err).Dur("retry", backoff).
				Msg("[counting] yolo_counter exited unexpectedly, restarting")
		} else {
			log.Info().Msg("[counting] yolo_counter stopped cleanly")
		}
		time.Sleep(backoff)
		if !deliberate && backoff < 60*time.Second {
			backoff *= 2
		}
	}
}

// ensurePortFree kills any process holding the target port and waits until the
// port is actually free (up to 5 seconds). The actual kill mechanism is
// platform-specific (see launcher_unix.go / launcher_windows.go).
func ensurePortFree(port string) {
	addr := "127.0.0.1:" + port

	// Quick check: is the port already free?
	if isPortFree(addr) {
		return
	}

	log.Warn().Str("port", port).Msg("[counting] port still in use before restart, killing stale process")

	killStaleOnPort(port) // platform-specific implementation

	// Wait up to 5 s for the port to free up
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		if isPortFree(addr) {
			log.Info().Str("port", port).Msg("[counting] port is now free")
			return
		}
	}
	log.Warn().Str("port", port).Msg("[counting] port still busy after 5s, starting anyway")
}

func isPortFree(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err != nil {
		return true // nothing listening → free
	}
	conn.Close()
	return false
}

func yoloPort(c Config) string {
	if c.YoloURL != "" {
		if u, err := url.Parse(c.YoloURL); err == nil && u.Port() != "" {
			return u.Port()
		}
	}
	return "8765"
}

func yoloArgs(c Config) []string {
	conf := "0.35"
	if c.YoloConf > 0 {
		conf = fmt.Sprintf("%.2f", c.YoloConf)
	}
	model := c.YoloModel
	if model == "" {
		model = "yolo11n.pt"
	}
	args := []string{
		"--port", yoloPort(c),
		"--model", model,
		"--conf", conf,
		"--rtsp-base", "rtsp://localhost:8554",
	}
	if d := c.YoloDevice; d != "" && d != "auto" {
		args = append(args, "--device", d)
	}
	return args
}
