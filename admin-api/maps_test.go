package main

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestParseMapsCoords(t *testing.T) {
	cases := []struct {
		name, in string
		lat, lon float64
		src      string
		ok       bool
	}{
		{"viewport @", "https://www.google.com/maps/@16.7923,100.2617,17z", 16.7923, 100.2617, "at", true},
		{"street view @", "https://www.google.com/maps/@16.7923,100.2617,3a,75y,90t/data=!3m6!1e1", 16.7923, 100.2617, "at", true},
		{"place pin beats viewport", "https://www.google.com/maps/place/Wat+X/@16.79,100.26,17z/data=!3m1!4b1!4m6!3m5!1s0x1:0x2!8m2!3d16.7923!4d100.2617!16s%2Fg%2F1", 16.7923, 100.2617, "pin", true},
		{"dropped pin DMS name with pin", "https://www.google.com/maps/place/16%C2%B047'32.3%22N+100%C2%B015'42.1%22E/@16.7923,100.2617,17z/data=!4m4!3m3!8m2!3d16.7923!4d100.2617", 16.7923, 100.2617, "pin", true},
		{"q=", "https://maps.google.com/?q=16.7923,100.2617", 16.7923, 100.2617, "query", true},
		{"q=loc:", "https://maps.google.com/?q=loc:16.7923,100.2617&z=15", 16.7923, 100.2617, "query", true},
		{"maps urls api query=", "https://www.google.com/maps/search/?api=1&query=16.7923%2C100.2617", 16.7923, 100.2617, "query", true},
		{"ll=", "https://maps.google.com/maps?ll=16.7923,100.2617&z=16&t=m", 16.7923, 100.2617, "query", true},
		{"search path with plus", "https://www.google.com/maps/search/16.7923,+100.2617?entry=tts", 16.7923, 100.2617, "path", true},
		{"place path", "https://www.google.com/maps/place/16.7923,100.2617", 16.7923, 100.2617, "path", true},
		{"southern/western hemisphere", "https://www.google.com/maps/@-33.8688,-70.6693,12z", -33.8688, -70.6693, "at", true},
		{"geo uri", "geo:16.7923,100.2617?z=17", 16.7923, 100.2617, "geo", true},
		{"co.th domain", "https://www.google.co.th/maps/place/Bangkok/@13.7563,100.5018,15z", 13.7563, 100.5018, "at", true},
		{"short link has nothing to parse", "https://maps.app.goo.gl/Wi3Rzy4X2tRmbQW77", 0, 0, "", false},
		{"out of range", "https://www.google.com/maps/@95.0,200.0,12z", 0, 0, "", false},
		{"null island", "https://maps.google.com/?q=0,0", 0, 0, "", false},
		{"directions without pin", "https://www.google.com/maps/dir/Bangkok/Chiang+Mai", 0, 0, "", false},
		{"plus code", "https://www.google.com/maps/place/7P52QRP6%2B3J", 0, 0, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, err := url.Parse(c.in)
			if err != nil {
				t.Fatal(err)
			}
			lat, lon, src, ok := parseMapsCoords(u)
			if ok != c.ok || src != c.src || math.Abs(lat-c.lat) > 1e-6 || math.Abs(lon-c.lon) > 1e-6 {
				t.Fatalf("got (%v,%v,%q,%v) want (%v,%v,%q,%v)", lat, lon, src, ok, c.lat, c.lon, c.src, c.ok)
			}
		})
	}
}

func TestIsGoogleMapsHost(t *testing.T) {
	for h, want := range map[string]bool{
		"maps.app.goo.gl": true, "goo.gl": true, "www.google.com": true, "google.com": true,
		"maps.google.com": true, "www.google.co.th": true, "google.co.uk": true, "google.com.au": true,
		"GOOGLE.COM.": true,
		"evil.com": false, "google.com.evil.com": false, "notgoogle.com": false, "consent.google.com": false,
		"www.google.evil": false, "maps.app.goo.gl.evil.com": false, "": false,
	} {
		if got := isGoogleMapsHost(h); got != want {
			t.Errorf("%q: got %v want %v", h, got, want)
		}
	}
}

// fakeMaps stands in for maps.app.goo.gl + google.com: short links redirect
// to a page whose URL carries the pin; the page itself never sends a body, so
// a resolver that reads bodies hangs and fails.
func fakeMaps(t *testing.T) (srv *httptest.Server, resolver *mapsResolver) {
	t.Helper()
	release := make(chan struct{})
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Wi3Rzy4X2tRmbQW77":
			http.Redirect(w, r, "/maps/place/Pin/@16.79,100.26,17z/data=!4m5!3m4!8m2!3d16.7923!4d100.2617", http.StatusFound)
		case "/hop":
			http.Redirect(w, r, "/Wi3Rzy4X2tRmbQW77", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/offsite":
			http.Redirect(w, r, "https://evil.example/maps/@1,2,3z", http.StatusFound)
		case "/consent":
			http.Redirect(w, r, "https://consent.google.com/m?continue="+url.QueryEscape("http://"+r.Host+"/maps/@16.7923,100.2617,17z"), http.StatusFound)
		case "/nopin":
			http.Redirect(w, r, "/maps/place/Somewhere+Nice", http.StatusFound)
		default:
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-release
		}
	}))
	// LIFO: release the blocked handlers first, then the server can close.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	host := srv.Listener.Addr().String()
	resolver = &mapsResolver{sem: make(chan struct{}, 4), allowURL: func(u *url.URL) bool { return u.Host == host }}
	resolver.client = &http.Client{Timeout: 2 * time.Second, CheckRedirect: resolver.checkRedirect,
		Transport: &http.Transport{DisableKeepAlives: true}}
	return srv, resolver
}

func TestResolveMapsFollowsRedirects(t *testing.T) {
	srv, r := fakeMaps(t)
	ctx := context.Background()
	cases := []struct {
		name, path string
		lat, lon   float64
		err        error
	}{
		{"short link", "/Wi3Rzy4X2tRmbQW77", 16.7923, 100.2617, nil},
		{"two hops", "/hop", 16.7923, 100.2617, nil},
		{"consent interstitial", "/consent", 16.7923, 100.2617, nil},
		{"page without pin", "/nopin", 0, 0, errNoCoords},
		{"offsite redirect", "/offsite", 0, 0, errOffsite},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lat, lon, _, _, err := r.resolve(ctx, srv.URL+c.path)
			if !errors.Is(err, c.err) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			if err == nil && (math.Abs(lat-c.lat) > 1e-6 || math.Abs(lon-c.lon) > 1e-6) {
				t.Fatalf("got %v,%v want %v,%v", lat, lon, c.lat, c.lon)
			}
		})
	}
	t.Run("redirect loop stops at mapsMaxHops", func(t *testing.T) {
		if _, _, _, _, err := r.resolve(ctx, srv.URL+"/loop"); err == nil || !strings.Contains(err.Error(), "too many redirects") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("host outside the allowlist is refused before any request", func(t *testing.T) {
		if _, _, _, _, err := r.resolve(ctx, "https://evil.example/maps/x"); !errors.Is(err, errNotAllowed) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a pasted url that already carries the pin needs no network", func(t *testing.T) {
		lat, lon, src, _, err := r.resolve(ctx, "https://www.google.com/maps/@13.7563,100.5018,15z")
		if err != nil || src != "at" || math.Abs(lat-13.7563) > 1e-6 || math.Abs(lon-100.5018) > 1e-6 {
			t.Fatalf("got %v,%v,%q,%v", lat, lon, src, err)
		}
	})
}

// POST /api/geo/resolve turns a share link into coordinates for the editor.
func TestGeoResolveEndpoint(t *testing.T) {
	s, mux := newTestServer(t)
	srv, r := fakeMaps(t)
	s.maps = r
	rr, d := doJSON(t, mux, "POST", "/api/geo/resolve", "admintok", map[string]any{"url": srv.URL + "/Wi3Rzy4X2tRmbQW77"})
	if rr.Code != 200 || math.Abs(d["lat"].(float64)-16.7923) > 1e-6 || math.Abs(d["lon"].(float64)-100.2617) > 1e-6 {
		t.Fatalf("%d %v", rr.Code, d)
	}
	if d["maps_url"] != "https://www.google.com/maps?q=16.792300,100.261700" {
		t.Fatalf("maps_url = %v", d["maps_url"])
	}
	if rr, _ := doJSON(t, mux, "POST", "/api/geo/resolve", "admintok", map[string]any{"url": srv.URL + "/nopin"}); rr.Code != 422 {
		t.Fatalf("no pin: %d", rr.Code)
	}
	if rr, _ := doJSON(t, mux, "POST", "/api/geo/resolve", "admintok", map[string]any{"url": "https://evil.example/x"}); rr.Code != 400 {
		t.Fatalf("offsite host: %d", rr.Code)
	}
	if rr, _ := doJSON(t, mux, "POST", "/api/geo/resolve", "", map[string]any{"url": srv.URL + "/Wi3Rzy4X2tRmbQW77"}); rr.Code != 401 {
		t.Fatalf("no token: %d", rr.Code)
	}
}

// PUT /api/devices/{mac} with maps_url resolves it into lat/lon, keeps the
// original link, and a bad link saves nothing.
func TestDevicePutMapsURL(t *testing.T) {
	s, mux, fb := newTestServerWithBroker(t)
	srv, r := fakeMaps(t)
	s.maps = r
	fb.nodes = []brokerNode{{MAC: "aa:bb:cc:dd:ee:07", RemoteIP: "27.1.1.7", Online: true}}
	link := srv.URL + "/Wi3Rzy4X2tRmbQW77"
	rr, d := doJSON(t, mux, "PUT", "/api/devices/aa:bb:cc:dd:ee:07", "admintok", map[string]any{"name": "Home G", "maps_url": link})
	if rr.Code != 200 || math.Abs(d["lat"].(float64)-16.7923) > 1e-6 || math.Abs(d["lon"].(float64)-100.2617) > 1e-6 || d["maps_url"] != link {
		t.Fatalf("%d %v", rr.Code, d)
	}
	_, list := doJSONList(t, mux, "GET", "/api/devices", "admintok")
	if list[0]["maps_url"] != link || list[0]["lat"] == nil {
		t.Fatalf("list: %v", list[0])
	}
	// a link without a pin is an error and changes nothing
	rr, _ = doJSON(t, mux, "PUT", "/api/devices/aa:bb:cc:dd:ee:07", "admintok", map[string]any{"maps_url": srv.URL + "/nopin"})
	if rr.Code != 422 {
		t.Fatalf("no pin: %d", rr.Code)
	}
	_, list = doJSONList(t, mux, "GET", "/api/devices", "admintok")
	if list[0]["maps_url"] != link || list[0]["name"] != "Home G" {
		t.Fatalf("bad link must not change the record: %v", list[0])
	}
	// an empty maps_url clears the stored link but keeps the coordinates
	rr, d = doJSON(t, mux, "PUT", "/api/devices/aa:bb:cc:dd:ee:07", "admintok", map[string]any{"maps_url": ""})
	if rr.Code != 200 || d["maps_url"] != "" || d["lat"] == nil {
		t.Fatalf("clear: %d %v", rr.Code, d)
	}
}
