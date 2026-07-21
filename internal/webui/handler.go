// Package webui serves the embedded production frontend.
package webui

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// assets always contains the tracked placeholder so Go packages compile from
// a clean checkout. The supported production build runs Vite first and embeds
// the generated index and hashed assets into the final binary.
//
//go:embed all:dist
var assets embed.FS

const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self' ws: wss:; font-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

// NewHandler returns an immutable handler over the embedded Vite build.
func NewHandler() http.Handler {
	dist, err := fs.Sub(assets, "dist")
	if err != nil {
		return unavailableHandler{}
	}
	return newHandler(dist)
}

type handler struct {
	assets     fs.FS
	fileServer http.Handler
	ready      bool
}

func newHandler(assets fs.FS) http.Handler {
	_, err := fs.Stat(assets, "index.html")
	return &handler{
		assets:     assets,
		fileServer: http.FileServerFS(assets),
		ready:      err == nil,
	}
}

func (handler *handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	setHeaders(response.Header())
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !handler.ready {
		http.Error(response, "frontend assets are unavailable; build the web application first", http.StatusServiceUnavailable)
		return
	}

	name := strings.TrimPrefix(request.URL.Path, "/")
	cleaned := path.Clean(name)
	if cleaned == "." {
		cleaned = "index.html"
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		http.NotFound(response, request)
		return
	}
	if info, err := fs.Stat(handler.assets, cleaned); err == nil && !info.IsDir() {
		if strings.HasPrefix(cleaned, "assets/") {
			response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			response.Header().Set("Cache-Control", "no-cache")
		}
		handler.fileServer.ServeHTTP(response, request)
		return
	}
	if path.Ext(cleaned) != "" {
		http.NotFound(response, request)
		return
	}

	response.Header().Set("Cache-Control", "no-cache")
	fallback := request.Clone(request.Context())
	fallback.URL.Path = "/"
	fallback.URL.RawPath = ""
	handler.fileServer.ServeHTTP(response, fallback)
}

type unavailableHandler struct{}

func (unavailableHandler) ServeHTTP(response http.ResponseWriter, _ *http.Request) {
	setHeaders(response.Header())
	http.Error(response, "frontend assets are unavailable", http.StatusServiceUnavailable)
}

func setHeaders(header http.Header) {
	header.Set("Content-Security-Policy", contentSecurityPolicy)
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
}

func (handler *handler) String() string {
	return fmt.Sprintf("embedded web UI (ready=%t)", handler.ready)
}
