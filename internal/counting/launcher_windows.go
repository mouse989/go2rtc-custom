//go:build windows

package counting

import (
	"os"
	"os/exec"
)

func setSysProcAttr(_ *exec.Cmd) {}

func killYoloProc(proc *os.Process) {
	if proc != nil {
		_ = proc.Kill()
	}
}
