package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Тест скорости через серверы Cloudflare (speed.cloudflare.com): пинг, загрузка и отдача —
// сначала через туннель (HTTP-прокси AWGProxy 127.0.0.1:1081), потом напрямую.

const (
	speedDownURL = "https://speed.cloudflare.com/__down?bytes="
	speedUpURL   = "https://speed.cloudflare.com/__up"
	downBytes    = 50_000_000 // максимум на загрузку; тест обрывается через downLimit
	upBytes      = 10_000_000
	downLimit    = 10 * time.Second
	upLimit      = 15 * time.Second
)

var (
	speedBusy   atomic.Bool
	speedMu     sync.Mutex
	speedTunnel string // итог последнего теста через туннель
	speedDirect string // итог последнего теста напрямую
)

// speedClient — как client(), но с повторным использованием соединения, чтобы пинг
// и скорость не включали установку TLS каждый раз.
func speedClient(viaTunnel bool) *http.Client {
	tr := &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 8 * time.Second}).DialContext,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if viaTunnel {
		u, _ := url.Parse("http://127.0.0.1:1081")
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Transport: tr, Timeout: 30 * time.Second}
}

type speedResult struct {
	ping       time.Duration
	down, up   float64 // Мбит/с
	downErr    error
	upErr      error
	pingFailed bool
}

func (r speedResult) String() string {
	if r.pingFailed {
		return "нет связи"
	}
	s := fmt.Sprintf("пинг %d мс", r.ping.Milliseconds())
	if r.downErr == nil {
		s += fmt.Sprintf(" · ↓ %.1f", r.down)
	} else {
		s += " · ↓ ошибка"
	}
	if r.upErr == nil {
		s += fmt.Sprintf(" · ↑ %.1f Мбит/с", r.up)
	} else {
		s += " · ↑ ошибка"
	}
	return s
}

func measurePing(c *http.Client) (time.Duration, bool) {
	var times []time.Duration
	for i := 0; i < 6; i++ {
		t0 := time.Now()
		resp, err := c.Get(speedDownURL + "0")
		if err != nil {
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if i > 0 { // первый запрос устанавливает соединение — не считаем
			times = append(times, time.Since(t0))
		}
	}
	if len(times) == 0 {
		return 0, false
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return times[len(times)/2], true
}

func measureDown(c *http.Client) (float64, error) {
	resp, err := c.Get(fmt.Sprintf("%s%d", speedDownURL, downBytes))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	buf := make([]byte, 64*1024)
	var n int64
	t0 := time.Now()
	for time.Since(t0) < downLimit {
		k, err := resp.Body.Read(buf)
		n += int64(k)
		if err == io.EOF {
			break
		}
		if err != nil {
			if n == 0 {
				return 0, err
			}
			break
		}
	}
	sec := time.Since(t0).Seconds()
	if sec <= 0 || n == 0 {
		return 0, fmt.Errorf("ничего не загрузилось")
	}
	return float64(n) * 8 / sec / 1e6, nil
}

func measureUp(c *http.Client) (float64, error) {
	body := bytes.NewReader(make([]byte, upBytes))
	req, err := http.NewRequest("POST", speedUpURL, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	cc := *c
	cc.Timeout = upLimit + 10*time.Second
	t0 := time.Now()
	resp, err := cc.Do(req)
	if err != nil {
		return 0, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	sec := time.Since(t0).Seconds()
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return float64(upBytes) * 8 / sec / 1e6, nil
}

func runSpeed(viaTunnel bool, label string) speedResult {
	c := speedClient(viaTunnel)
	defer c.CloseIdleConnections()
	var r speedResult
	setInfo("Тест скорости (" + label + "): пинг…")
	p, ok := measurePing(c)
	if !ok {
		r.pingFailed = true
		return r
	}
	r.ping = p
	setInfo("Тест скорости (" + label + "): загрузка…")
	r.down, r.downErr = measureDown(c)
	setInfo("Тест скорости (" + label + "): отдача…")
	r.up, r.upErr = measureUp(c)
	return r
}

func speedTest() {
	if !speedBusy.CompareAndSwap(false, true) {
		return
	}
	defer func() {
		speedBusy.Store(false)
		updateMenu()
	}()
	updateMenu()
	if tunnelRunning() {
		t := runSpeed(true, "через Армению")
		speedMu.Lock()
		speedTunnel = t.String() + " (" + time.Now().Format("15:04") + ")"
		speedMu.Unlock()
		log.Println("скорость через туннель:", t.String())
		updateMenu()
	} else {
		speedMu.Lock()
		speedTunnel = "туннель выключен"
		speedMu.Unlock()
	}
	d := runSpeed(false, "напрямую")
	speedMu.Lock()
	speedDirect = d.String() + " (" + time.Now().Format("15:04") + ")"
	speedMu.Unlock()
	log.Println("скорость напрямую:", d.String())
	setInfo("Тест скорости готов")
}
