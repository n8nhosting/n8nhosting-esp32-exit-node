package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dashboard
var dashboardFS embed.FS

func dashboardHandler() http.Handler {
	sub, _ := fs.Sub(dashboardFS, "dashboard")
	index, _ := fs.ReadFile(sub, "index.html")
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		// SPA root: write index.html directly (FileServer would redirect
		// /index.html -> / and loop). Other assets are served normally.
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Write(index)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
