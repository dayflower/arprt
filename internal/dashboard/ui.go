package dashboard

import (
	"embed"
	"net/http"
	"strconv"
)

//go:embed assets/*
var uiAssets embed.FS

// serveUI uses an explicit allowlist, so API and unknown paths cannot fall back
// to HTML or expose the embedded filesystem.
func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) bool {
	var name, contentType string
	switch r.URL.Path {
	case "/":
		name, contentType = "index.html", "text/html; charset=utf-8"
	case "/assets/dashboard.css":
		name, contentType = "dashboard.css", "text/css; charset=utf-8"
	case "/assets/dashboard.js":
		name, contentType = "dashboard.js", "text/javascript; charset=utf-8"
	case "/assets/mark.svg":
		name, contentType = "mark.svg", "image/svg+xml"
	default:
		return false
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		fail(w, http.StatusMethodNotAllowed)
		return true
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		fail(w, http.StatusBadRequest)
		return true
	}
	b, err := uiAssets.ReadFile("assets/" + name)
	if err != nil {
		fail(w, http.StatusInternalServerError)
		return true
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
	return true
}
