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
