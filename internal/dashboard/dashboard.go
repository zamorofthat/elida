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
	_ = fs.WalkDir(staticFS, ".", func(path string, d fs.DirEntry, err error) error {
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

// ServeHTTP serves the dashboard files with SPA fallback.
// Static assets (JS, CSS, images) are served directly.
// All other paths get index.html so client-side routing works on reload.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// Serve index.html for root
	if path == "/" || path == "" || path == "/index.html" {
		h.serveIndex(w, r)
		return
	}

	// Check if the path matches a real static file
	if h.hasStaticFile(path) {
		h.fileServer.ServeHTTP(w, r)
		return
	}

	// SPA fallback: serve index.html for all other routes
	h.serveIndex(w, r)
}

// hasStaticFile checks if a path corresponds to an embedded static file.
func (h *Handler) hasStaticFile(path string) bool {
	// Strip leading slash for embed.FS lookup
	name := "static" + path
	f, err := staticFiles.Open(name)
	if err != nil {
		return false
	}
	_ = f.Close()
	info, err := fs.Stat(staticFiles, name)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// serveIndex serves the index.html file directly
func (h *Handler) serveIndex(w http.ResponseWriter, _ *http.Request) {
	content, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "Dashboard not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(content)
}
