package api

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// The built web UI is compiled into the binary, so a apm2go install is one file
// with no asset directory to deploy, and no version skew between the API and
// the interface that talks to it.
//
//go:embed all:dist
var uiFiles embed.FS

// uiHandler serves the web UI, falling back to index.html for client-side routes.
func (s *Server) uiHandler() http.Handler {
	dist, err := fs.Sub(uiFiles, "dist")
	if err != nil {
		// Only reachable if the embed directive above is broken, which is a
		// build error rather than a runtime condition.
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "web UI was not built into this binary", http.StatusInternalServerError)
		})
	}

	fileServer := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		// Anything the bundle actually contains is served directly. Hashed
		// asset names are immutable, so they get a long cache lifetime while
		// index.html must always be revalidated.
		if f, err := dist.Open(path); err == nil {
			f.Close()
			if strings.HasPrefix(path, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			fileServer.ServeHTTP(w, r)
			return
		}

		// An unknown path under /api is a genuine 404; anything else is a
		// client-side route and must be answered with the app shell.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusNotFound, "no such endpoint")
			return
		}

		index, err := fs.ReadFile(dist, "index.html")
		if err != nil {
			http.Error(w, "web UI was not built into this binary", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}
