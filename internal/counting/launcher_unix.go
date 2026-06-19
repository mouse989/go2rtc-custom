//go:build !windows

package counting

import (
	"os"
	"os/exec"
	"syscall"
)

// setSysProcAttr puts the child process into its own process group so that
// killing -pid (negative) sends the signal to the entire group, including any
// sub-processes spawned by yolo_counter (e.g. uvicorn workers, PyInstaller
// bootloader children).
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killYoloProc kills the process and its entire process group.
func killYoloProc(proc *os.Process) {
	if proc == nil {
		return
	}
	// Kill the whole process group (negative PID = group leader)
	_ = syscall.Kill(-proc.Pid, syscall.SIGKILL)
	_ = proc.Kill()
}

// killStaleOnPort uses fuser to forcibly kill any process holding the port.
func killStaleOnPort(port string) {
	out, err := exec.Command("fuser", "-k", "-KILL", port+"/tcp").CombinedOutput()
	if err == nil {
		log.Debug().Str("port", port).Str("out", string(out)).Msg("[counting] fuser killed stale process")
	}
}
