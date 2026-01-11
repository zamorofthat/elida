package dashboard

import (
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
)

//go:embed all:static
var staticFiles embed.FS

// Handler serves the dashboard UI
type Handler struct {
	fileServer http.Handler
}

// New creates a new dashboard handler
func New() *Handler {
	slog.Info("initializing dashboard handler")

	// Get the static subdirectory
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		slog.Error("failed to get static subdirectory", "error", err)
	}

	// Log embedded files
	var fileCount int
	fs.WalkDir(staticFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil {
			slog.Info("embedded file", "path", path, "is_dir", d.IsDir())
			fileCount++
		}
		return nil
	})
	slog.Info("dashboard files embedded", "count", fileCount)

	return &Handler{
		fileServer: http.FileServer(http.FS(staticFS)),
	}
}

// ServeHTTP serves the dashboard files
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// Serve index.html for root and SPA routes
	if path == "/" || path == "" || path == "/index.html" {
		h.serveIndex(w, r)
		return
	}

	// Try to serve static files
	h.fileServer.ServeHTTP(w, r)
}

// serveIndex serves the index.html file directly
func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	content, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "Dashboard not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(content)
}
