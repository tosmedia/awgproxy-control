package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Строгий режим (macOS): брандмауэр pf пропускает наружу только сам туннель к серверам AWG
// (адреса из всех профилей), локальную сеть и DHCP. Если сервер не отвечает — интернета нет
// ни у одной программы, даже у тех, что не слушают системный прокси. DNS наружу закрыт.
//
// Устройство: один раз (пароль администратора) ставится системная служба
// com.awgproxy.killswitch и скрипт apply.sh (владелец root). Служба следит за файлом
// killswitch.conf в папке AWGProxy пользователя и применяет его: «mode on/off», адреса
// серверов и исключения. Дальше приложение включает и выключает режим без пароля.
// Правила живут в отдельном якоре pf com.apple/250.AWGProxyKillSwitch, чужие правила не трогаются.
// После перезагрузки служба сама применяет последнее состояние.

const (
	ksDir    = "/Library/Application Support/AWGProxyKillSwitch"
	ksDaemon = "/Library/LaunchDaemons/com.awgproxy.killswitch.plist"
	ksLabel  = "com.awgproxy.killswitch"
	ksAnchor = "com.apple/250.AWGProxyKillSwitch"
)

func killSwitchSupported() bool { return true }

func ksStateFile() string { return filepath.Join(awgDir(), "killswitch.conf") }
func ksAllowFile() string { return filepath.Join(awgDir(), "killswitch-allow.txt") }
func ksStatusFile() string { return filepath.Join(ksDir, "status") }

func ksInstalled() bool {
	_, e1 := os.Stat(filepath.Join(ksDir, "apply.sh"))
	_, e2 := os.Stat(ksDaemon)
	return e1 == nil && e2 == nil
}

// ksStatus — то, что последний раз записала служба: «on …», «off …» или «error …».
func ksStatus() string {
	b, err := os.ReadFile(ksStatusFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// killSwitchActive — правила блокировки сейчас загружены.
func killSwitchActive() bool { return strings.HasPrefix(ksStatus(), "on ") }

// ksWanted — что записано в файле состояния (желаемый режим).
func ksWanted() bool {
	b, err := os.ReadFile(ksStateFile())
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == "mode on" {
			return true
		}
	}
	return false
}

// ksEndpoints — адреса и порты серверов из всех профилей и из рабочего конфига.
func ksEndpoints() []string {
	files, _ := filepath.Glob(filepath.Join(profilesDir(), "*.conf"))
	files = append(files, activeConf())
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		ep := endpointOf(f)
		host, port, err := net.SplitHostPort(ep)
		if err != nil {
			continue
		}
		if p, err := strconv.Atoi(port); err != nil || p <= 0 || p > 65535 {
			continue
		}
		var ips []string
		if ip := net.ParseIP(host); ip != nil {
			if ip.To4() != nil {
				ips = []string{ip.String()}
			}
		} else if addrs, err := net.LookupIP(host); err == nil {
			for _, a := range addrs {
				if a.To4() != nil {
					ips = append(ips, a.String())
				}
			}
		}
		for _, ip := range ips {
			k := ip + " " + port
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ksAllows — исключения пользователя: по одному IPv4-адресу или сети (a.b.c.d/nn) в строке.
func ksAllows() []string {
	b, err := os.ReadFile(ksAllowFile())
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if i := strings.Index(l, "#"); i >= 0 {
			l = l[:i]
		}
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if ip := net.ParseIP(l); ip != nil && ip.To4() != nil {
			out = append(out, ip.String())
		} else if _, n, err := net.ParseCIDR(l); err == nil && n.IP.To4() != nil {
			out = append(out, n.String())
		}
	}
	return out
}

func ksEnsureAllowFile() {
	if _, err := os.Stat(ksAllowFile()); err == nil {
		return
	}
	_ = os.WriteFile(ksAllowFile(), []byte(`# Исключения строгого режима AWGProxy: адреса, куда можно ходить напрямую,
# мимо Армении (например, сервер удалённого доступа). По одному в строке:
#   203.0.113.5
#   198.51.100.0/24
# После правки выключите и снова включите строгий режим.
`), 0o600)
}

// ksWrite записывает файл состояния целиком (через временный файл), служба его подхватит.
func ksWrite(on bool) error {
	_ = os.MkdirAll(awgDir(), 0o700)
	var b strings.Builder
	b.WriteString("# Строгий режим AWGProxy. Файл пишет приложение AWGProxy Control.\n")
	if on {
		b.WriteString("mode on\n")
	} else {
		b.WriteString("mode off\n")
	}
	for _, e := range ksEndpoints() {
		b.WriteString("endpoint " + e + "\n")
	}
	for _, a := range ksAllows() {
		b.WriteString("allow " + a + "\n")
	}
	tmp := ksStateFile() + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, ksStateFile())
}

// ksApplyAndWait пишет состояние и ждёт, пока служба его применит.
func ksApplyAndWait(on bool) error {
	for attempt := 0; attempt < 3; attempt++ {
		if err := ksWrite(on); err != nil {
			return err
		}
		for i := 0; i < 16; i++ {
			time.Sleep(500 * time.Millisecond)
			s := ksStatus()
			if on && strings.HasPrefix(s, "on ") || !on && strings.HasPrefix(s, "off ") {
				return nil
			}
			if strings.HasPrefix(s, "error ") && on {
				return fmt.Errorf("%s", strings.TrimPrefix(s, "error "))
			}
		}
	}
	return fmt.Errorf("служба строгого режима не ответила (статус: %s)", orDash(ksStatus()))
}

// setKillSwitch включает или выключает строгий режим; при первом включении ставит службу
// (macOS спросит пароль администратора).
func setKillSwitch(on bool) error {
	if on && len(ksEndpoints()) == 0 {
		return fmt.Errorf("не найден адрес сервера в профилях")
	}
	if !ksInstalled() {
		if !on {
			return nil
		}
		ksEnsureAllowFile()
		if err := ksWrite(false); err != nil {
			return err
		}
		if err := ksInstall(); err != nil {
			return err
		}
	}
	return ksApplyAndWait(on)
}

// ksRefresh — после смены профилей обновить адреса серверов, не меняя режим.
func ksRefresh() {
	if ksInstalled() {
		_ = ksWrite(ksWanted())
	}
}

func ksInstall() error {
	script := ksInstallScript(ksStateFile())
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("awgproxy-ks-install-%d.sh", os.Getpid()))
	if err := os.WriteFile(tmp, []byte(script), 0o600); err != nil {
		return err
	}
	defer os.Remove(tmp)
	osa := `do shell script "/bin/bash " & quoted form of "` + tmp + `" with prompt "AWGProxy: установка строгого режима (блокировка интернета без Армении)" with administrator privileges`
	out, err := exec.Command("osascript", "-e", osa).CombinedOutput()
	if err != nil {
		o := strings.TrimSpace(string(out))
		if strings.Contains(o, "-128") {
			return fmt.Errorf("установка отменена")
		}
		return fmt.Errorf("установка: %v %s", err, o)
	}
	if !ksInstalled() {
		return fmt.Errorf("служба строгого режима не установилась")
	}
	return nil
}

func ksInstallScript(state string) string {
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>` + ksLabel + `</string>
  <key>ProgramArguments</key><array><string>/bin/bash</string><string>` + xmlEscape(ksDir+"/apply.sh") + `</string><string>` + xmlEscape(state) + `</string></array>
  <key>WatchPaths</key><array><string>` + xmlEscape(state) + `</string></array>
  <key>RunAtLoad</key><true/>
  <key>StandardErrorPath</key><string>/var/log/awgproxy-killswitch.log</string>
</dict></plist>
`
	return `set -e
D="` + ksDir + `"
mkdir -p "$D"
cat > "$D/apply.sh" <<'APPLY'
` + ksApplyScript + `APPLY
chown -R root:wheel "$D"; chmod 755 "$D" "$D/apply.sh"
cat > "` + ksDaemon + `" <<'PLIST'
` + plist + `PLIST
chown root:wheel "` + ksDaemon + `"; chmod 644 "` + ksDaemon + `"
launchctl bootout system/` + ksLabel + ` 2>/dev/null || true
launchctl bootstrap system "` + ksDaemon + `"
`
}

// ksApplyScript выполняется от root. Из файла пользователя берёт только строго проверенные
// значения (режим, IPv4-адреса, порты), всё остальное игнорирует.
const ksApplyScript = `#!/bin/bash
# Строгий режим AWGProxy: интернет только через туннель к серверу AWG. Ставит AWGProxy Control.
STATE="$1"; D="/Library/Application Support/AWGProxyKillSwitch"; ST="$D/status"
ANCHOR="com.apple/250.AWGProxyKillSwitch"
now(){ date -u '+%Y-%m-%d %H:%M:%S UTC'; }
status(){ echo "$1" > "$ST.tmp" && chmod 644 "$ST.tmp" && mv -f "$ST.tmp" "$ST"; }
ip4='^([0-9]{1,3}\.){3}[0-9]{1,3}$'
okip(){ [[ $1 =~ $ip4 ]] || return 1; local IFS=.; for o in $1; do [ "$o" -le 255 ] || return 1; done; }
mode=off; eps=(); allows=()
if [ -f "$STATE" ] && [ ! -L "$STATE" ]; then
  while read -r k a b _; do
    case "$k" in
      mode) [ "$a" = on ] && mode=on ;;
      endpoint) okip "$a" && [[ $b =~ ^[0-9]{1,5}$ ]] && [ "$b" -le 65535 ] && eps+=("$a $b") ;;
      allow) n="${a%/*}"; okip "$n" || continue
             if [[ $a == */* ]]; then m="${a#*/}"; [[ $m =~ ^[0-9]{1,2}$ ]] && [ "$m" -le 32 ] || continue; fi
             allows+=("$a") ;;
    esac
  done < "$STATE"
fi
if [ "$mode" != on ]; then
  pfctl -a "$ANCHOR" -F all >/dev/null 2>&1
  status "off $(now)"; exit 0
fi
if [ ${#eps[@]} -eq 0 ]; then
  pfctl -a "$ANCHOR" -F all >/dev/null 2>&1
  status "error $(now) нет адреса сервера — блокировка не включена"; exit 1
fi
R=$(mktemp /tmp/awgks.XXXXXX)
{
  echo 'pass quick on lo0 all'
  for e in "${eps[@]}"; do set -- $e; echo "pass out quick inet proto udp from any to $1 port $2 keep state"; done
  for a in "${allows[@]}"; do echo "pass out quick inet from any to $a keep state"; done
  echo 'pass out quick inet proto udp from any port 68 to any port 67 keep state'
  echo 'block return out quick proto { tcp udp } from any to any port 53'
  echo 'pass out quick inet from any to { 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 169.254.0.0/16 224.0.0.0/4 255.255.255.255 } keep state'
  echo 'pass quick inet6 proto ipv6-icmp all keep state'
  echo 'pass out quick inet6 from any to { fe80::/10 ff00::/8 } keep state'
  echo 'block return out quick all'
} > "$R"
# главный набор правил должен ссылаться на якоря com.apple/* (так в стандартном /etc/pf.conf)
pfctl -s Anchors 2>/dev/null | grep -q 'com.apple' || pfctl -f /etc/pf.conf 2>/dev/null
if ERR=$(pfctl -a "$ANCHOR" -f "$R" 2>&1); then
  pfctl -E >/dev/null 2>&1
  if pfctl -s info 2>/dev/null | grep -q 'Status: Enabled'; then
    status "on $(now) серверы: ${eps[*]}"
  else
    status "error $(now) брандмауэр pf не включился"
  fi
else
  status "error $(now) $(echo "$ERR" | grep -v ALTQ | head -2 | tr '\n' ' ')"
fi
rm -f "$R"
`

func killSwitchDiag() string {
	if !ksInstalled() {
		return "строгий режим: не установлен (выключен)\n"
	}
	w := map[bool]string{true: "включить", false: "выключить"}[ksWanted()]
	return fmt.Sprintf("строгий режим: просили %s; служба: %s; серверы: %s; исключения: %s\n",
		w, orDash(ksStatus()), strings.Join(ksEndpoints(), ", "), orDash(strings.Join(ksAllows(), ", ")))
}

// ---------- кто ходит мимо туннеля ----------

// bypassCheck перечисляет программы этого пользователя с открытыми соединениями в интернет
// напрямую — не через 127.0.0.1:1080/1081. Такие программы не слушают системный прокси.
func bypassCheck() string {
	eps := map[string]bool{}
	for _, e := range ksEndpoints() {
		eps[strings.Fields(e)[0]] = true
	}
	type hit struct{ proto, remote string }
	byApp := map[string][]hit{}
	viaProxy := map[string]bool{}
	for _, proto := range []string{"TCP", "UDP"} {
		args := []string{"+c", "0", "-nP", "-i" + proto}
		if proto == "TCP" {
			args = append(args, "-sTCP:ESTABLISHED")
		}
		out, _ := exec.Command("lsof", args...).Output()
		for _, l := range strings.Split(string(out), "\n")[1:] {
			f := strings.Fields(l)
			if len(f) < 9 {
				continue
			}
			name := strings.ReplaceAll(f[0], `\x20`, " ")
			conn := f[8]
			i := strings.Index(conn, "->")
			if i < 0 {
				continue
			}
			remote := conn[i+2:]
			host, port, err := net.SplitHostPort(remote)
			if err != nil {
				continue
			}
			ip := net.ParseIP(host)
			if ip == nil {
				continue
			}
			if ip.IsLoopback() {
				if port == "1080" || port == "1081" {
					viaProxy[name] = true
				}
				continue
			}
			if ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || eps[host] {
				continue
			}
			byApp[name] = append(byApp[name], hit{proto, remote})
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Проверка %s: программы с соединениями в интернет НАПРЯМУЮ (мимо Армении)\n", time.Now().Format("2006-01-02 15:04:05"))
	if killSwitchActive() {
		b.WriteString("Строгий режим включён: такие соединения брандмауэр не пропускает.\n")
	}
	names := make([]string, 0, len(byApp))
	for n := range byApp {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		b.WriteString("\nНапрямую сейчас никто не ходит.\n")
	}
	for _, n := range names {
		hs := byApp[n]
		ex := []string{}
		for i, h := range hs {
			if i == 4 {
				ex = append(ex, "…")
				break
			}
			ex = append(ex, h.proto+" "+h.remote)
		}
		fmt.Fprintf(&b, "\n✕ %s — %d соед.: %s", n, len(hs), strings.Join(ex, ", "))
		if viaProxy[n] {
			b.WriteString("  (часть соединений этой программы идёт через туннель)")
		}
	}
	var vp []string
	for n := range viaProxy {
		if _, bad := byApp[n]; !bad {
			vp = append(vp, n)
		}
	}
	sort.Strings(vp)
	b.WriteString("\n\nЧерез туннель (системный прокси) сейчас ходят: " + orDash(strings.Join(vp, ", ")) + "\n")
	b.WriteString("\nПримечание: видны только программы этого пользователя; системные службы macOS не показаны.\n" +
		"Программа, которой сейчас нет в списках, просто не держит соединений — откройте её и проверьте ещё раз.\n")
	return b.String()
}
