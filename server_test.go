package main

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// newTestMux creates a virtualHostMux configured for testing with the given base directory.
func newTestMux(t *testing.T, base string) *virtualHostMux {
	t.Helper()
	return &virtualHostMux{
		cfg: &config{
			Base: base,
			Site: siteSettings{
				CacheAge:     15 * time.Minute,
				StaticAge:    24 * time.Hour,
				ParentLevels: 1,
				Index:        []string{"index.yaml", "index.md", "index.html"},
				MaxBodyBytes: 1 << 20,
			},
		},
	}
}

func TestHandleYAMLRendersHTML(t *testing.T) {
	base, _ := os.Getwd()
	mux := newTestMux(t, filepath.Join(base, "www"))

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
	mux := newTestMux(t, filepath.Join(base, "www"))

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

func TestKnownVhostFallsBackToDefault(t *testing.T) {
	base, _ := os.Getwd()
	mux := newTestMux(t, filepath.Join(base, "www"))

	// "www.default" is one subdomain deeper than the "default" vhost
	// directory, so it's a known vhost and should fall back to default/
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "www.default"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d (known vhost should fall back to default/)", resp.StatusCode, http.StatusOK)
	}
}

func TestUnknownVhostRejects(t *testing.T) {
	base, _ := os.Getwd()
	mux := newTestMux(t, filepath.Join(base, "www"))

	// A completely unknown domain should get 421, not fall back to default
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "nonexistent.example.com"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("status = %d, want %d (unknown vhost should not be served)", resp.StatusCode, http.StatusMisdirectedRequest)
	}
}

func TestDeeplyNestedVhostRejects(t *testing.T) {
	base, _ := os.Getwd()
	mux := newTestMux(t, filepath.Join(base, "www"))

	// Deeply nested subdomain of a known vhost should be rejected
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "update.update.update.m.default"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("status = %d, want %d (deeply nested domain should not be served)", resp.StatusCode, http.StatusMisdirectedRequest)
	}
}

func TestNotFoundReturns404(t *testing.T) {
	base, _ := os.Getwd()
	mux := newTestMux(t, filepath.Join(base, "www"))

	req := httptest.NewRequest("GET", "/this-page-does-not-exist-at-all", nil)
	req.Host = "default"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestNotFoundRendersErrorPage(t *testing.T) {
	base, _ := os.Getwd()
	mux := newTestMux(t, filepath.Join(base, "www"))

	req := httptest.NewRequest("GET", "/this-page-does-not-exist-at-all", nil)
	req.Host = "default"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	body := w.Body.String()
	// Should be a full HTML page from error.yaml, not plain text
	if !strings.Contains(body, "<!DOCTYPE html>") {
		t.Error("404 response should be a full HTML page from error.yaml")
	}
	if !strings.Contains(body, "404") {
		t.Error("404 response missing status code")
	}
	if !strings.Contains(body, "Not Found") {
		t.Error("404 response missing status text")
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("404 Content-Type = %q, want text/html", ct)
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
	mux := newTestMux(t, filepath.Join(base, "www"))

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
		"X-Frame-Options":        "SAMEORIGIN",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
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

func TestIsKnownVhost(t *testing.T) {
	tmpDir := t.TempDir()
	// Create vhost directories
	os.MkdirAll(filepath.Join(tmpDir, "example.com"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "bar.org"), 0755)

	tests := []struct {
		host string
		want bool
		desc string
	}{
		// Direct matches
		{"example.com", true, "exact vhost match"},
		{"bar.org", true, "exact vhost match"},
		{"Example.COM", true, "case insensitive match"},

		// One level deeper (allowed)
		{"www.example.com", true, "www prefix"},
		{"api.example.com", true, "subdomain of vhost"},
		{"m.bar.org", true, "subdomain of vhost"},

		// Two or more levels deeper (rejected)
		{"a.b.example.com", false, "two levels deep"},
		{"x.y.z.example.com", false, "three levels deep"},
		{"update.update.update.m.example.com", false, "many levels deep"},

		// No matching vhost at all
		{"unknown.com", false, "no matching vhost"},
		{"www.unknown.com", false, "subdomain of non-existent vhost"},
		{"a.b.c.d.e.f.bserver.info", false, "deeply nested bogus domain"},
	}

	for _, tt := range tests {
		if got := isKnownVhost(tt.host, tmpDir); got != tt.want {
			t.Errorf("isKnownVhost(%q) = %v, want %v (%s)", tt.host, got, tt.want, tt.desc)
		}
	}
}

func TestIsKnownVhostSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a real directory and a symlink to it
	os.MkdirAll(filepath.Join(tmpDir, "default"), 0755)
	os.Symlink(filepath.Join(tmpDir, "default"), filepath.Join(tmpDir, "mysite.com"))

	if !isKnownVhost("mysite.com", tmpDir) {
		t.Error("isKnownVhost should follow symlinks")
	}
	if !isKnownVhost("www.mysite.com", tmpDir) {
		t.Error("isKnownVhost should allow one level above symlinked vhost")
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

func TestOversizedPostBodyReturns413ForYAML(t *testing.T) {
	base, _ := os.Getwd()
	mux := newTestMux(t, filepath.Join(base, "www"))
	mux.cfg.Site.MaxBodyBytes = 8
	vhostConfigCache.Delete(filepath.Join(base, "www", "default"))

	req := httptest.NewRequest("POST", "/", strings.NewReader("0123456789"))
	req.Host = "default"
	req.ContentLength = int64(len("0123456789"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestPostBodyWithinLimitStillRendersYAML(t *testing.T) {
	base, _ := os.Getwd()
	mux := newTestMux(t, filepath.Join(base, "www"))
	mux.cfg.Site.MaxBodyBytes = 64
	vhostConfigCache.Delete(filepath.Join(base, "www", "default"))

	req := httptest.NewRequest("POST", "/", strings.NewReader("ok"))
	req.Host = "default"
	req.ContentLength = int64(len("ok"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if !strings.Contains(w.Body.String(), "<!DOCTYPE html>") {
		t.Fatal("expected rendered HTML body")
	}
}

func TestHardenedTLSConfigDefaults(t *testing.T) {
	cfg := &config{Base: t.TempDir(), CacheDir: t.TempDir()}
	m := &autocert.Manager{}
	tlsCfg := hardenedTLSConfig(cfg, m)

	if tlsCfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %v, want %v", tlsCfg.MinVersion, tls.VersionTLS12)
	}
	if len(tlsCfg.CurvePreferences) == 0 {
		t.Fatal("expected CurvePreferences to be set")
	}
	if len(tlsCfg.CipherSuites) == 0 {
		t.Fatal("expected CipherSuites to be set")
	}
	if tlsCfg.GetCertificate == nil {
		t.Fatal("expected GetCertificate callback")
	}

	hasACME := false
	for _, proto := range tlsCfg.NextProtos {
		if proto == acmeALPNProto {
			hasACME = true
			break
		}
	}
	if !hasACME {
		t.Fatalf("NextProtos missing %q", acmeALPNProto)
	}
}
