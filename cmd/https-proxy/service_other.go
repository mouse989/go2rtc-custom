//go:build !windows

package main

import "errors"

func isWindowsService() bool { return false }

func runService(run func(stop <-chan struct{})) { run(make(chan struct{})) }

func controlService(string, string) error {
	return errors.New("chỉ hỗ trợ trên Windows (dùng systemd trên Linux)")
}

func disableQuickEdit() {}
