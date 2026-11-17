// AWGProxy Control — значок в строке меню (Mac) / в трее (Windows) для AWGProxy.
// Показывает, идёт ли трафик через Армению, включает и выключает VPN, держит его
// включённым в любой сети («сторож»), переключает серверы и собирает отчёт.
// Ключи из конфигов никогда не показываются и не попадают в отчёт.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"awgproxy-control/tray"
)

// version подставляется при сборке: -ldflags "-X main.version=1.2.3"
var version = "dev"

// ---------- настройки ----------

type Settings struct {
	Guard        bool `json:"guard"`
	NoAutoUpdate bool `json:"no_auto_update"`
}

var (
	settings   Settings
	settingsMu sync.Mutex
)

func configDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d = os.TempDir()
	}
	d = filepath.Join(d, "AWGProxyControl")
	_ = os.MkdirAll(d, 0o700)
	return d
}

func loadSettings() {
	b, err := os.ReadFile(filepath.Join(configDir(), "settings.json"))
	if err == nil {
		_ = json.Unmarshal(b, &settings)
	}
}

func saveSettings() {
	settingsMu.Lock()
	b, _ := json.MarshalIndent(settings, "", "  ")
	settingsMu.Unlock()
	_ = os.WriteFile(filepath.Join(configDir(), "settings.json"), b, 0o600)
}

func guardOn() bool {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	return settings.Guard
}

func autoUpdateOn() bool {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	return !settings.NoAutoUpdate
}

func setAutoUpdate(on bool) {
	settingsMu.Lock()
	settings.NoAutoUpdate = !on
	settingsMu.Unlock()
	saveSettings()
}

func quitApp() { tray.Quit() }

func setGuard(on bool) {
	settingsMu.Lock()
	settings.Guard = on
	settingsMu.Unlock()
	saveSettings()
}

// ---------- состояние ----------

type Status struct {
	TunnelRunning bool
	TunnelIP      string
	TunnelPlace   string
	DirectIP      string
	DirectPlace   string
	ProxyOn       bool
	ProxyDetail   string
	Profile       string
	Checked       time.Time
}

var (
	stMu    sync.Mutex
	st      Status
	busy    atomic.Bool // идёт переключение / включение — сторож ждёт
	infoMu  sync.Mutex
	infoMsg string
)

func setInfo(s string) {
	infoMu.Lock()
	infoMsg = s
	infoMu.Unlock()
	if s != "" {
		log.Println("info:", s)
	}
	updateMenu()
}

func getStatus() Status {
	stMu.Lock()
	defer stMu.Unlock()
	return st
}

// refresh проверяет туннель, прокси и IP и обновляет меню.
func refresh() Status {
	s := Status{Checked: time.Now()}
	s.TunnelRunning = tunnelRunning()
	s.ProxyOn, s.ProxyDetail = proxyState()
	s.Profile = activeProfile()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if s.TunnelRunning {
			s.TunnelIP = publicIP(true)
			s.TunnelPlace = placeOf(s.TunnelIP, true)
		}
	}()
	go func() {
		defer wg.Done()
		s.DirectIP = publicIP(false)
		s.DirectPlace = placeOf(s.DirectIP, false)
	}()
	wg.Wait()
	stMu.Lock()
	st = s
	stMu.Unlock()
	updateMenu()
	return s
}

// ---------- меню ----------

var (
	mStatus, mDirect, mInfo        *tray.MenuItem
	mToggle, mGuard, mServer       *tray.MenuItem
	mDiag, mFolder, mSetup, mLogin *tray.MenuItem
	mQuit, mUpdate, mAutoUpd       *tray.MenuItem
	profileItems                   = map[string]*tray.MenuItem{}
	menuReady                      atomic.Bool
	menuMu                         sync.Mutex
)

func vpnOn(s Status) bool { return s.TunnelRunning || s.ProxyOn }

func updateMenu() {
	if !menuReady.Load() {
		return
	}
	menuMu.Lock()
	defer menuMu.Unlock()
	s := getStatus()
	infoMu.Lock()
	info := infoMsg
	infoMu.Unlock()

	var icon []byte
	switch {
	case s.Checked.IsZero():
		mStatus.SetTitle("Проверяю…")
		icon = iconGray
	case s.TunnelRunning && s.TunnelIP != "" && s.ProxyOn:
		mStatus.SetTitle("● Через Армению: " + s.TunnelIP + place(s.TunnelPlace))
		icon = iconGreen
	case s.TunnelRunning && s.TunnelIP != "" && !s.ProxyOn:
		mStatus.SetTitle("◐ Туннель работает, но системный прокси выключен")
		icon = iconYellow
	case s.TunnelRunning && s.TunnelIP == "":
		mStatus.SetTitle("✕ Туннель запущен, но сервер не отвечает")
		icon = iconRed
	case !s.TunnelRunning && s.ProxyOn:
		mStatus.SetTitle("✕ Прокси включён, а туннель остановлен — нет интернета")
		icon = iconRed
	default:
		mStatus.SetTitle("○ VPN выключен — всё идёт напрямую")
		icon = iconGray
	}
	tray.SetIcon(icon)
	if s.DirectIP != "" {
		mDirect.SetTitle("Напрямую: " + s.DirectIP + place(s.DirectPlace))
	} else if !s.Checked.IsZero() {
		mDirect.SetTitle("Напрямую: нет ответа")
	}
	tip := "AWGProxy"
	if s.TunnelIP != "" {
		tip += " — " + s.TunnelIP
	}
	tray.SetTooltip(tip)
	if info != "" {
		mInfo.SetTitle(info)
		mInfo.Show()
	} else {
		mInfo.Hide()
	}
	if vpnOn(s) {
		mToggle.SetTitle("Выключить VPN")
	} else {
		mToggle.SetTitle("Включить VPN")
	}
	if guardOn() {
		mGuard.Check()
	} else {
		mGuard.Uncheck()
	}
	for name, it := range profileItems {
		if name == s.Profile {
			it.Check()
		} else {
			it.Uncheck()
		}
	}
	if availableVersion != "" {
		mUpdate.SetTitle("Обновить до версии " + availableVersion)
	} else {
		mUpdate.SetTitle("Версия " + version + " — проверить обновления")
	}
	if autoUpdateOn() {
		mAutoUpd.Check()
	} else {
		mAutoUpd.Uncheck()
	}
	if mSetup != nil {
		if needsSetup() {
			mSetup.Show()
		} else {
			mSetup.Hide()
		}
	}
}

func place(p string) string {
	if p == "" {
		return ""
	}
	return " · " + p
}

func onReady() {
	tray.SetIcon(iconGray)
	tray.SetTooltip("AWGProxy")
	mStatus = tray.AddMenuItem("Проверяю…", "")
	mStatus.Disable()
	mDirect = tray.AddMenuItem("Напрямую: …", "")
	mDirect.Disable()
	mInfo = tray.AddMenuItem("", "")
	mInfo.Disable()
	mInfo.Hide()
	tray.AddSeparator()
	mToggle = tray.AddMenuItem("Выключить VPN", "Туннель и системный прокси вместе")
	mGuard = tray.AddMenuItemCheckbox("Всегда через Армению (сторож)",
		"В любой сети сам включает прокси и перезапускает туннель", guardOn())
	mServer = tray.AddMenuItem("Сервер", "")
	for _, p := range listProfiles() {
		it := mServer.AddSubMenuItemCheckbox(profileTitle(p), "", false)
		profileItems[p] = it
		go func(name string, it *tray.MenuItem) {
			for range it.ClickedCh {
				go switchProfile(name)
			}
		}(p, it)
	}
	if len(profileItems) == 0 {
		it := mServer.AddSubMenuItem("Профилей нет", "")
		it.Disable()
	}
	tray.AddSeparator()
	mDiag = tray.AddMenuItem("Проверить и скопировать отчёт", "Отчёт без ключей — можно прислать Claude")
	mFolder = tray.AddMenuItem("Открыть папку профилей", "")
	if setupSupported() {
		mSetup = tray.AddMenuItem("Настроить права (один раз, нужен администратор)", "")
		mSetup.Hide()
	}
	mLogin = tray.AddMenuItemCheckbox("Запускать при входе", "", autostartEnabled())
	mUpdate = tray.AddMenuItem("Версия "+version+" — проверить обновления", "")
	mAutoUpd = tray.AddMenuItemCheckbox("Обновлять автоматически", "", autoUpdateOn())
	tray.AddSeparator()
	mQuit = tray.AddMenuItem("Выход (сторож остановится)", "")
	menuReady.Store(true)
	updateMenu()

	go func() {
		for range mToggle.ClickedCh {
			go toggleVPN()
		}
	}()
	go func() {
		for range mGuard.ClickedCh {
			if guardOn() {
				setGuard(false)
				setInfo("Сторож выключен")
			} else {
				setGuard(true)
				guardTakeover()
				setInfo("Сторож включён: Армения в любой сети")
				go guardPass(true)
			}
		}
	}()
	go func() {
		for range mDiag.ClickedCh {
			go diagnose()
		}
	}()
	go func() {
		for range mFolder.ClickedCh {
			_ = os.MkdirAll(profilesDir(), 0o700)
			openPath(profilesDir())
		}
	}()
	if mSetup != nil {
		go func() {
			for range mSetup.ClickedCh {
				go func() {
					if err := runSetupElevated(); err != nil {
						setInfo("Настройка прав не запущена: " + err.Error())
						return
					}
					setInfo("Подтвердите запрос Windows «Да»…")
					for i := 0; i < 60 && needsSetup(); i++ {
						time.Sleep(2 * time.Second)
					}
					if needsSetup() {
						setInfo("Права не настроены — нажмите пункт ещё раз")
					} else {
						setInfo("Права настроены")
						reloadProfilesHint()
					}
					refresh()
				}()
			}
		}()
	}
	go func() {
		for range mLogin.ClickedCh {
			on := !autostartEnabled()
			if err := setAutostart(on); err != nil {
				setInfo("Автозапуск: " + err.Error())
			}
			if autostartEnabled() {
				mLogin.Check()
			} else {
				mLogin.Uncheck()
			}
		}
	}()
	go func() {
		for range mUpdate.ClickedCh {
			go checkUpdate(true)
		}
	}()
	go func() {
		for range mAutoUpd.ClickedCh {
			setAutoUpdate(!autoUpdateOn())
			updateMenu()
		}
	}()
	go func() {
		<-mQuit.ClickedCh
		quitApp()
	}()

	go loop()
	go updateLoop()
}

func reloadProfilesHint() {
	if len(profileItems) == 0 && len(listProfiles()) > 0 {
		setInfo("Профили появились — перезапустите приложение, чтобы увидеть их в меню")
	}
}

// ---------- действия ----------

func toggleVPN() {
	if !busy.CompareAndSwap(false, true) {
		return
	}
	defer busy.Store(false)
	s := refresh()
	if vpnOn(s) {
		setGuard(false)
		setInfo("Выключаю…")
		if err := setProxy(false); err != nil {
			setInfo("Прокси: " + err.Error())
		}
		if err := stopTunnel(); err != nil {
			setInfo("Туннель: " + err.Error())
		} else {
			setInfo("VPN выключен")
		}
		refresh()
		return
	}
	setInfo("Включаю…")
	if err := startTunnel(); err != nil {
		setInfo("Туннель не запустился: " + err.Error())
		refresh()
		return
	}
	if ip := waitTunnel(25 * time.Second); ip == "" {
		setInfo("Сервер не ответил за 25 с — прокси не включаю, чтобы не пропал интернет")
		refresh()
		return
	}
	if err := setProxy(true); err != nil {
		setInfo("Прокси: " + err.Error())
	} else {
		setInfo("")
	}
	refresh()
}

func waitTunnel(d time.Duration) string {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if ip := publicIP(true); ip != "" {
			return ip
		}
		time.Sleep(2 * time.Second)
	}
	return ""
}

func switchProfile(name string) {
	if !busy.CompareAndSwap(false, true) {
		return
	}
	defer busy.Store(false)
	src := filepath.Join(profilesDir(), name+".conf")
	data, err := os.ReadFile(src)
	if err != nil {
		setInfo("Не читается профиль " + name + ": " + err.Error())
		return
	}
	data = ensureListeners(data)
	old, err := os.ReadFile(activeConf())
	if err != nil {
		setInfo("Не читается текущий конфиг: " + err.Error())
		return
	}
	setInfo("Переключаю на «" + profileTitle(name) + "», жду до 30 с…")
	if err := os.WriteFile(activeConf(), data, 0o600); err != nil {
		setInfo("Не записать конфиг: " + err.Error())
		return
	}
	if err := restartTunnel(); err != nil {
		log.Println("restart:", err)
	}
	if ip := waitTunnel(30 * time.Second); ip != "" {
		_ = os.WriteFile(activeProfileFile(), []byte(name+"\n"), 0o600)
		setInfo("Сервер: " + profileTitle(name) + " — " + ip)
	} else {
		_ = os.WriteFile(activeConf(), old, 0o600)
		_ = restartTunnel()
		setInfo("«" + profileTitle(name) + "» не ответил — вернул прежний сервер")
	}
	refresh()
}

// ensureListeners добавляет в профиль локальные SOCKS 1080 и HTTP 1081, если их нет.
func ensureListeners(b []byte) []byte {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	low := strings.ToLower(s)
	if !strings.Contains(low, "[socks5]") {
		s = strings.TrimRight(s, "\n") + "\n\n[Socks5]\nBindAddress = 127.0.0.1:1080\n"
	}
	if !strings.Contains(low, "[http]") {
		s = strings.TrimRight(s, "\n") + "\n\n[http]\nBindAddress = 127.0.0.1:1081\n"
	}
	return []byte(s)
}

// ---------- профили ----------

func listProfiles() []string {
	m, _ := filepath.Glob(filepath.Join(profilesDir(), "*.conf"))
	var out []string
	for _, p := range m {
		out = append(out, strings.TrimSuffix(filepath.Base(p), ".conf"))
	}
	sort.Strings(out)
	return out
}

func activeProfile() string {
	b, err := os.ReadFile(activeProfileFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func endpointOf(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(t), "endpoint") {
			if i := strings.Index(t, "="); i >= 0 {
				return strings.TrimSpace(t[i+1:])
			}
		}
	}
	return ""
}

func profileTitle(name string) string {
	known := map[string]string{
		"m6":      "Армения — Mac mini M6",
		"armenia": "Армения — Mac mini Intel",
		"main":    "Основной",
	}
	t := known[name]
	if t == "" {
		t = name
	}
	if ep := endpointOf(filepath.Join(profilesDir(), name+".conf")); ep != "" {
		if h, _, err := net.SplitHostPort(ep); err == nil {
			ep = h
		}
		t += "  [" + ep + "]"
	}
	return t
}

// ---------- сторож ----------

var tunnelFails int

// guardPass — одна проверка сторожа. force — проверить, даже если недавно проверяли.
func guardPass(force bool) {
	if !guardOn() || busy.Load() {
		return
	}
	if !tunnelRunning() {
		log.Println("guard: туннель остановлен — запускаю")
		_ = startTunnel()
		time.Sleep(3 * time.Second)
	}
	if on, _ := proxyState(); !on {
		log.Println("guard: прокси выключен — включаю")
		if err := setProxy(true); err != nil {
			log.Println("guard: прокси:", err)
		}
	}
	if publicIP(true) == "" {
		tunnelFails++
		if tunnelFails >= 2 && hasNetwork() {
			log.Println("guard: туннель не отвечает — перезапуск")
			_ = restartTunnel()
			tunnelFails = 0
		}
	} else {
		tunnelFails = 0
	}
}

func netSignature() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var parts []string
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			s := a.String()
			if strings.HasPrefix(s, "fe80") || strings.HasPrefix(s, "169.254") {
				continue
			}
			parts = append(parts, i.Name+"="+s)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func hasNetwork() bool { return netSignature() != "" }

func loop() {
	refresh()
	if guardOn() {
		guardTakeover()
		guardPass(true)
		refresh()
	}
	sig := netSignature()
	last := time.Now()
	t := time.NewTicker(5 * time.Second)
	for range t.C {
		ns := netSignature()
		changed := ns != sig
		if changed {
			log.Println("сеть изменилась")
			sig = ns
			time.Sleep(3 * time.Second) // дать сети подняться
		}
		if changed || time.Since(last) >= 30*time.Second {
			last = time.Now()
			if guardOn() {
				guardPass(changed)
			}
			if !busy.Load() {
				refresh()
			}
		}
	}
}

// ---------- отчёт ----------

func diagnose() {
	setInfo("Собираю отчёт…")
	s := refresh()
	var b strings.Builder
	fmt.Fprintf(&b, "AWGProxy Control %s — отчёт %s\n", version, time.Now().Format("2006-01-02 15:04:05"))
	host, _ := os.Hostname()
	fmt.Fprintf(&b, "Компьютер: %s (%s)\n", host, platformName())
	fmt.Fprintf(&b, "Туннель: %v\n", map[bool]string{true: "запущен", false: "остановлен"}[s.TunnelRunning])
	fmt.Fprintf(&b, "IP через туннель: %s%s\n", orDash(s.TunnelIP), place(s.TunnelPlace))
	fmt.Fprintf(&b, "IP напрямую: %s%s\n", orDash(s.DirectIP), place(s.DirectPlace))
	fmt.Fprintf(&b, "Системный прокси: %s\n", s.ProxyDetail)
	fmt.Fprintf(&b, "Сторож: %v\n", map[bool]string{true: "включён", false: "выключен"}[guardOn()])
	fmt.Fprintf(&b, "Активный профиль: %s; Endpoint в рабочем конфиге: %s\n", orDash(s.Profile), orDash(endpointOf(activeConf())))
	fmt.Fprintf(&b, "Профили: %s\n", strings.Join(listProfiles(), ", "))
	fmt.Fprintf(&b, "Сети: %s\n", netSignature())
	b.WriteString("\n--- система ---\n")
	b.WriteString(platformDiag())
	b.WriteString("\n--- журнал приложения (последние строки) ---\n")
	b.WriteString(tailFile(filepath.Join(configDir(), "control.log"), 25))
	report := redact(b.String())
	path := filepath.Join(configDir(), "report.txt")
	_ = os.WriteFile(path, []byte(report), 0o600)
	if err := copyToClipboard(report); err != nil {
		setInfo("Отчёт сохранён (в буфер не скопирован): " + path)
	} else {
		setInfo("Отчёт скопирован в буфер обмена — вставьте его Claude")
	}
	showFile(path)
}

// redact убирает из текста строки с ключами на всякий случай.
func redact(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		l := strings.ToLower(line)
		if strings.Contains(l, "privatekey") || strings.Contains(l, "presharedkey") || strings.Contains(l, "headerprotectionkey") {
			out = append(out, "[строка с ключом скрыта]")
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func tailFile(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "(нет)\n"
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}

// ---------- запуск ----------

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--setup" {
		f, _ := os.OpenFile(filepath.Join(os.TempDir(), "awgproxy-control-setup.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if f != nil {
			log.SetOutput(f)
		}
		if err := adminSetup(); err != nil {
			log.Println("setup:", err)
			os.Exit(1)
		}
		log.Println("setup: готово")
		return
	}
	lf, err := os.OpenFile(filepath.Join(configDir(), "control.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err == nil {
		if fi, _ := lf.Stat(); fi != nil && fi.Size() > 1<<20 {
			_ = lf.Truncate(0)
		}
		log.SetOutput(lf)
	}
	log.SetFlags(log.Ldate | log.Ltime)
	cleanupOldBinary()
	// одна копия приложения; после обновления старая копия ещё несколько секунд закрывается
	var ln net.Listener
	for i := 0; i < 30; i++ {
		if ln, err = net.Listen("tcp", "127.0.0.1:47391"); err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if ln == nil {
		log.Println("уже запущено — выходим")
		return
	}
	defer ln.Close()
	loadSettings()
	log.Println("старт", version, platformName())
	tray.Run(onReady, func() { log.Println("выход") })
}
