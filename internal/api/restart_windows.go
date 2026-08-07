//go:build windows

package api

import (
	"os"
	"os/exec"
)

// doRestart spawns a new instance and exits the current one — Windows has no
// POSIX exec()-style in-place process replacement (syscall.Exec is a stub
// there that always returns EWINDOWS and does nothing, which is why restart
// silently no-oped on Windows before this).
func doRestart(path string) {
	cmd := exec.Command(path, os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = os.Environ()
	if wd, err := os.Getwd(); err == nil {
		cmd.Dir = wd
	}
	if err := cmd.Start(); err != nil {
		log.Error().Err(err).Msg("[api] restart: failed to spawn new process")
		return
	}
	os.Exit(0)
}
