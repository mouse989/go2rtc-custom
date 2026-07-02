package counting

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// autoLaunchYolo finds yolo_counter[.exe/.bat/.cmd] next to the running binary
// and starts it as a supervised subprocess. Restarts on crash with exponential backoff.
// On Windows, .bat/.cmd files are launched via "cmd /c" so users can wrap a plain
// "python counter.py" call without needing a PyInstaller bundle.
func autoLaunchYolo() {
	yoloPath, found := findYoloPath()
	if !found {
		selfPath, _ := os.Executable()
		selfPath, _ = filepath.EvalSymlinks(selfPath)
		log.Info().Str("dir", filepath.Dir(selfPath)).
			Msg("[counting] yolo_counter not found — place it next to go2rtc for auto-launch")
		return
	}
	log.Info().Str("path", yoloPath).Msg("[counting] found yolo_counter, launching")
	go supervisedYolo(yoloPath)
}

// findYoloPath searches for yolo_counter next to the running binary.
// On Windows it tries .exe first, then .bat, then .cmd so a simple batch-file
// wrapper (e.g. "python counter.py %*") can substitute for a PyInstaller bundle.
func findYoloPath() (string, bool) {
	selfPath, err := os.Executable()
	if err != nil {
		return "", false
	}
	selfPath, _ = filepath.EvalSymlinks(selfPath)
	dir := filepath.Dir(selfPath)

	var candidates []string
	if runtime.GOOS == "windows" {
		candidates = []string{"yolo_counter.exe", "yolo_counter.bat", "yolo_counter.cmd"}
	} else {
		candidates = []string{"yolo_counter"}
	}
	for _, name := range candidates {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil {
			// Downloaded/copied files commonly lose the execute bit on Unix
			// (e.g. after `unzip`, or a browser/SFTP download), causing a
			// confusing "fork/exec: permission denied" instead of a clear
			// hint — fix it up front so auto-launch just works.
			if runtime.GOOS != "windows" && fi.Mode()&0o111 == 0 {
				_ = os.Chmod(p, fi.Mode()|0o755)
			}
			return p, true
		}
	}
	return "", false
}

// buildYoloCmd constructs the exec.Cmd for yoloPath.
// On Windows, .bat and .cmd files must be run via "cmd /c" because they are
// not directly executable by CreateProcess.
func buildYoloCmd(yoloPath string, args []string) *exec.Cmd {
	ext := strings.ToLower(filepath.Ext(yoloPath))
	if runtime.GOOS == "windows" && (ext == ".bat" || ext == ".cmd") {
		return exec.Command("cmd", append([]string{"/c", yoloPath}, args...)...)
	}
	return exec.Command(yoloPath, args...)
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

		cmd := buildYoloCmd(yoloPath, args)
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
