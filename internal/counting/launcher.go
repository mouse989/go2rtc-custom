package counting

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

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
		if err := cmd.Run(); err != nil {
			log.Warn().Err(err).Dur("retry", backoff).
				Msg("[counting] yolo_counter exited unexpectedly, restarting")
		} else {
			log.Info().Msg("[counting] yolo_counter stopped cleanly")
		}
		time.Sleep(backoff)
		if backoff < 60*time.Second {
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
