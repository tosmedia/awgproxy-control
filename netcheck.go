package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Проверки идут через локальный HTTP-прокси AWGProxy (127.0.0.1:1081) или напрямую,
// мимо системного прокси.
func client(viaTunnel bool, timeout time.Duration) *http.Client {
	tr := &http.Transport{
		Proxy:             nil, // напрямую, системный прокси не используется
		DisableKeepAlives: true,
		DialContext:       (&net.Dialer{Timeout: 6 * time.Second}).DialContext,
	}
	if viaTunnel {
		u, _ := url.Parse("http://127.0.0.1:1081")
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

func publicIP(viaTunnel bool) string {
	for _, u := range []string{"https://api.ipify.org", "https://ifconfig.me/ip"} {
		resp, err := client(viaTunnel, 8*time.Second).Get(u)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 100))
		resp.Body.Close()
		ip := strings.TrimSpace(string(b))
		if net.ParseIP(ip) != nil {
			return ip
		}
	}
	return ""
}

var (
	placeMu    sync.Mutex
	placeCache = map[string]string{}
)

// placeOf — город и провайдер по IP (ipinfo.io), с кэшем, чтобы не спрашивать часто.
func placeOf(ip string, viaTunnel bool) string {
	if ip == "" {
		return ""
	}
	placeMu.Lock()
	p, ok := placeCache[ip]
	placeMu.Unlock()
	if ok {
		return p
	}
	var info struct {
		City    string `json:"city"`
		Country string `json:"country"`
		Org     string `json:"org"`
	}
	for _, via := range []bool{viaTunnel, !viaTunnel} {
		resp, err := client(via, 8*time.Second).Get("https://ipinfo.io/" + ip + "/json")
		if err != nil {
			continue
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&info)
		resp.Body.Close()
		if err == nil && (info.City != "" || info.Org != "") {
			break
		}
	}
	org := info.Org
	if strings.HasPrefix(org, "AS") {
		if i := strings.Index(org, " "); i > 0 {
			org = org[i+1:]
		}
	}
	var parts []string
	for _, s := range []string{info.City, info.Country, org} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	p = strings.Join(parts, ", ")
	if p != "" {
		placeMu.Lock()
		placeCache[ip] = p
		placeMu.Unlock()
	}
	return p
}
