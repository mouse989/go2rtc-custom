package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func isWindowsService() bool {
	ok, _ := svc.IsWindowsService()
	return ok
}

type service struct {
	run func(stop <-chan struct{})
}

func (s *service) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		s.run(stop)
		close(done)
	}()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				close(stop)
				select {
				case <-done:
				case <-time.After(3 * time.Second):
				}
				return false, 0
			}
		case <-done:
			return false, 1 // exited on its own (startup error) → let recovery restart it
		}
	}
}

func runService(run func(stop <-chan struct{})) {
	_ = svc.Run(serviceName, &service{run: run})
}

func controlService(action, configPath string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("%w (cần chạy bằng quyền Administrator)", err)
	}
	defer m.Disconnect()

	switch action {
	case "install":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if s, err := m.OpenService(serviceName); err == nil {
			s.Close()
			return errors.New("service đã được cài")
		}
		s, err := m.CreateService(serviceName, exe, mgr.Config{
			DisplayName: "HTTPS Proxy (go2rtc & web sites)",
			Description: "Reverse proxy HTTPS dùng chung chứng chỉ cho go2rtc và các trang web khác trên máy chủ.",
			StartType:   mgr.StartAutomatic,
		}, "-c", configPath)
		if err != nil {
			return err
		}
		defer s.Close()
		// restart automatically if the process ever dies
		_ = s.SetRecoveryActions([]mgr.RecoveryAction{
			{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
			{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
			{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		}, 86400)
		fmt.Println("Đã cài service", serviceName, "— chạy: https-proxy.exe -service start")
		return nil

	case "uninstall":
		s, err := m.OpenService(serviceName)
		if err != nil {
			return err
		}
		defer s.Close()
		_, _ = s.Control(svc.Stop)
		if err = s.Delete(); err != nil {
			return err
		}
		fmt.Println("Đã gỡ service", serviceName)
		return nil

	case "start":
		s, err := m.OpenService(serviceName)
		if err != nil {
			return err
		}
		defer s.Close()
		if err = s.Start(); err != nil {
			return err
		}
		fmt.Println("Đã khởi động service", serviceName)
		return nil

	case "stop":
		s, err := m.OpenService(serviceName)
		if err != nil {
			return err
		}
		defer s.Close()
		if _, err = s.Control(svc.Stop); err != nil {
			return err
		}
		fmt.Println("Đã dừng service", serviceName)
		return nil
	}
	return fmt.Errorf("không rõ lệnh %q (install, uninstall, start, stop)", action)
}

// disableQuickEdit: a mouse click into a console window with QuickEdit on
// freezes all console output until a key is pressed; turn it off.
func disableQuickEdit() {
	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return
	}
	_ = windows.SetConsoleMode(h, mode&^windows.ENABLE_QUICK_EDIT_MODE|windows.ENABLE_EXTENDED_FLAGS)
}
