package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed portal
var portalFS embed.FS

func registerPortalStatic(mux *http.ServeMux) {
	mux.HandleFunc("GET /portal", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/portal/", http.StatusTemporaryRedirect)
	})
	sub, _ := fs.Sub(portalFS, "portal")
	mux.Handle("GET /portal/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path[len("/portal/"):]
		if name == "" || name == "index.html" {
			name = "index.html"
		}
		if name != "index.html" && name != "portal.js" && name != "portal.css" {
			http.NotFound(w, r)
			return
		}
		data, err := fs.ReadFile(sub, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' https://challenges.cloudflare.com; style-src 'self'; connect-src 'self' https://challenges.cloudflare.com; frame-src https://challenges.cloudflare.com; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		switch name {
		case "index.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case "portal.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		case "portal.css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		}
		w.Write(data)
	}))
}
