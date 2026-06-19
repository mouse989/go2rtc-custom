//go:build windows

package counting

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func setSysProcAttr(_ *exec.Cmd) {}

func killYoloProc(proc *os.Process) {
	if proc == nil {
		return
	}
	// /T kills the process tree (all child processes) in addition to the parent.
	_ = exec.Command("taskkill", "/PID", strconv.Itoa(proc.Pid), "/F", "/T").Run()
	_ = proc.Kill()
}

// killStaleOnPort parses `netstat -ano` to find the PID listening on the port
// then kills it (plus its children) with taskkill /F /T.
func killStaleOnPort(port string) {
	out, err := exec.Command("netstat", "-ano").Output()
	if err != nil {
		return
	}
	target := ":" + port
	killed := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		// netstat line format: Proto  LocalAddr  ForeignAddr  State  PID
		// e.g.:  TCP    0.0.0.0:8765    0.0.0.0:0    LISTENING    4321
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 5 {
			continue
		}
		// Match exact port (":8765" but not ":87650")
		localAddr := fields[1]
		if !strings.HasSuffix(localAddr, target) {
			continue
		}
		// Skip system idle process
		pid := fields[len(fields)-1]
		if pid == "0" || killed[pid] {
			continue
		}
		killed[pid] = true
		if err := exec.Command("taskkill", "/PID", pid, "/F", "/T").Run(); err == nil {
			log.Debug().Str("pid", pid).Str("port", port).Msg("[counting] taskkill killed stale process")
		}
	}
}
