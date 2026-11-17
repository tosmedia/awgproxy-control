package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"awgproxy-control/tray"
)

// Журнал связи: каждые 5 секунд приложение проверяет, проходит ли запрос через туннель
// и есть ли интернет напрямую. Обрывы записываются в conn.log с точным временем
// (UTC и местным) — их можно сравнить с журналом сервера и понять, где рвётся связь.

const (
	probeURL      = "http://cp.cloudflare.com/generate_204" // крошечный ответ 204
	probeEvery    = 5 * time.Second
	probeTimeout  = 4 * time.Second
	failsToOutage = 2 // столько неудач подряд (≈10 с) — обрыв
)

type outage struct {
	start, end time.Time
	directOK   bool // был ли в это время интернет напрямую
	tunnelUp   bool // был ли запущен процесс туннеля
}

var (
	monMu       sync.Mutex
	outages     []outage // за последние 24 ч
	curOutage   *outage
	failStreak  int
	lastLatency time.Duration
	checks, oks int
	mStab       *tray.MenuItem
	mConnLog    *tray.MenuItem
)

func connLogPath() string { return filepath.Join(configDir(), "conn.log") }

func connLog(format string, a ...any) {
	now := time.Now()
	line := fmt.Sprintf("%s UTC (%s местн.) ", now.UTC().Format("2006-01-02 15:04:05"), now.Format("15:04:05")) +
		fmt.Sprintf(format, a...) + "\n"
	f, err := os.OpenFile(connLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	if fi, _ := f.Stat(); fi != nil && fi.Size() > 2<<20 {
		f.Truncate(0)
	}
	f.WriteString(line)
	f.Close()
}

// probe — один короткий запрос: через туннель (HTTP-прокси 1081) или напрямую.
func probe(viaTunnel bool) (time.Duration, bool) {
	c := client(viaTunnel, probeTimeout)
	t0 := time.Now()
	resp, err := c.Get(probeURL)
	if err != nil {
		return 0, false
	}
	resp.Body.Close()
	return time.Since(t0), resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK
}

func fmtDur(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%d с", int(d.Seconds()))
	}
	return fmt.Sprintf("%d мин %d с", int(d.Minutes()), int(d.Seconds())%60)
}

func monitorLoop() {
	connLog("приложение запущено, версия %s; проверка связи каждые %d с", version, int(probeEvery.Seconds()))
	lastBeat := time.Now()
	lastPID := tunnelPID()
	lastProxy := getStatus().ProxyOn
	for {
		time.Sleep(probeEvery)
		// перезапуски процесса туннеля и переключения прокси — кем бы они ни были сделаны
		if pid := tunnelPID(); pid != lastPID {
			switch {
			case pid == 0:
				connLog("процесс туннеля ОСТАНОВЛЕН (был %d)", lastPID)
			case lastPID == 0:
				connLog("процесс туннеля запущен (номер %d)", pid)
			default:
				connLog("процесс туннеля ПЕРЕЗАПУЩЕН (%d → %d)", lastPID, pid)
			}
			lastPID = pid
		}
		if p, _ := proxyState(); p != lastProxy {
			connLog("системный прокси %s", map[bool]string{true: "ВКЛЮЧЁН", false: "ВЫКЛЮЧЕН"}[p])
			lastProxy = p
		}
		if busy.Load() { // включение/переключение — не считаем
			continue
		}
		if lastPID == 0 || !lastProxy {
			// туннель остановлен или прокси выключен — VPN не используется; если шёл обрыв, закрываем его
			monMu.Lock()
			if curOutage != nil {
				curOutage.end = time.Now()
				outages = append(outages, *curOutage)
				connLog("туннель остановлен или прокси выключен во время обрыва (обрыв длился %s)", fmtDur(curOutage.end.Sub(curOutage.start)))
				curOutage = nil
			}
			failStreak = 0
			monMu.Unlock()
			continue
		}
		lat, ok := probe(true)
		monMu.Lock()
		checks++
		if ok {
			oks++
			lastLatency = lat
			if curOutage != nil {
				curOutage.end = time.Now()
				outages = append(outages, *curOutage)
				connLog("ВОССТАНОВЛЕНО: через туннель снова работает (обрыв длился %s, задержка %d мс)",
					fmtDur(curOutage.end.Sub(curOutage.start)), lat.Milliseconds())
				curOutage = nil
				monMu.Unlock()
				refresh()
				monMu.Lock()
			}
			failStreak = 0
		} else {
			failStreak++
			if failStreak == failsToOutage && curOutage == nil {
				monMu.Unlock()
				_, direct := probe(false)
				running := tunnelRunning()
				monMu.Lock()
				curOutage = &outage{start: time.Now().Add(-probeEvery * (failsToOutage - 1)), directOK: direct, tunnelUp: running}
				connLog("ОБРЫВ: через туннель нет ответа уже %s; интернет напрямую: %s; процесс туннеля: %s",
					fmtDur(probeEvery*failsToOutage),
					map[bool]string{true: "ЕСТЬ (значит, не отвечает M6 или путь до него)", false: "НЕТ (пропал интернет у этого компьютера)"}[direct],
					map[bool]string{true: "запущен", false: "ОСТАНОВЛЕН"}[running])
				monMu.Unlock()
				refresh()
				monMu.Lock()
			}
		}
		// раз в час — короткая сводка, чтобы было видно, что журнал жив
		if time.Since(lastBeat) >= time.Hour {
			lastBeat = time.Now()
			connLog("сводка за час: проверок %d, успешных %d, задержка сейчас %d мс", checks, oks, lastLatency.Milliseconds())
			checks, oks = 0, 0
		}
		// храним обрывы только за сутки
		cut := time.Now().Add(-24 * time.Hour)
		k := 0
		for _, o := range outages {
			if o.end.After(cut) {
				outages[k] = o
				k++
			}
		}
		outages = outages[:k]
		monMu.Unlock()
		updateMenu()
	}
}

func stabilitySummary() string {
	monMu.Lock()
	defer monMu.Unlock()
	var total time.Duration
	for _, o := range outages {
		total += o.end.Sub(o.start)
	}
	n := len(outages)
	if curOutage != nil {
		return fmt.Sprintf("⚠ Обрыв идёт %s · за сутки обрывов: %d", fmtDur(time.Since(curOutage.start)), n+1)
	}
	if n == 0 {
		if lastLatency > 0 {
			return fmt.Sprintf("Связь за сутки: без обрывов · задержка %d мс", lastLatency.Milliseconds())
		}
		return "Связь за сутки: без обрывов"
	}
	last := outages[n-1]
	return fmt.Sprintf("Связь за сутки: обрывов %d, всего %s · последний в %s (%s)",
		n, fmtDur(total), last.start.Format("15:04"), fmtDur(last.end.Sub(last.start)))
}

func addMonitorMenu() {
	mStab = tray.AddMenuItem("Связь за сутки: проверяю…", "")
	mStab.Disable()
	mConnLog = tray.AddMenuItem("Открыть журнал связи", "Все обрывы с точным временем")
	go func() {
		for range mConnLog.ClickedCh {
			if _, err := os.Stat(connLogPath()); err != nil {
				connLog("журнал создан")
			}
			showFile(connLogPath())
		}
	}()
}

func updateMonitorMenu() {
	if mStab != nil {
		mStab.SetTitle(stabilitySummary())
	}
}

func monitorReport() string {
	var b strings.Builder
	b.WriteString("\n--- связь ---\n")
	b.WriteString(stabilitySummary() + "\n")
	b.WriteString("журнал связи (последние строки):\n")
	b.WriteString(tailFile(connLogPath(), 30))
	return b.String()
}
