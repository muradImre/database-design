package dbServer

import (
	_ "embed"
	"net/http"
)

// uiPage is the single-page admin console, embedded so the binary is
// self-contained and needs no external asset files at runtime.
//
//go:embed web/index.html
var uiPage []byte

// serveUI serves the admin console for the site root and returns 404 for any
// other unmatched path (API routes are registered separately and take priority).
func serveUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(uiPage)
}
