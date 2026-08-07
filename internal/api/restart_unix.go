//go:build !windows

package api

import (
	"os"
	"syscall"
)

// doRestart replaces the current process image in place — same PID, no
// window where the port is unbound between old and new.
func doRestart(path string) {
	if err := syscall.Exec(path, os.Args, os.Environ()); err != nil {
		log.Error().Err(err).Msg("[api] restart: exec failed")
	}
}
