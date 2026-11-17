package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Windows: туннель — служба «awgproxy» (NSSM) в C:\awgproxy, системный прокси — настройки
// Интернета текущего пользователя (HKCU), 127.0.0.1:1081.

const (
	winDir  = `C:\awgproxy`
	svcName = "awgproxy"
	// права обычного пользователя на запуск/остановку службы (добавляются один раз при настройке)
	svcACE   = "(A;;CCLCSWRPWPDTLOCRRC;;;IU)"
	inetKey  = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	runKey   = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue = "AWGProxyControl"
)

func profilesDir() string       { return filepath.Join(winDir, "profiles") }
func activeConf() string        { return filepath.Join(winDir, "awgproxy.conf") }
func activeProfileFile() string { return filepath.Join(winDir, "active_profile") }
func platformName() string      { return "Windows" }

func hidden(c *exec.Cmd) *exec.Cmd {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return c
}

func run(name string, args ...string) (string, error) {
	out, err := hidden(exec.Command(name, args...)).CombinedOutput()
	return string(out), err
}

func svcState() string {
	out, err := run("sc.exe", "query", svcName)
	if err != nil && out == "" {
		return ""
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "STATE") {
			f := strings.Fields(l)
			if len(f) > 0 {
				return f[len(f)-1]
			}
		}
	}
	return ""
}

func tunnelRunning() bool { return svcState() == "RUNNING" }

// tunnelPID — номер процесса службы (0, если не запущена); смена номера = служба перезапускалась.
func tunnelPID() int {
	out, _ := run("sc.exe", "queryex", svcName)
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "PID") {
			f := strings.Fields(l)
			var n int
			fmt.Sscanf(f[len(f)-1], "%d", &n)
			return n
		}
	}
	return 0
}

func waitState(want string, d time.Duration) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if svcState() == want {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return svcState() == want
}

func startTunnel() error {
	if tunnelRunning() {
		return nil
	}
	out, err := run("sc.exe", "start", svcName)
	if !waitState("RUNNING", 15*time.Second) {
		return fmt.Errorf("служба не запустилась: %v %s", err, firstLine(out))
	}
	return nil
}

func stopTunnel() error {
	if svcState() == "STOPPED" {
		return nil
	}
	out, err := run("sc.exe", "stop", svcName)
	if !waitState("STOPPED", 15*time.Second) {
		return fmt.Errorf("служба не остановилась: %v %s", err, firstLine(out))
	}
	return nil
}

func restartTunnel() error {
	if err := stopTunnel(); err != nil {
		return err
	}
	return startTunnel()
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			return t
		}
	}
	return ""
}

func proxyState() (bool, string) {
	k, err := registry.OpenKey(registry.CURRENT_USER, inetKey, registry.QUERY_VALUE)
	if err != nil {
		return false, "не читается: " + err.Error()
	}
	defer k.Close()
	en, _, _ := k.GetIntegerValue("ProxyEnable")
	srv, _, _ := k.GetStringValue("ProxyServer")
	ok := en == 1 && strings.Contains(srv, "127.0.0.1:1081")
	return ok, fmt.Sprintf("ProxyEnable=%d ProxyServer=%s", en, srv)
}

func setProxy(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, inetKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if on {
		if err := k.SetStringValue("ProxyServer", "127.0.0.1:1081"); err != nil {
			return err
		}
		_ = k.SetStringValue("ProxyOverride", "localhost;127.*;10.*;172.16.*;192.168.*;<local>")
		if err := k.SetDWordValue("ProxyEnable", 1); err != nil {
			return err
		}
	} else if err := k.SetDWordValue("ProxyEnable", 0); err != nil {
		return err
	}
	// сообщить программам, что настройки прокси изменились
	wininet := windows.NewLazySystemDLL("wininet.dll")
	p := wininet.NewProc("InternetSetOptionW")
	_, _, _ = p.Call(0, 39, 0, 0) // INTERNET_OPTION_SETTINGS_CHANGED
	_, _, _ = p.Call(0, 37, 0, 0) // INTERNET_OPTION_REFRESH
	return nil
}

func guardTakeover() {}

func setupSupported() bool { return true }

// needsSetup — true, если обычный пользователь не может менять конфиг или управлять службой.
func needsSetup() bool {
	f, err := os.OpenFile(activeConf(), os.O_RDWR, 0)
	if err != nil {
		return true
	}
	f.Close()
	sd, _ := run("sc.exe", "sdshow", svcName)
	return !strings.Contains(sd, svcACE)
}

func runSetupElevated() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	args, _ := windows.UTF16PtrFromString("--setup")
	return windows.ShellExecute(0, verb, file, args, nil, windows.SW_HIDE)
}

// adminSetup запускается один раз с правами администратора (пункт меню «Настроить права»).
func adminSetup() error {
	// 1) папка C:\awgproxy доступна обычным пользователям (конфиг, профили, журналы)
	if out, err := run("icacls", winDir, "/grant", "*S-1-5-32-545:(OI)(CI)M", "/T", "/C"); err != nil {
		return fmt.Errorf("icacls: %v %s", err, out)
	}
	// 2) обычный пользователь может запускать и останавливать службу awgproxy
	sd, err := run("sc.exe", "sdshow", svcName)
	if err != nil {
		return fmt.Errorf("sdshow: %v %s", err, sd)
	}
	sd = strings.TrimSpace(sd)
	if !strings.Contains(sd, svcACE) {
		i := strings.Index(sd, "D:")
		if i < 0 {
			return fmt.Errorf("неожиданный формат прав службы: %s", sd)
		}
		j := i + 2
		// пропустить флаги DACL (например, D:P или D:AI) до первой «(»
		for j < len(sd) && sd[j] != '(' {
			j++
		}
		nsd := sd[:j] + svcACE + sd[j:]
		if out, err := run("sc.exe", "sdset", svcName, nsd); err != nil {
			return fmt.Errorf("sdset: %v %s", err, out)
		}
	}
	// 3) папка профилей: текущий рабочий конфиг становится профилем, если профилей нет
	_ = os.MkdirAll(profilesDir(), 0o755)
	if len(listProfiles()) == 0 {
		if b, err := os.ReadFile(activeConf()); err == nil {
			name := "main"
			_ = os.WriteFile(filepath.Join(profilesDir(), name+".conf"), b, 0o600)
			_ = os.WriteFile(activeProfileFile(), []byte(name+"\n"), 0o644)
		}
	}
	_, _ = run("icacls", winDir, "/grant", "*S-1-5-32-545:(OI)(CI)M", "/T", "/C")
	// 4) Amnezia со старым профилем мешает сети — остановить и не запускать
	if out, _ := run("sc.exe", "query", "AmneziaVPN-service"); strings.Contains(out, "STATE") {
		_, _ = run("sc.exe", "stop", "AmneziaVPN-service")
		_, _ = run("sc.exe", "config", "AmneziaVPN-service", "start=", "disabled")
	}
	return nil
}

func openPath(p string) { _ = hidden(exec.Command("explorer.exe", p)).Start() }
func showFile(p string) { _ = exec.Command("notepad.exe", p).Start() }

func copyToClipboard(s string) error {
	tmp := filepath.Join(configDir(), "clip.txt")
	if err := os.WriteFile(tmp, []byte("\xef\xbb\xbf"+s), 0o600); err != nil {
		return err
	}
	_, err := run("powershell", "-NoProfile", "-Command",
		"Get-Content -Raw -Encoding UTF8 -LiteralPath '"+tmp+"' | Set-Clipboard")
	_ = os.Remove(tmp)
	return err
}

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(runValue)
	return err == nil
}

func setAutostart(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		return k.DeleteValue(runValue)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(runValue, `"`+exe+`"`)
}

func platformDiag() string {
	var b strings.Builder
	q, _ := run("sc.exe", "query", svcName)
	b.WriteString("служба awgproxy: " + svcState() + "\n")
	_ = q
	_, p := proxyState()
	b.WriteString("прокси (HKCU): " + p + "\n")
	if needsSetup() {
		b.WriteString("права: НЕ настроены (пункт меню «Настроить права»)\n")
	} else {
		b.WriteString("права: настроены\n")
	}
	am, _ := run("sc.exe", "query", "AmneziaVPN-service")
	if strings.Contains(am, "STATE") {
		b.WriteString("AmneziaVPN-service: " + firstState(am) + "\n")
	}
	tl, _ := run("tasklist", "/FI", "IMAGENAME eq AmneziaVPN.exe", "/NH")
	if strings.Contains(tl, "AmneziaVPN.exe") {
		b.WriteString("ВНИМАНИЕ: запущено приложение AmneziaVPN\n")
	}
	// последний журнал службы
	logs, _ := filepath.Glob(filepath.Join(winDir, "awgproxy*.log"))
	sort.Slice(logs, func(i, j int) bool {
		a, _ := os.Stat(logs[i])
		c, _ := os.Stat(logs[j])
		return a != nil && c != nil && a.ModTime().After(c.ModTime())
	})
	for _, l := range logs {
		if fi, err := os.Stat(l); err == nil && fi.Size() > 0 {
			b.WriteString("журнал " + filepath.Base(l) + ":\n" + tailFile(l, 15))
			break
		}
	}
	return b.String()
}

func firstState(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "STATE") {
			f := strings.Fields(l)
			return f[len(f)-1]
		}
	}
	return ""
}
