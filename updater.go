package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"time"
)

// Автообновление: новые версии берутся из GitHub Releases открытого репозитория
// tosmedia/awgproxy-control. Файл сверяется по SHA256SUMS из того же релиза.
const releasesAPI = "https://api.github.com/repos/tosmedia/awgproxy-control/releases/latest"

type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (r *release) version() string { return strings.TrimPrefix(r.Tag, "v") }

func (r *release) asset(name string) string {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.URL
		}
	}
	return ""
}

// newer: true, если версия a новее b (формат 1.2.3; "dev" — самая старая).
func newer(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, y := 0, 0
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// fetch скачивает сначала напрямую, затем через туннель.
func fetch(url string, limit int64, timeout time.Duration) ([]byte, error) {
	var lastErr error
	for _, via := range []bool{false, true} {
		resp, err := client(via, timeout).Get(url)
		if err != nil {
			lastErr = err
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != 200 {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		return b, nil
	}
	return nil, lastErr
}

func latestRelease() (*release, error) {
	b, err := fetch(releasesAPI, 1<<20, 15*time.Second)
	if err != nil {
		return nil, err
	}
	var r release
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	if r.Tag == "" {
		return nil, fmt.Errorf("в релизе нет версии")
	}
	return &r, nil
}

var availableVersion string // найденная новая версия (если автообновление выключено)

// checkUpdate: manual — пользователь нажал «Проверить обновления».
func checkUpdate(manual bool) {
	if manual {
		setInfo("Проверяю обновления…")
	}
	r, err := latestRelease()
	if err != nil {
		log.Println("обновление:", err)
		if manual {
			setInfo("Не удалось проверить обновления: " + err.Error())
		}
		return
	}
	if !newer(r.version(), version) {
		availableVersion = ""
		if manual {
			setInfo("Установлена последняя версия " + version)
		}
		updateMenu()
		return
	}
	availableVersion = r.version()
	updateMenu()
	if manual || autoUpdateOn() {
		installRelease(r)
	} else {
		setInfo("Есть новая версия " + r.version())
	}
}

func installRelease(r *release) {
	if !startBusy("Обновляю приложение…") {
		return
	}
	defer endBusy()
	name := updateAssetName()
	url := r.asset(name)
	sumsURL := r.asset("SHA256SUMS")
	if url == "" || sumsURL == "" {
		setInfo("В релизе " + r.Tag + " нет файла " + name)
		return
	}
	setInfo("Скачиваю версию " + r.version() + "…")
	data, err := fetch(url, 100<<20, 3*time.Minute)
	if err != nil {
		setInfo("Обновление не скачалось: " + err.Error())
		return
	}
	sums, err := fetch(sumsURL, 1<<16, 30*time.Second)
	if err != nil {
		setInfo("Не скачалась контрольная сумма: " + err.Error())
		return
	}
	sum := sha256.Sum256(data)
	want := ""
	for _, l := range strings.Split(string(sums), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			want = strings.ToLower(f[0])
		}
	}
	if want == "" || want != hex.EncodeToString(sum[:]) {
		setInfo("Обновление не прошло проверку подписи — не ставлю")
		return
	}
	relaunch, err := installUpdate(data)
	if err != nil {
		setInfo("Обновление не установилось: " + err.Error())
		return
	}
	log.Println("обновлено до", r.version(), "— перезапуск")
	setInfo("Обновлено до " + r.version() + ", перезапускаю…")
	if err := relaunch(); err != nil {
		setInfo("Обновлено до " + r.version() + " — перезапустите приложение вручную")
		return
	}
	time.Sleep(500 * time.Millisecond)
	quitApp()
}

func updateLoop() {
	time.Sleep(45 * time.Second)
	for {
		checkUpdate(false)
		time.Sleep(6 * time.Hour)
	}
}
