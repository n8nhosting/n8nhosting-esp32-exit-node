package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// geo enriches an exit IP with city-level location and ISP/org, using a free
// geo-IP service (ip-api.com). Results are cached in memory; failures degrade
// gracefully (empty geo). No precise GPS — ESP32 has no GPS; this is IP-level.

type geoInfo struct {
	Country string  `json:"country"`
	City    string  `json:"city"`
	Region  string  `json:"region"`
	ISP     string  `json:"isp"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
}

type geoCache struct {
	mu   sync.Mutex
	data map[string]geoEntry
	url  string // override for tests
}

type geoEntry struct {
	info geoInfo
	at   time.Time
}

func newGeoCache() *geoCache {
	return &geoCache{data: map[string]geoEntry{}, url: envOr("GEOIP_URL", "http://ip-api.com/json/")}
}

const geoTTL = 6 * time.Hour

func (g *geoCache) lookup(ip string) geoInfo {
	if ip == "" {
		return geoInfo{}
	}
	g.mu.Lock()
	if e, ok := g.data[ip]; ok && time.Since(e.at) < geoTTL {
		g.mu.Unlock()
		return e.info
	}
	g.mu.Unlock()

	cl := &http.Client{Timeout: 4 * time.Second}
	resp, err := cl.Get(g.url + ip + "?fields=status,country,regionName,city,isp,lat,lon")
	if err != nil {
		return geoInfo{}
	}
	defer resp.Body.Close()
	var raw struct {
		Status     string  `json:"status"`
		Country    string  `json:"country"`
		RegionName string  `json:"regionName"`
		City       string  `json:"city"`
		ISP        string  `json:"isp"`
		Lat        float64 `json:"lat"`
		Lon        float64 `json:"lon"`
	}
	if json.NewDecoder(resp.Body).Decode(&raw) != nil || raw.Status != "success" {
		return geoInfo{}
	}
	info := geoInfo{Country: raw.Country, Region: raw.RegionName, City: raw.City, ISP: raw.ISP, Lat: raw.Lat, Lon: raw.Lon}
	g.mu.Lock()
	g.data[ip] = geoEntry{info: info, at: time.Now()}
	g.mu.Unlock()
	return info
}
