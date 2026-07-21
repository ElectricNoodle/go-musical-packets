package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHandlerServesAssetsAndSPAFallback(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><div>Musical Packets</div>")},
		"assets/app-abc123.js": {Data: []byte("console.log('ready')")},
	}
	handler := newHandler(assets)

	tests := []struct {
		name        string
		method      string
		path        string
		status      int
		body        string
		cache       string
		contentType string
	}{
		{name: "root", method: http.MethodGet, path: "/", status: 200, body: "Musical Packets", cache: "no-cache", contentType: "text/html; charset=utf-8"},
		{name: "SPA route", method: http.MethodGet, path: "/setup/capture", status: 200, body: "Musical Packets", cache: "no-cache", contentType: "text/html; charset=utf-8"},
		{name: "asset", method: http.MethodGet, path: "/assets/app-abc123.js", status: 200, body: "console.log", cache: "public, max-age=31536000, immutable", contentType: "text/javascript; charset=utf-8"},
		{name: "missing asset", method: http.MethodGet, path: "/assets/missing.js", status: 404, body: "404 page not found"},
		{name: "method", method: http.MethodPost, path: "/", status: 405, body: "method not allowed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.body) {
				t.Fatalf("response = %d %q, want %d containing %q", response.Code, response.Body.String(), test.status, test.body)
			}
			if test.cache != "" && response.Header().Get("Cache-Control") != test.cache {
				t.Fatalf("Cache-Control = %q, want %q", response.Header().Get("Cache-Control"), test.cache)
			}
			if test.contentType != "" && response.Header().Get("Content-Type") != test.contentType {
				t.Fatalf("Content-Type = %q, want %q", response.Header().Get("Content-Type"), test.contentType)
			}
			assertWebSecurityHeaders(t, response.Header())
		})
	}
}

func TestHandlerWithoutBuildReturnsUnavailable(t *testing.T) {
	handler := newHandler(fstest.MapFS{".placeholder": {Data: []byte("build first")}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

func TestHandlerRejectsTraversal(t *testing.T) {
	handler := newHandler(fstest.MapFS{"index.html": {Data: []byte("index")}})
	request := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	request.URL.Path = "/../../secret"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func assertWebSecurityHeaders(t *testing.T, header http.Header) {
	t.Helper()
	for _, name := range []string{"Content-Security-Policy", "Referrer-Policy", "X-Content-Type-Options", "X-Frame-Options", "Cross-Origin-Opener-Policy", "Permissions-Policy"} {
		if header.Get(name) == "" {
			t.Fatalf("%s is empty", name)
		}
	}
}

var _ fs.FS = fstest.MapFS{}
