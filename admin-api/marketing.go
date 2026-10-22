package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var portalLocales = []string{"th", "en", "zh-CN", "ja", "ru", "vi"}
var portalPublicPages = []string{"home", "plans", "help"}

// Tracking configuration accepts IDs, never executable HTML or JavaScript.
type MarketingAnalytics struct {
	GA4ID          string `json:"ga4_id"`
	GoogleAdsID    string `json:"google_ads_id"`
	GoogleAdsLabel string `json:"google_ads_label"`
	MetaPixelID    string `json:"meta_pixel_id"`
	TikTokPixelID  string `json:"tiktok_pixel_id"`
	GTMID          string `json:"gtm_id"`
}
type MarketingSEO struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Keywords    string `json:"keywords"`
}
type MarketingSettings struct {
	Enabled   bool                               `json:"enabled"`
	Analytics MarketingAnalytics                 `json:"analytics"`
	SEO       map[string]map[string]MarketingSEO `json:"seo"`
	UpdatedAt string                             `json:"updated_at"`
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func validateMarketing(m *MarketingSettings) error {
	ids := []struct {
		name    string
		value   *string
		pattern string
	}{
		{"ga4_id", &m.Analytics.GA4ID, `^G-[A-Z0-9]{4,20}$`},
		{"google_ads_id", &m.Analytics.GoogleAdsID, `^AW-[0-9]{5,20}$`},
		{"google_ads_label", &m.Analytics.GoogleAdsLabel, `^[A-Za-z0-9_-]{1,100}$`},
		{"meta_pixel_id", &m.Analytics.MetaPixelID, `^[0-9]{5,30}$`},
		{"tiktok_pixel_id", &m.Analytics.TikTokPixelID, `^[A-Za-z0-9]{6,40}$`},
		{"gtm_id", &m.Analytics.GTMID, `^GTM-[A-Z0-9]{4,20}$`},
	}
	for _, id := range ids {
		*id.value = strings.TrimSpace(*id.value)
		if *id.value != "" && !regexp.MustCompile(id.pattern).MatchString(*id.value) {
			return fmt.Errorf("invalid %s", id.name)
		}
	}
	if m.Analytics.GoogleAdsLabel != "" && m.Analytics.GoogleAdsID == "" {
		return errors.New("google_ads_label requires google_ads_id")
	}
	if m.Analytics.GTMID != "" && (m.Analytics.GA4ID != "" || m.Analytics.GoogleAdsID != "" || m.Analytics.MetaPixelID != "" || m.Analytics.TikTokPixelID != "") {
		return errors.New("use GTM or direct integrations to avoid duplicate tracking")
	}
	if m.Enabled && m.Analytics.GA4ID == "" && m.Analytics.GoogleAdsID == "" && m.Analytics.MetaPixelID == "" && m.Analytics.TikTokPixelID == "" && m.Analytics.GTMID == "" {
		return errors.New("enable tracking requires at least one integration ID")
	}
	for locale, pages := range m.SEO {
		if !containsString(portalLocales, locale) {
			return errors.New("unsupported SEO locale")
		}
		for page, seo := range pages {
			if !containsString(portalPublicPages, page) {
				return errors.New("unsupported SEO page")
			}
			for _, field := range []struct {
				value string
				max   int
			}{{seo.Title, 120}, {seo.Description, 320}, {seo.Keywords, 600}} {
				if !utf8.ValidString(field.value) || utf8.RuneCountInString(field.value) > field.max || strings.IndexFunc(field.value, func(r rune) bool { return unicode.IsControl(r) }) >= 0 {
					return errors.New("SEO field is too long or contains control characters")
				}
			}
			seo.Title = strings.TrimSpace(seo.Title)
			seo.Description = strings.TrimSpace(seo.Description)
			seo.Keywords = strings.TrimSpace(seo.Keywords)
			pages[page] = seo
		}
	}
	if m.SEO == nil {
		m.SEO = map[string]map[string]MarketingSEO{}
	}
	return nil
}
func (s *server) marketingSettings(ctx context.Context) (MarketingSettings, error) {
	m := MarketingSettings{SEO: map[string]map[string]MarketingSEO{}}
	var raw string
	if err := s.store.DB().QueryRowContext(ctx, `SELECT config,updated_at FROM marketing_settings WHERE id=1`).Scan(&raw, &m.UpdatedAt); err != nil {
		return m, err
	}
	updated := m.UpdatedAt
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return m, err
	}
	m.UpdatedAt = updated
	if err := validateMarketing(&m); err != nil {
		return m, err
	}
	return m, nil
}
func registerMarketingRoutes(mux *http.ServeMux, s *server) {
	mux.HandleFunc("/api/marketing", s.requireAdmin(s.handleMarketing))
	mux.HandleFunc("GET /portal/api/marketing", s.handlePublicMarketing)
}
func (s *server) handleMarketing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		m, err := s.marketingSettings(r.Context())
		if err != nil {
			portalError(w, 503, "marketing settings unavailable")
			return
		}
		writeJSON(w, 200, m)
	case http.MethodPut:
		var m MarketingSettings
		r.Body = http.MaxBytesReader(w, r.Body, 65536)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&m); err != nil {
			portalError(w, 400, "invalid marketing settings")
			return
		}
		var extra any
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			portalError(w, 400, "invalid marketing settings")
			return
		}
		if err := validateMarketing(&m); err != nil {
			portalError(w, 400, err.Error())
			return
		}
		previous := m.UpdatedAt
		m.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		raw, _ := json.Marshal(m)
		result, err := s.store.DB().ExecContext(r.Context(), `UPDATE marketing_settings SET config=?,updated_at=? WHERE id=1 AND updated_at=?`, string(raw), m.UpdatedAt, previous)
		if err != nil {
			portalError(w, 503, "marketing settings unavailable")
			return
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			portalError(w, 409, "marketing settings changed; reload before saving")
			return
		}
		// No request body, identifiers or SEO text is copied into the event log.
		s.emit("marketing.updated", "info", "system", "marketing", "admin", "Portal marketing settings updated", map[string]bool{"enabled": m.Enabled})
		writeJSON(w, 200, m)
	default:
		w.Header().Set("Allow", "GET, PUT")
		portalError(w, 405, "method not allowed")
	}
}
func (s *server) handlePublicMarketing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	m, err := s.marketingSettings(r.Context())
	if err != nil {
		portalError(w, 503, "marketing settings unavailable")
		return
	}
	raw, _ := json.Marshal(m)
	sum := sha256.Sum256(raw)
	if !m.Enabled {
		m.Analytics = MarketingAnalytics{}
	}
	writeJSON(w, 200, map[string]any{"enabled": m.Enabled, "analytics": m.Analytics, "consent_required": true, "config_version": hex.EncodeToString(sum[:8])})
}
