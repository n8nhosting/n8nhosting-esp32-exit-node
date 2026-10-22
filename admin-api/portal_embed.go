package main

import (
	"embed"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"regexp"
	"strings"
)

//go:embed portal
var portalFS embed.FS

var htmlTitleRE = regexp.MustCompile(`(?s)<title>.*?</title>`)
var htmlLangRE = regexp.MustCompile(`<html\s+lang="[^"]*"`)
var portalAssetRE = regexp.MustCompile(`^(portal\.(js|css)|i18n\.js|analytics\.js|locales/(th|en|zh-CN|ja|ru|vi)\.json|og/portal-(th|en|zh-CN|ja|ru|vi)\.png)$`)

// Do not derive public URLs from attacker-controlled Host/Forwarded headers.
func portalPublicURL(locale, page string) string {
	u, ok := portalBase()
	if !ok {
		return ""
	}
	u.Path = "/portal/" + locale + "/"
	if page != "home" {
		u.Path += page
	}
	return u.String()
}
func portalRoute(path string) (locale, page string, public, valid bool) {
	name := strings.TrimPrefix(path, "/portal/")
	if name == "" || name == "index.html" {
		return "th", "home", true, true
	}
	parts := strings.Split(strings.TrimSuffix(name, "/"), "/")
	if !containsString(portalLocales, parts[0]) {
		return "", "", false, false
	}
	locale = parts[0]
	page = "home"
	if len(parts) == 2 {
		page = parts[1]
	} else if len(parts) > 2 {
		return "", "", false, false
	}
	if containsString(portalPublicPages, page) {
		return locale, page, true, true
	}
	if containsString([]string{"account", "subscriptions", "orders", "login", "verify", "checkout"}, page) {
		return locale, page, false, true
	}
	return "", "", false, false
}
func portalDictionary(locale string) map[string]string {
	var out map[string]string
	if raw, err := portalFS.ReadFile("portal/locales/" + locale + ".json"); err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	if out == nil {
		out = map[string]string{}
	}
	return out
}
func portalSEO(locale, page string, m MarketingSettings) (MarketingSEO, map[string]string) {
	d := portalDictionary(locale)
	if !containsString(portalPublicPages, page) {
		key := "ui.sign.in"
		switch page {
		case "account", "subscriptions":
			key = "ui.my.plans"
		case "orders":
			key = "ui.orders"
		}
		return MarketingSEO{Title: d[key] + " | Proxy Server", Description: d["seo.home.description"]}, d
	}
	prefix := "seo." + page + "."
	seo := MarketingSEO{Title: d[prefix+"title"], Description: d[prefix+"description"], Keywords: d[prefix+"keywords"]}
	if seo.Title == "" {
		seo.Title = "Thailand proxy access | Proxy Server"
	}
	if seo.Description == "" {
		seo.Description = "Measured-capacity proxy access from Thailand. Compare board-based shared and dedicated plans and manage your account securely."
	}
	if pages := m.SEO[locale]; pages != nil {
		if custom, ok := pages[page]; ok {
			if custom.Title != "" {
				seo.Title = custom.Title
			}
			if custom.Description != "" {
				seo.Description = custom.Description
			}
			if custom.Keywords != "" {
				seo.Keywords = custom.Keywords
			}
		}
	}
	return seo, d
}
func portalCSP(public bool, m MarketingSettings) string {
	script := "'self' https://challenges.cloudflare.com"
	connect := "'self' https://challenges.cloudflare.com"
	img := "'self' data:"
	frame := "https://challenges.cloudflare.com"
	if public && m.Enabled {
		a := m.Analytics
		if a.GA4ID != "" || a.GoogleAdsID != "" || a.GTMID != "" {
			script += " https://www.googletagmanager.com"
			connect += " https://www.google-analytics.com https://region1.google-analytics.com https://analytics.google.com https://www.googletagmanager.com https://www.google.com"
			img += " https://www.google-analytics.com https://www.googletagmanager.com"
		}
		if a.GoogleAdsID != "" || a.GTMID != "" {
			script += " https://www.googleadservices.com https://www.google.com"
			connect += " https://www.googleadservices.com https://googleads.g.doubleclick.net https://www.google.com https://www.google.co.th https://pagead2.googlesyndication.com https://ad.doubleclick.net"
			img += " https://www.googleadservices.com https://googleads.g.doubleclick.net https://www.google.com https://www.google.co.th https://pagead2.googlesyndication.com"
			frame += " https://www.googletagmanager.com"
		}
		if a.MetaPixelID != "" || a.GTMID != "" {
			script += " https://connect.facebook.net"
			connect += " https://www.facebook.com"
			img += " https://www.facebook.com"
		}
		if a.TikTokPixelID != "" || a.GTMID != "" {
			script += " https://analytics.tiktok.com"
			connect += " https://analytics.tiktok.com https://business-api.tiktok.com"
			img += " https://analytics.tiktok.com"
		}
	}
	return "default-src 'self'; script-src " + script + "; style-src 'self'; connect-src " + connect + "; frame-src " + frame + "; img-src " + img + "; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"
}
func portalSEOHead(locale, page string, seo MarketingSEO, indexable bool) string {
	e := html.EscapeString
	var h strings.Builder
	fmt.Fprintf(&h, `<meta name="description" content="%s"><meta name="keywords" content="%s"><meta name="robots" content="%s">`, e(seo.Description), e(seo.Keywords), map[bool]string{true: "index,follow", false: "noindex,nofollow"}[indexable])
	if !indexable {
		return h.String()
	}
	canonical := portalPublicURL(locale, page)
	fmt.Fprintf(&h, `<link rel="canonical" href="%s">`, e(canonical))
	for _, lang := range portalLocales {
		fmt.Fprintf(&h, `<link rel="alternate" hreflang="%s" href="%s">`, lang, e(portalPublicURL(lang, page)))
	}
	fmt.Fprintf(&h, `<link rel="alternate" hreflang="x-default" href="%s">`, e(portalPublicURL("th", page)))
	base, _ := portalBase()
	base.Path = "/portal/og/portal-" + locale + ".png"
	ogLocale := map[string]string{"th": "th_TH", "en": "en_US", "zh-CN": "zh_CN", "ja": "ja_JP", "ru": "ru_RU", "vi": "vi_VN"}[locale]
	for lang, region := range map[string]string{"th": "th_TH", "en": "en_US", "zh-CN": "zh_CN", "ja": "ja_JP", "ru": "ru_RU", "vi": "vi_VN"} {
		if lang != locale {
			fmt.Fprintf(&h, `<meta property="og:locale:alternate" content="%s">`, region)
		}
	}
	fmt.Fprintf(&h, `<meta property="og:type" content="website"><meta property="og:site_name" content="Proxy Server"><meta property="og:locale" content="%s"><meta property="og:title" content="%s"><meta property="og:description" content="%s"><meta property="og:url" content="%s"><meta property="og:image" content="%s"><meta property="og:image:width" content="1200"><meta property="og:image:height" content="630"><meta property="og:image:alt" content="%s"><meta name="twitter:card" content="summary_large_image"><meta name="twitter:title" content="%s"><meta name="twitter:description" content="%s"><meta name="twitter:image" content="%s">`, ogLocale, e(seo.Title), e(seo.Description), e(canonical), e(base.String()), e(seo.Title), e(seo.Title), e(seo.Description), e(base.String()))
	return h.String()
}
func portalPublicContent(locale, page string, d map[string]string) string {
	e := html.EscapeString
	h1 := d["seo."+page+".h1"]
	if h1 == "" {
		h1 = d["seo."+page+".title"]
	}
	if h1 == "" {
		h1 = "Proxy Server · Thailand"
	}
	intro := d["seo."+page+".intro"]
	if intro == "" {
		intro = d["seo."+page+".description"]
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<section id="publicContent" class="public-content" aria-labelledby="publicHeading"><h1 id="publicHeading">%s</h1><p>%s</p><nav aria-label="%s">`, e(h1), e(intro), e(d["nav.label"]))
	for _, p := range portalPublicPages {
		label := d["seo."+p+".h1"]
		if label == "" {
			label = p
		}
		path := "/portal/" + locale + "/"
		if p != "home" {
			path += p
		}
		fmt.Fprintf(&b, `<a href="%s">%s</a>`, path, e(label))
	}
	b.WriteString(`</nav>`)
	if body := d["seo."+page+".body"]; body != "" {
		fmt.Fprintf(&b, `<p class="public-body">%s</p>`, e(body))
	}
	b.WriteString(`<div class="public-features">`)
	for i := 1; i <= 3; i++ {
		prefix := fmt.Sprintf("seo.%s.features.%d.", page, i)
		if title := d[prefix+"title"]; title != "" {
			fmt.Fprintf(&b, `<article><h2>%s</h2><p>%s</p></article>`, e(title), e(d[prefix+"body"]))
		}
	}
	b.WriteString(`</div>`)
	if q := d["seo."+page+".faq.question"]; q != "" {
		fmt.Fprintf(&b, `<section class="public-faq"><h2>%s</h2><p>%s</p></section>`, e(q), e(d["seo."+page+".faq.answer"]))
	}
	b.WriteString(`<div class="language-links">`)
	for _, lang := range portalLocales {
		path := "/portal/" + lang + "/"
		if page != "home" {
			path += page
		}
		fmt.Fprintf(&b, `<a href="%s" hreflang="%s" lang="%s">%s</a>`, path, lang, lang, e(map[string]string{"th": "ไทย", "en": "English", "zh-CN": "简体中文", "ja": "日本語", "ru": "Русский", "vi": "Tiếng Việt"}[lang]))
	}
	b.WriteString(`</div></section>`)
	return b.String()
}
func registerPortalStatic(mux *http.ServeMux, s *server) {
	mux.HandleFunc("GET /portal", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/portal/", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("GET /portal/sitemap.xml", s.handlePortalSitemap)
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300")
		fmt.Fprint(w, "User-agent: *\nDisallow: /api/\nDisallow: /gost/\nDisallow: /webhook/\nDisallow: /portal/api/\n")
		if base, ok := portalBase(); ok {
			base.Path = "/portal/sitemap.xml"
			fmt.Fprintln(w, "Sitemap: "+base.String())
		} else {
			fmt.Fprintln(w, "Disallow: /portal/")
		}
	})
	sub, _ := fs.Sub(portalFS, "portal")
	mux.Handle("GET /portal/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/portal/")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if portalAssetRE.MatchString(name) {
			data, err := fs.ReadFile(sub, name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			switch {
			case strings.HasSuffix(name, ".js"):
				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			case strings.HasSuffix(name, ".css"):
				w.Header().Set("Content-Type", "text/css; charset=utf-8")
			case strings.HasSuffix(name, ".json"):
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
			case strings.HasSuffix(name, ".png"):
				w.Header().Set("Content-Type", "image/png")
				w.Header().Set("Cache-Control", "public, max-age=3600")
			}
			w.Write(data)
			return
		}
		locale, page, public, valid := portalRoute(r.URL.Path)
		if !valid {
			http.NotFound(w, r)
			return
		}
		m, err := s.marketingSettings(r.Context())
		if err != nil {
			portalError(w, 503, "portal settings unavailable")
			return
		}
		_, configured := portalBase()
		indexable := public && configured && r.URL.RawQuery == ""
		cleanLocalized := public && strings.TrimPrefix(r.URL.Path, "/portal/") != "" && r.URL.Path != "/portal/index.html"
		w.Header().Set("Content-Security-Policy", portalCSP(cleanLocalized && r.URL.RawQuery == "", m))
		w.Header().Set("Content-Language", locale)
		if !indexable {
			w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		}
		raw, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		seo, dict := portalSEO(locale, page, m)
		doc := string(raw)
		doc = htmlTitleRE.ReplaceAllStringFunc(doc, func(string) string { return "<title>" + html.EscapeString(seo.Title) + "</title>" })
		doc = htmlLangRE.ReplaceAllString(doc, `<html lang="`+locale+`"`)
		doc = strings.Replace(doc, "</head>", portalSEOHead(locale, page, seo, indexable)+"</head>", 1)
		content := `<section id="publicContent" class="public-content" hidden></section>`
		if public {
			content = portalPublicContent(locale, page, dict)
		}
		if strings.Contains(doc, "<!--PORTAL_PUBLIC_CONTENT-->") {
			doc = strings.Replace(doc, "<!--PORTAL_PUBLIC_CONTENT-->", content, 1)
		} else {
			doc = strings.Replace(doc, `<div id="page">`, `<div id="page">`+content, 1)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, doc)
	}))
}
func (s *server) handlePortalSitemap(w http.ResponseWriter, r *http.Request) {
	if _, ok := portalBase(); !ok {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		portalError(w, 503, "public portal URL not configured")
		return
	}
	type sitemapURL struct {
		Loc string `xml:"loc"`
	}
	type sitemap struct {
		XMLName xml.Name     `xml:"urlset"`
		NS      string       `xml:"xmlns,attr"`
		URLs    []sitemapURL `xml:"url"`
	}
	out := sitemap{NS: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	for _, locale := range portalLocales {
		for _, page := range portalPublicPages {
			out.URLs = append(out.URLs, sitemapURL{portalPublicURL(locale, page)})
		}
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	fmt.Fprint(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(out)
}
