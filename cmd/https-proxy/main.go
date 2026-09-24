// https-proxy is a small standalone HTTPS reverse proxy for Windows (and
// other OSes): it owns ports 80/443, obtains Let's Encrypt certificates
// automatically, and forwards each domain to a local website — go2rtc and
// any other web app on the same server. Running it as its own process means
// a busy, hung or restarting go2rtc no longer takes the other sites down.
//
// Usage:
//
//	https-proxy.exe                     run (config: https-proxy.json next to the exe)
//	https-proxy.exe -c D:\proxy\cfg.json
//	https-proxy.exe -import D:\go2rtc\reverse_proxy.json
//	https-proxy.exe -service install|uninstall|start|stop   (Windows service)
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

const serviceName = "HttpsProxy"

func main() {
	var configFlag, importFlag, serviceFlag string
	flag.StringVar(&configFlag, "c", "", "config file (default: https-proxy.json next to the executable)")
	flag.StringVar(&importFlag, "import", "", "import sites from go2rtc's reverse_proxy.json")
	flag.StringVar(&serviceFlag, "service", "", "Windows service: install, uninstall, start, stop")
	flag.Parse()

	exe, _ := os.Executable()
	exeDir := filepath.Dir(exe)
	if configFlag == "" {
		configFlag = filepath.Join(exeDir, "https-proxy.json")
	}
	cfgPath, _ = filepath.Abs(configFlag)

	if serviceFlag != "" {
		if err := controlService(serviceFlag, cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, "service:", err)
			os.Exit(1)
		}
		return
	}

	if isWindowsService() {
		runService(func(stop <-chan struct{}) {
			if err := start(importFlag); err != nil {
				logf("[main] %v", err)
				return
			}
			<-stop
		})
		return
	}

	disableQuickEdit()
	if err := start(importFlag); err != nil {
		fmt.Fprintln(os.Stderr, err)
		time.Sleep(10 * time.Second) // keep the console window readable
		os.Exit(1)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	time.AfterFunc(2*time.Second, func() { os.Exit(0) })
	logf("[main] dừng")
}

func start(importPath string) error {
	initLogging(filepath.Join(filepath.Dir(cfgPath), "https-proxy.log"))

	c, err := loadConfig(cfgPath)
	firstRun := errors.Is(err, os.ErrNotExist)
	switch {
	case firstRun:
		c = defaultConfig()
		// migrating from go2rtc's embedded proxy: pick up its site list
		if importPath == "" {
			if p := filepath.Join(filepath.Dir(cfgPath), "reverse_proxy.json"); fileExists(p) {
				importPath = p
			}
		}
	case err != nil:
		return fmt.Errorf("lỗi đọc cấu hình %s: %w", cfgPath, err)
	}

	if importPath != "" {
		n, err := importGo2rtcSites(c, importPath)
		if err != nil {
			logf("[main] không nhập được %s: %v", importPath, err)
		} else {
			logf("[main] đã nhập %d trang từ %s", n, importPath)
		}
	}

	if err = ensureAdminPassword(c); err != nil {
		return err
	}
	if err = validateConfig(c); err != nil {
		return fmt.Errorf("cấu hình không hợp lệ (%s): %w", cfgPath, err)
	}
	if err = saveConfig(c); err != nil {
		return fmt.Errorf("không ghi được %s: %w", cfgPath, err)
	}

	cfgMu.Lock()
	cfg = c
	cfgMu.Unlock()

	logf("[main] https-proxy khởi động, cấu hình: %s", cfgPath)
	apply(c)
	logf("[main] trang quản trị: http://%s", adminURLHost(c.AdminListen))
	return nil
}

func adminURLHost(addr string) string {
	if len(addr) > 0 && addr[0] == ':' {
		return "127.0.0.1" + addr
	}
	return addr
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
