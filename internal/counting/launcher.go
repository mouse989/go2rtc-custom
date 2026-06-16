package counting

import (
	"fmt"
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
	_ = proc.Kill()
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

		cmd := exec.Command(yoloPath, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Dir = filepath.Dir(yoloPath)

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
			backoff = 3 * time.Second // reconnect quickly, this wasn't a crash
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

func yoloArgs(c Config) []string {
	port := "8765"
	if c.YoloURL != "" {
		if u, err := url.Parse(c.YoloURL); err == nil && u.Port() != "" {
			port = u.Port()
		}
	}
	conf := "0.35"
	if c.YoloConf > 0 {
		conf = fmt.Sprintf("%.2f", c.YoloConf)
	}
	model := c.YoloModel
	if model == "" {
		model = "yolo11n.pt"
	}
	return []string{
		"--port", port,
		"--model", model,
		"--conf", conf,
		"--rtsp-base", "rtsp://localhost:8554",
	}
}
