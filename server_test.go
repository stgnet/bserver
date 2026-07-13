package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestUnknownHost404IsBare verifies that a 404 for a host with no
// matching directory under www/ returns the bare status code with no
// rendered body. Scanners send arbitrary Host headers and unique paths;
// rendering and caching their 404s would explode the render cache (host
// is part of the key) without serving any real user.
func TestUnknownHost404IsBare(t *testing.T) {
	base, _ := os.Getwd()
	mux := newTestMux(t, filepath.Join(base, "www"))

	req := httptest.NewRequest("GET", "/some-missing-path", nil)
	req.Host = "scanner.example" // single dot → falls back to default
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	if body := w.Body.String(); body != "" {
		t.Errorf("fallback host should get bare 404 with no body, got %d bytes", len(body))
	}
}

// TestErrorPageBailsOutIfBlockedWhileQueued verifies that serveErrorPage
// re-checks the rate limiter after acquiring its render semaphore. During
// a scanner flood, many concurrent 404 requests pass the middleware's
// isBlocked check before the block triggers, then pile up in the render
// queue. If the block fires while requests are waiting, the queued
// requests must not render a full error page anyway.
func TestErrorPageBailsOutIfBlockedWhileQueued(t *testing.T) {
	base, _ := os.Getwd()
	mux := newTestMux(t, filepath.Join(base, "www"))
	mux.rl = newRateLimiter()
	defer mux.rl.Close()

	ip := "203.0.113.99"

	// Drive the IP into blocked state directly (simulates the state that
	// would exist after 10 in-flight errors completed while our request
	// was waiting in the render queue).
	for i := 0; i < maxConsecutiveErrors; i++ {
		mux.rl.recordResult(ip, http.StatusNotFound)
	}
	if blocked, _ := mux.rl.isBlocked(ip); !blocked {
		t.Fatal("setup failed: IP should be blocked")
	}

	req := httptest.NewRequest("GET", "/this-path-does-not-exist-xyz", nil)
	req.Host = "default"
	req.RemoteAddr = ip + ":40000"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	// Body should be empty — the render was skipped because the IP
	// became blocked while the request was (conceptually) queued.
	if body := w.Body.String(); body != "" {
		t.Errorf("blocked IP should get bare status with no body, got %d bytes", len(body))
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

// TestHiddenPathsBlocked ensures version-control metadata and other dotfiles
// are never served, even though extensionless files bypass the allowed-types
// check. .well-known must remain reachable.
func TestHiddenPathsBlocked(t *testing.T) {
	tmpDir := t.TempDir()
	defaultDir := filepath.Join(tmpDir, "default")
	gitDir := filepath.Join(defaultDir, ".git")
	wkDir := filepath.Join(defaultDir, ".well-known")
	os.MkdirAll(gitDir, 0755)
	os.MkdirAll(wkDir, 0755)
	vendorDir := filepath.Join(defaultDir, "vendor")
	os.MkdirAll(vendorDir, 0755)
	os.WriteFile(filepath.Join(gitDir, "index"), []byte("DIRC-secret"), 0644)
	os.WriteFile(filepath.Join(gitDir, "config"), []byte("[core]"), 0644)
	os.WriteFile(filepath.Join(defaultDir, ".env"), []byte("SECRET=1"), 0644)
	os.WriteFile(filepath.Join(vendorDir, "autoload.php"), []byte("secret"), 0644)
	os.WriteFile(filepath.Join(wkDir, "security.txt"), []byte("Contact: x"), 0644)

	mux := newTestMux(t, tmpDir)

	for _, p := range []string{"/.git/index", "/.git/config", "/.git/", "/.env", "/vendor/autoload.php", "/vendor/"} {
		req := httptest.NewRequest("GET", p, nil)
		req.Host = "default"
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Result().StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", p, w.Result().StatusCode)
		}
		if strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "SECRET") {
			t.Errorf("GET %s leaked hidden file content", p)
		}
	}

	// .well-known must still be served.
	req := httptest.NewRequest("GET", "/.well-known/security.txt", nil)
	req.Host = "default"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("GET /.well-known/security.txt: status = %d, want 200", w.Result().StatusCode)
	}
}

// TestPathBlockOverrides verifies allow-paths exposes an otherwise-denied path
// and block-paths denies an additional one.
func TestPathBlockOverrides(t *testing.T) {
	tmpDir := t.TempDir()
	defaultDir := filepath.Join(tmpDir, "default")
	os.MkdirAll(filepath.Join(defaultDir, "vendor", "pub"), 0755)
	os.MkdirAll(filepath.Join(defaultDir, "private"), 0755)
	os.WriteFile(filepath.Join(defaultDir, "vendor", "pub", "app.js"), []byte("ok"), 0644)
	os.WriteFile(filepath.Join(defaultDir, "private", "data.txt"), []byte("ok"), 0644)

	mux := newTestMux(t, tmpDir)
	// Expose /vendor/pub (rooted prefix) but keep the rest of vendor blocked;
	// additionally block any "private" directory.
	mux.cfg.Site.AllowedPaths = []string{"/vendor/pub"}
	mux.cfg.Site.BlockedPaths = []string{"private"}

	cases := []struct {
		path string
		want int
	}{
		{"/vendor/pub/app.js", http.StatusOK},      // exempted by allow-paths
		{"/private/data.txt", http.StatusNotFound}, // denied by block-paths
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", c.path, nil)
		req.Host = "default"
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if got := w.Result().StatusCode; got != c.want {
			t.Errorf("GET %s: status = %d, want %d", c.path, got, c.want)
		}
	}
}

func TestPathMatchesPattern(t *testing.T) {
	cases := []struct {
		upath, pattern string
		want           bool
	}{
		{"/vendor/x", "vendor", true},         // bare name, any depth
		{"/a/vendor/b", "vendor", true},       // bare name, nested
		{"/vendored/x", "vendor", false},      // segment-aware, no prefix bleed
		{"/vendor/x", "/vendor", true},        // rooted prefix
		{"/a/vendor/x", "/vendor", false},     // rooted, not at depth
		{"/vendor/pub/x", "vendor/pub", true}, // multi-segment rooted prefix
		{"/vendor/priv", "vendor/pub", false},
		{"/.well-known/x", "/.well-known", true},
		{"/", "vendor", false},
		{"/anything", "", false},
	}
	for _, c := range cases {
		if got := pathMatchesPattern(c.upath, c.pattern); got != c.want {
			t.Errorf("pathMatchesPattern(%q, %q) = %v, want %v", c.upath, c.pattern, got, c.want)
		}
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

func TestCertAllowed(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "example.com"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "app.example.com"), 0755)

	tests := []struct {
		host string
		want bool
		desc string
	}{
		{"example.com", true, "exact base-domain dir"},
		{"app.example.com", true, "exact subdomain dir"},
		{"Example.COM", true, "case insensitive"},
		{"www.example.com", true, "www. alias of a dir"},
		{"www.app.example.com", true, "www. alias of a subdomain dir"},
		// The exhaustion cases: subdomains of a served base domain that
		// have no dir of their own must NOT trigger Let's Encrypt.
		{"scan1.example.com", false, "scanner subdomain, no dir"},
		{"smtp.example.com", false, "scanner subdomain, no dir"},
		{"api.app.example.com", false, "subdomain of a subdomain, no dir"},
		{"unknown.com", false, "no matching dir"},
	}
	for _, tt := range tests {
		if got := certAllowed(tt.host, tmpDir); got != tt.want {
			t.Errorf("certAllowed(%q) = %v, want %v (%s)", tt.host, got, tt.want, tt.desc)
		}
	}
}

func TestValidCertHost(t *testing.T) {
	tests := []struct {
		host string
		want bool
		desc string
	}{
		{"example.com", true, "normal hostname"},
		{"app.example.com", true, "subdomain"},
		{"xn--80ak6aa92e.com", true, "punycode"},
		{"", false, "empty (no SNI)"},
		{"../../etc/passwd", false, "parent traversal"},
		{"a/b", false, "forward slash"},
		{"a\\b", false, "backslash"},
		{"..", false, "bare dot-dot"},
		{"foo..bar", false, "embedded dot-dot"},
		{"foo\x00bar", false, "embedded NUL"},
		{"foo\nbar", false, "embedded newline"},
		{strings.Repeat("a", 254), false, "over length limit"},
	}
	for _, tt := range tests {
		if got := validCertHost(tt.host); got != tt.want {
			t.Errorf("validCertHost(%q) = %v, want %v (%s)", tt.host, got, tt.want, tt.desc)
		}
	}
}

// TestSelfSignedCertRejectsTraversal ensures an attacker-controlled SNI
// containing path-traversal sequences cannot cause a cert/key file to be
// written outside the cache directory.
func TestSelfSignedCertRejectsTraversal(t *testing.T) {
	cacheDir := t.TempDir()
	outsideDir := t.TempDir() // a sibling the traversal would try to reach

	// filepath.Join(cacheDir, host+".crt") for this host cleans to a path
	// under outsideDir if the ".." segments are honored.
	rel, err := filepath.Rel(cacheDir, filepath.Join(outsideDir, "pwn"))
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	if _, err := getOrCreateSelfSignedCert(rel, cacheDir); err == nil {
		t.Fatalf("expected error for traversal host %q, got nil", rel)
	}
	// No .crt/.key file should have been written outside the cache dir.
	for _, suffix := range []string{".crt", ".key"} {
		if _, err := os.Stat(filepath.Join(outsideDir, "pwn"+suffix)); err == nil {
			t.Errorf("traversal wrote file outside cache dir: pwn%s", suffix)
		}
	}
}

// TestSelfSignedCacheBounded verifies the in-memory self-signed cache does
// not grow without bound when flooded with unique fresh (non-expired) hosts.
func TestSelfSignedCacheBounded(t *testing.T) {
	selfSignedCache.Range(func(k, _ any) bool { selfSignedCache.Delete(k); return true })
	t.Cleanup(func() {
		selfSignedCache.Range(func(k, _ any) bool { selfSignedCache.Delete(k); return true })
	})

	now := time.Now()
	for i := 0; i < maxSelfSignedCacheEntries+50; i++ {
		storeSelfSigned(fmt.Sprintf("h%d.example.com", i), &selfSignedEntry{created: now})
	}
	if size := selfSignedCacheSize(); size > maxSelfSignedCacheEntries {
		t.Errorf("self-signed cache grew to %d, want <= %d", size, maxSelfSignedCacheEntries)
	}
}

// TestGetPathProxySSRFGuard verifies the path-based proxy refuses a
// loopback/private backend unless private targets are explicitly allowed.
func TestGetPathProxySSRFGuard(t *testing.T) {
	// Clear any cached proxies from other tests.
	pathProxyCache.Range(func(k, _ any) bool { pathProxyCache.Delete(k); return true })
	t.Cleanup(func() {
		pathProxyCache.Range(func(k, _ any) bool { pathProxyCache.Delete(k); return true })
	})

	// Loopback backend, private not allowed -> refused (nil).
	if rp := getPathProxy("127.0.0.1:7681", false); rp != nil {
		t.Errorf("expected loopback backend to be refused without allow-private")
	}
	// AWS metadata endpoint (link-local), refused.
	if rp := getPathProxy("169.254.169.254:80", false); rp != nil {
		t.Errorf("expected link-local metadata backend to be refused")
	}
	// Same loopback backend, private allowed -> permitted (the web-terminal case).
	if rp := getPathProxy("127.0.0.1:7681", true); rp == nil {
		t.Errorf("expected loopback backend to be allowed with allow-private")
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
