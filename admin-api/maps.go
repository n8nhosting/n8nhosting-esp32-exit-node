// maps.go - turn a Google Maps share link (https://maps.app.goo.gl/...) into
// the pin's coordinates, server side. Browsers cannot follow the short link's
// cross-origin redirect, so the dashboard posts it here. Only google hosts
// are ever contacted, over TLS, with a short timeout, and no response body is
// read: the coordinates live in the final URL.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	errBadURL     = errors.New("bad url")
	errNotAllowed = errors.New("host not allowed")
	errOffsite    = errors.New("redirect outside google maps")
	errNoCoords   = errors.New("no coordinates in url")
)

const mapsMaxHops = 5

var (
	reGoogleTLD = regexp.MustCompile(`^(?:www\.|maps\.)?google\.(?:com|[a-z]{2}|co\.[a-z]{2}|com\.[a-z]{2})$`)
	reAt        = regexp.MustCompile(`/@(-?\d{1,2}(?:\.\d+)?),(-?\d{1,3}(?:\.\d+)?)(?:,|/|$)`)
	rePin       = regexp.MustCompile(`!3d(-?\d{1,2}(?:\.\d+)?)!4d(-?\d{1,3}(?:\.\d+)?)`)
	rePath      = regexp.MustCompile(`/maps/(?:place|search)/(-?\d{1,2}(?:\.\d+)?),\+?(-?\d{1,3}(?:\.\d+)?)(?:/|$)`)
	rePair      = regexp.MustCompile(`^(?:loc:)?\s*(-?\d{1,2}(?:\.\d+)?)\s*,\s*\+?(-?\d{1,3}(?:\.\d+)?)\s*$`)
)

func isGoogleMapsHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	return h == "maps.app.goo.gl" || h == "goo.gl" || reGoogleTLD.MatchString(h)
}

func validLatLon(lat, lon float64) bool {
	return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 && !(lat == 0 && lon == 0)
}

func pair(a, b string) (float64, float64, bool) {
	lat, e1 := strconv.ParseFloat(a, 64)
	lon, e2 := strconv.ParseFloat(b, 64)
	if e1 != nil || e2 != nil || !validLatLon(lat, lon) {
		return 0, 0, false
	}
	return lat, lon, true
}

// parseMapsCoords extracts the pin from a Google Maps URL: the place pin
// (!3d!4d) over an explicit query (q=, query=, ll=, center=, destination=)
// over coordinates in the path (/place/lat,lon) over the viewport centre
// (/@lat,lon). src names the shape that matched.
func parseMapsCoords(u *url.URL) (lat, lon float64, src string, ok bool) {
	if u.Scheme == "geo" {
		if m := rePair.FindStringSubmatch(strings.SplitN(u.Opaque, "?", 2)[0]); m != nil {
			if lat, lon, ok = pair(m[1], m[2]); ok {
				return lat, lon, "geo", true
			}
		}
		return 0, 0, "", false
	}
	if m := rePin.FindStringSubmatch(u.Path); m != nil {
		if lat, lon, ok = pair(m[1], m[2]); ok {
			return lat, lon, "pin", true
		}
	}
	q := u.Query()
	for _, k := range []string{"q", "query", "ll", "center", "destination"} {
		if m := rePair.FindStringSubmatch(q.Get(k)); m != nil {
			if lat, lon, ok = pair(m[1], m[2]); ok {
				return lat, lon, "query", true
			}
		}
	}
	if m := rePath.FindStringSubmatch(u.Path); m != nil {
		if lat, lon, ok = pair(m[1], m[2]); ok {
			return lat, lon, "path", true
		}
	}
	if m := reAt.FindStringSubmatch(u.Path); m != nil {
		if lat, lon, ok = pair(m[1], m[2]); ok {
			return lat, lon, "at", true
		}
	}
	return 0, 0, "", false
}

// mapsResolver follows a share link to the URL that carries coordinates.
// allowURL gates the first request and every redirect hop.
type mapsResolver struct {
	client   *http.Client
	allowURL func(*url.URL) bool
	sem      chan struct{}
}

func (r *mapsResolver) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= mapsMaxHops {
		return errors.New("too many redirects")
	}
	if r.allowURL(req.URL) {
		return nil
	}
	if strings.EqualFold(req.URL.Hostname(), "consent.google.com") {
		return http.ErrUseLastResponse // keep the 3xx; resolve() reads its continue= parameter
	}
	return errOffsite
}

func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1]&0xc0 == 64 { // 100.64.0.0/10
		return false
	}
	return true
}

func newMapsResolver() *mapsResolver {
	r := &mapsResolver{sem: make(chan struct{}, 4),
		allowURL: func(u *url.URL) bool { return u.Scheme == "https" && isGoogleMapsHost(u.Hostname()) }}
	d := &net.Dialer{Timeout: 3 * time.Second}
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSHandshakeTimeout: 3 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if isPublicIP(ip.IP) {
					return d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
				}
			}
			return nil, errors.New("no public address for " + host)
		}}
	r.client = &http.Client{Transport: tr, Timeout: 6 * time.Second, CheckRedirect: r.checkRedirect}
	return r
}

// resolve returns the pin in raw, following redirects when raw itself has
// none. final is the URL the coordinates were read from.
func (r *mapsResolver) resolve(ctx context.Context, raw string) (lat, lon float64, src, final string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" {
		return 0, 0, "", "", errBadURL
	}
	if lat, lon, src, ok := parseMapsCoords(u); ok {
		return lat, lon, src, u.String(), nil // no network needed
	}
	if u.Scheme == "geo" {
		return 0, 0, "", "", errNoCoords // nothing to follow for a geo: URI
	}
	if !r.allowURL(u) { // production: https and a google maps host only
		return 0, 0, "", "", errNotAllowed
	}
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return 0, 0, "", "", ctx.Err()
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("User-Agent", "esp32-exit-node-admin/1.0 (+maps link resolver)")
	resp, err := r.client.Do(req)
	if err != nil {
		if errors.Is(err, errOffsite) {
			return 0, 0, "", "", errOffsite
		}
		return 0, 0, "", "", err
	}
	resp.Body.Close() // headers are all we need; the body is never read
	fu := resp.Request.URL
	if resp.StatusCode/100 == 3 { // stopped on the consent interstitial
		loc, err := fu.Parse(resp.Header.Get("Location"))
		if err != nil {
			return 0, 0, "", "", errOffsite
		}
		cont, err := url.Parse(loc.Query().Get("continue"))
		if err != nil || !r.allowURL(cont) {
			return 0, 0, "", "", errOffsite
		}
		fu = cont
	}
	if lat, lon, src, ok := parseMapsCoords(fu); ok {
		return lat, lon, src, fu.String(), nil
	}
	return 0, 0, "", fu.String(), errNoCoords
}

// mapsLink is where the dashboard sends the operator for a stored position.
func mapsLink(lat, lon float64) string {
	return "https://www.google.com/maps?q=" + strconv.FormatFloat(lat, 'f', 6, 64) + "," + strconv.FormatFloat(lon, 'f', 6, 64)
}

// resolveMapsURL is the shared front of the endpoint and of PUT /api/devices:
// it classifies a resolver error into the HTTP status the dashboard shows.
func (s *server) resolveMapsURL(ctx context.Context, raw string) (lat, lon float64, status int, errMsg string) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 2048 {
		return 0, 0, http.StatusBadRequest, "url too long"
	}
	if strings.HasPrefix(raw, "http://") { // a pasted http google link is followed over TLS only
		if pu, err := url.Parse(raw); err == nil && isGoogleMapsHost(pu.Hostname()) {
			raw = "https://" + raw[len("http://"):]
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	lat, lon, _, _, err := s.maps.resolve(ctx, raw)
	switch {
	case err == nil:
		return lat, lon, http.StatusOK, ""
	case errors.Is(err, errNoCoords):
		return 0, 0, http.StatusUnprocessableEntity, "the link has no pin; open it in Google Maps, drop a pin and share that"
	case errors.Is(err, errBadURL), errors.Is(err, errNotAllowed), errors.Is(err, errOffsite):
		return 0, 0, http.StatusBadRequest, err.Error()
	default:
		log.Printf("geo resolve: %v", err) // never the URL: the link may embed a place name
		return 0, 0, http.StatusBadGateway, "could not reach google maps"
	}
}

// POST /api/geo/resolve {"url": "<google maps share link>"} -> {lat, lon, maps_url}
func (s *server) handleGeoResolve(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL string `json:"url"`
	}
	if r.Method != http.MethodPost || json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	lat, lon, status, msg := s.resolveMapsURL(r.Context(), in.URL)
	if status != http.StatusOK {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lat": lat, "lon": lon, "maps_url": mapsLink(lat, lon)})
}
