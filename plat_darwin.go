package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// macOS: туннель — LaunchAgent com.awgproxy, файлы в ~/Library/Application Support/AWGProxy,
// системный прокси — networksetup для каждой сетевой службы.

func home() string { h, _ := os.UserHomeDir(); return h }

func awgDir() string { return filepath.Join(home(), "Library", "Application Support", "AWGProxy") }

func profilesDir() string       { return filepath.Join(awgDir(), "profiles") }
func activeConf() string        { return filepath.Join(awgDir(), "awgproxy.conf") }
func activeProfileFile() string { return filepath.Join(awgDir(), "active_profile") }
func platformName() string      { return "macOS" }

func uidDomain() string   { return fmt.Sprintf("gui/%d", os.Getuid()) }
func tunnelLabel() string { return uidDomain() + "/com.awgproxy" }
func tunnelPlist() string {
	return filepath.Join(home(), "Library", "LaunchAgents", "com.awgproxy.plist")
}

func run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// tunnelPID — номер процесса туннеля (0, если не запущен); смена номера = туннель перезапускался.
func tunnelPID() int {
	out, err := run("launchctl", "print", tunnelLabel())
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "pid = ") {
			var n int
			fmt.Sscanf(strings.TrimPrefix(l, "pid = "), "%d", &n)
			return n
		}
	}
	return 0
}

func tunnelRunning() bool {
	_, err := run("launchctl", "print", tunnelLabel())
	return err == nil
}

func startTunnel() error {
	if tunnelRunning() {
		return nil
	}
	if _, err := os.Stat(tunnelPlist()); err != nil {
		return fmt.Errorf("AWGProxy не установлен (нет %s)", tunnelPlist())
	}
	if out, err := run("launchctl", "bootstrap", uidDomain(), tunnelPlist()); err != nil {
		return fmt.Errorf("%v %s", err, strings.TrimSpace(out))
	}
	return nil
}

func stopTunnel() error {
	if !tunnelRunning() {
		return nil
	}
	if out, err := run("launchctl", "bootout", tunnelLabel()); err != nil {
		return fmt.Errorf("%v %s", err, strings.TrimSpace(out))
	}
	return nil
}

func restartTunnel() error {
	if !tunnelRunning() {
		return startTunnel()
	}
	_, err := run("launchctl", "kickstart", "-k", tunnelLabel())
	return err
}

func services() []string {
	out, err := run("networksetup", "-listallnetworkservices")
	if err != nil {
		return nil
	}
	var res []string
	for i, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(strings.TrimPrefix(l, "*"))
		if i == 0 || l == "" {
			continue
		}
		res = append(res, l)
	}
	return res
}

func proxyOK(out, port string) bool {
	return strings.Contains(out, "Enabled: Yes") && strings.Contains(out, "Port: "+port)
}

func proxyState() (bool, string) {
	svcs := services()
	if len(svcs) == 0 {
		return false, "нет сетевых служб"
	}
	all := true
	var parts []string
	for _, s := range svcs {
		socks, _ := run("networksetup", "-getsocksfirewallproxy", s)
		web, _ := run("networksetup", "-getwebproxy", s)
		sec, _ := run("networksetup", "-getsecurewebproxy", s)
		ok := proxyOK(socks, "1080") && proxyOK(web, "1081") && proxyOK(sec, "1081")
		if !ok {
			all = false
		}
		parts = append(parts, fmt.Sprintf("%s: %s", s, map[bool]string{true: "вкл", false: "ВЫКЛ"}[ok]))
	}
	return all, strings.Join(parts, "; ")
}

func setProxy(on bool) error {
	var errs []string
	for _, s := range services() {
		var cmds [][]string
		if on {
			cmds = [][]string{
				{"-setsocksfirewallproxy", s, "127.0.0.1", "1080"},
				{"-setwebproxy", s, "127.0.0.1", "1081"},
				{"-setsecurewebproxy", s, "127.0.0.1", "1081"},
			}
		} else {
			cmds = [][]string{
				{"-setsocksfirewallproxystate", s, "off"},
				{"-setwebproxystate", s, "off"},
				{"-setsecurewebproxystate", s, "off"},
			}
		}
		for _, c := range cmds {
			if out, err := run("networksetup", c...); err != nil {
				errs = append(errs, s+": "+strings.TrimSpace(out))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// guardTakeover выключает старого сторожа из .command-кнопок, чтобы они не работали вдвоём.
func guardTakeover() {
	_ = os.Remove(filepath.Join(awgDir(), "always-on"))
	_, _ = run("launchctl", "bootout", uidDomain()+"/com.awgproxy.guard")
}

func setupSupported() bool    { return false }
func needsSetup() bool        { return false }
func runSetupElevated() error { return nil }
func adminSetup() error       { return nil }

func openPath(p string) { _, _ = run("open", p) }
func showFile(p string) { _, _ = run("open", "-e", p) }

func copyToClipboard(s string) error {
	c := exec.Command("pbcopy")
	c.Stdin = strings.NewReader(s)
	return c.Run()
}

func autostartPlist() string {
	return filepath.Join(home(), "Library", "LaunchAgents", "com.awgproxy.control.plist")
}

func autostartEnabled() bool { _, err := os.Stat(autostartPlist()); return err == nil }

func setAutostart(on bool) error {
	if !on {
		return os.Remove(autostartPlist())
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.awgproxy.control</string>
  <key>ProgramArguments</key><array><string>` + xmlEscape(exe) + `</string></array>
  <key>RunAtLoad</key><true/>
  <key>LimitLoadToSessionType</key><string>Aqua</string>
  <key>ProcessType</key><string>Interactive</string>
</dict></plist>
`
	_ = os.MkdirAll(filepath.Dir(autostartPlist()), 0o755)
	return os.WriteFile(autostartPlist(), []byte(plist), 0o644)
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func platformDiag() string {
	var b strings.Builder
	out, _ := run("route", "-n", "get", "default")
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "interface:") || strings.Contains(l, "gateway:") {
			b.WriteString(strings.TrimSpace(l) + "\n")
		}
	}
	_, p := proxyState()
	b.WriteString("networksetup: " + p + "\n")
	sc, _ := run("scutil", "--proxy")
	b.WriteString("scutil --proxy:\n" + sc)
	if _, err := os.Stat(filepath.Join(awgDir(), "always-on")); err == nil {
		b.WriteString("старый сторож (.command): включён\n")
	}
	b.WriteString("журнал туннеля:\n" + tailFile(filepath.Join(awgDir(), "log.txt"), 15))
	return b.String()
}
