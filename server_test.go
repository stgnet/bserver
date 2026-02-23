package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestMux creates a virtualHostMux configured for testing with the given base directory.
func newTestMux(t *testing.T, base string) *virtualHostMux {
	t.Helper()
	return &virtualHostMux{
		cfg: &config{
			Base:            base,
			IndexPriority:   []string{"index.yaml", "index.md", "index.html"},
			MaxParentLevels: 1,
		},
	}
}

func TestHandleYAMLRendersHTML(t *testing.T) {
	base, _ := os.Getwd()
	mux := newTestMux(t, base)

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "default"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<!DOCTYPE html>") {
		t.Error("response missing DOCTYPE")
	}
}

func TestHandleMarkdownRendersHTML(t *testing.T) {
	base, _ := os.Getwd()
	mux := newTestMux(t, base)

	req := httptest.NewRequest("GET", "/getting-started", nil)
	req.Host = "default"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Getting Started") {
		t.Error("markdown page missing heading content")
	}
}

func TestVirtualHostFallsBackToDefault(t *testing.T) {
	base, _ := os.Getwd()
	mux := newTestMux(t, base)

	// Request with a host that doesn't have a directory
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "nonexistent.example.com"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d (should fall back to default/)", resp.StatusCode, http.StatusOK)
	}
}

func TestNotFoundReturns404(t *testing.T) {
	base, _ := os.Getwd()
	mux := newTestMux(t, base)

	req := httptest.NewRequest("GET", "/this-page-does-not-exist-at-all", nil)
	req.Host = "default"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestNotFoundDoesNotLeakPaths(t *testing.T) {
	tmpDir := t.TempDir()
	// No default/ directory, so all requests should 404
	mux := newTestMux(t, tmpDir)

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "missing.example.com"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	body := w.Body.String()
	if strings.Contains(body, tmpDir) {
		t.Errorf("404 response leaks filesystem path %q:\n%s", tmpDir, body)
	}
}

func TestDebugQueryParameter(t *testing.T) {
	base, _ := os.Getwd()
	mux := newTestMux(t, base)

	req := httptest.NewRequest("GET", "/?debug", nil)
	req.Host = "default"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "<!-- resolve") {
		t.Error("debug mode should produce HTML comment tracing")
	}
}

func TestSecurityHeadersMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := securityHeadersMiddleware(inner)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	tests := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":       "SAMEORIGIN",
		"Referrer-Policy":       "strict-origin-when-cross-origin",
	}
	for header, expected := range tests {
		if got := w.Header().Get(header); got != expected {
			t.Errorf("%s = %q, want %q", header, got, expected)
		}
	}
}

func TestLoggingMiddlewareCapturesStatus(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	handler := loggingMiddleware(inner)

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d", w.Code, http.StatusTeapot)
	}
}

func TestStaticFileServing(t *testing.T) {
	tmpDir := t.TempDir()
	defaultDir := filepath.Join(tmpDir, "default")
	os.MkdirAll(defaultDir, 0755)
	os.WriteFile(filepath.Join(defaultDir, "test.css"), []byte("body { color: red; }"), 0644)

	mux := newTestMux(t, tmpDir)

	req := httptest.NewRequest("GET", "/test.css", nil)
	req.Host = "default"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	body := w.Body.String()
	if !strings.Contains(body, "color: red") {
		t.Error("static file content not served correctly")
	}
}

func TestIsPublicDomain(t *testing.T) {
	public := []string{"example.com", "sub.example.com", "bserver.info"}
	for _, h := range public {
		if !isPublicDomain(h) {
			t.Errorf("isPublicDomain(%q) = false, want true", h)
		}
	}
	nonPublic := []string{
		"localhost", "my.local", "192.168.1.1", "::1",
		"dev.test", "foo.internal", "machine.lan",
	}
	for _, h := range nonPublic {
		if isPublicDomain(h) {
			t.Errorf("isPublicDomain(%q) = true, want false", h)
		}
	}
}

func TestHostOnly(t *testing.T) {
	tests := map[string]string{
		"example.com":      "example.com",
		"example.com:8080": "example.com",
		"[::1]:443":        "::1",
	}
	for input, expected := range tests {
		if got := hostOnly(input); got != expected {
			t.Errorf("hostOnly(%q) = %q, want %q", input, got, expected)
		}
	}
}
