package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCachePutAndGet(t *testing.T) {
	rc := newRenderCache(1<<20, 5*time.Minute)
	defer rc.Close()

	rc.Put("key1", "<html>hello</html>", nil)

	got, ok := rc.Get("key1")
	if !ok {
		t.Fatal("expected cache hit for key1")
	}
	if got != "<html>hello</html>" {
		t.Errorf("got %q, want %q", got, "<html>hello</html>")
	}
}

func TestCacheMiss(t *testing.T) {
	rc := newRenderCache(1<<20, 5*time.Minute)
	defer rc.Close()

	_, ok := rc.Get("nonexistent")
	if ok {
		t.Error("expected cache miss for nonexistent key")
	}
}

func TestCacheExpiry(t *testing.T) {
	rc := newRenderCache(1<<20, 50*time.Millisecond)
	defer rc.Close()

	rc.Put("key1", "data", nil)

	// Should be a hit immediately
	if _, ok := rc.Get("key1"); !ok {
		t.Fatal("expected cache hit before expiry")
	}

	time.Sleep(100 * time.Millisecond)

	// Should be expired now
	if _, ok := rc.Get("key1"); ok {
		t.Error("expected cache miss after expiry")
	}
}

func TestCacheSizeEviction(t *testing.T) {
	// Cache max size of 100 bytes
	rc := newRenderCache(100, 5*time.Minute)
	defer rc.Close()

	// Each entry is ~50 bytes
	rc.Put("key1", strings.Repeat("a", 50), nil)
	rc.Put("key2", strings.Repeat("b", 50), nil)

	// Both should fit
	if _, ok := rc.Get("key1"); !ok {
		t.Error("expected key1 to be cached")
	}
	if _, ok := rc.Get("key2"); !ok {
		t.Error("expected key2 to be cached")
	}

	// Adding a third should evict the oldest (key1, since key2 was just accessed by Get)
	rc.Put("key3", strings.Repeat("c", 50), nil)

	if _, ok := rc.Get("key1"); ok {
		t.Error("expected key1 to be evicted")
	}
	if _, ok := rc.Get("key3"); !ok {
		t.Error("expected key3 to be cached")
	}
}

func TestCacheOversizedEntryNotCached(t *testing.T) {
	rc := newRenderCache(10, 5*time.Minute)
	defer rc.Close()

	rc.Put("big", strings.Repeat("x", 100), nil)

	if _, ok := rc.Get("big"); ok {
		t.Error("entry larger than max size should not be cached")
	}
}

func TestCacheStats(t *testing.T) {
	rc := newRenderCache(1<<20, 5*time.Minute)
	defer rc.Close()

	rc.Put("a", "hello", nil)
	rc.Put("b", "world!", nil)

	entries, size := rc.Stats()
	if entries != 2 {
		t.Errorf("entries = %d, want 2", entries)
	}
	if size != 11 {
		t.Errorf("size = %d, want 11", size)
	}
}

func TestCacheFileWatchInvalidation(t *testing.T) {
	rc := newRenderCache(1<<20, 5*time.Minute)
	defer rc.Close()

	// Create a temp file to watch
	dir := t.TempDir()
	srcFile := filepath.Join(dir, "test.yaml")
	os.WriteFile(srcFile, []byte("original"), 0644)

	rc.Put("page1", "<html>cached</html>", []string{srcFile})

	// Should be a hit
	if _, ok := rc.Get("page1"); !ok {
		t.Fatal("expected cache hit before file change")
	}

	// Modify the source file — fsnotify should invalidate the entry
	os.WriteFile(srcFile, []byte("modified"), 0644)

	// Give the watcher a moment to process the event
	time.Sleep(200 * time.Millisecond)

	if _, ok := rc.Get("page1"); ok {
		t.Error("expected cache miss after source file was modified")
	}
}

func TestCacheNewFileInvalidation(t *testing.T) {
	rc := newRenderCache(1<<20, 5*time.Minute)
	defer rc.Close()

	dir := t.TempDir()
	existingFile := filepath.Join(dir, "html.yaml")
	os.WriteFile(existingFile, []byte("html: body"), 0644)

	rc.Put("page1", "<html>cached</html>", []string{existingFile})

	// Should be a hit
	if _, ok := rc.Get("page1"); !ok {
		t.Fatal("expected cache hit before new file creation")
	}

	// Create a new file in the watched directory — should invalidate because
	// a new file could change name resolution order
	os.WriteFile(filepath.Join(dir, "navbar.yaml"), []byte("navbar: nav"), 0644)

	time.Sleep(200 * time.Millisecond)

	if _, ok := rc.Get("page1"); ok {
		t.Error("expected cache miss after new file created in watched directory")
	}
}

func TestCacheReplace(t *testing.T) {
	rc := newRenderCache(1<<20, 5*time.Minute)
	defer rc.Close()

	rc.Put("key1", "first", nil)
	rc.Put("key1", "second", nil)

	got, ok := rc.Get("key1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got != "second" {
		t.Errorf("got %q, want %q", got, "second")
	}

	entries, size := rc.Stats()
	if entries != 1 {
		t.Errorf("entries = %d, want 1 after replace", entries)
	}
	if size != 6 {
		t.Errorf("size = %d, want 6 after replace", size)
	}
}

func TestCacheKey(t *testing.T) {
	k1 := cacheKey("/srv/default", "/srv/default/index.yaml")
	k2 := cacheKey("/srv/other", "/srv/other/index.yaml")
	k3 := cacheKey("/srv/default", "/srv/default/about.yaml")

	if k1 == k2 {
		t.Error("different docRoots should produce different keys")
	}
	if k1 == k3 {
		t.Error("different file paths should produce different keys")
	}
}

func TestStaticFileCacheControl(t *testing.T) {
	maxAge := 24 * time.Hour

	// File modified 1 hour ago → max-age should be 30 minutes (half of 1 hour)
	cc := staticFileCacheControl(time.Now().Add(-1*time.Hour), maxAge)
	if !strings.HasPrefix(cc, "public, max-age=") {
		t.Errorf("unexpected Cache-Control: %q", cc)
	}
	if !strings.Contains(cc, "max-age=1800") {
		t.Errorf("expected max-age=1800 (half of 1 hour), got %q", cc)
	}

	// File modified 10 days ago → max-age capped at 24 hours
	cc = staticFileCacheControl(time.Now().Add(-240*time.Hour), maxAge)
	if !strings.Contains(cc, "max-age=86400") {
		t.Errorf("expected max-age=86400 (capped at 24h), got %q", cc)
	}

	// Very recently modified → minimum 60 seconds
	cc = staticFileCacheControl(time.Now().Add(-10*time.Second), maxAge)
	if !strings.Contains(cc, "max-age=60") {
		t.Errorf("expected min max-age=60, got %q", cc)
	}

	// Zero time → empty
	cc = staticFileCacheControl(time.Time{}, maxAge)
	if cc != "" {
		t.Errorf("expected empty for zero time, got %q", cc)
	}
}

func TestDetectAvailableRAM(t *testing.T) {
	// Test that it returns at most the configured max
	configured := int64(1 << 30) // 1 GB
	result := detectAvailableRAM(configured)
	if result > configured {
		t.Errorf("result %d exceeds configured max %d", result, configured)
	}
	if result <= 0 {
		t.Errorf("result should be positive, got %d", result)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{1 << 30, "1.0 GB"},
		{512 * (1 << 20), "512.0 MB"},
		{100 * (1 << 20), "100.0 MB"},
		{int64(1.5 * float64(1<<30)), "1.5 GB"},
	}
	for _, tc := range tests {
		got := formatBytes(tc.input)
		if got != tc.expected {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestCacheIntegrationWithRender(t *testing.T) {
	rc := newRenderCache(1<<20, 5*time.Minute)
	defer rc.Close()

	docRoot := filepath.Join(func() string { b, _ := os.Getwd(); return b }(), "www", "default")
	yamlPath := filepath.Join(docRoot, "index.yaml")
	key := cacheKey(docRoot, yamlPath)

	// First render: cache miss
	if _, ok := rc.Get(key); ok {
		t.Fatal("expected cache miss on first access")
	}

	output, sourceFiles := renderYAMLPage(docRoot, yamlPath, false, 1, nil)
	rc.Put(key, output, sourceFiles)

	// Second access: cache hit with same content
	cached, ok := rc.Get(key)
	if !ok {
		t.Fatal("expected cache hit after Put")
	}
	if cached != output {
		t.Error("cached output differs from rendered output")
	}

	// Source files should include html.yaml and other site files
	if len(sourceFiles) == 0 {
		t.Error("expected source files list to be non-empty")
	}
	foundHTMLYaml := false
	for _, f := range sourceFiles {
		if strings.HasSuffix(f, "html.yaml") {
			foundHTMLYaml = true
		}
	}
	if !foundHTMLYaml {
		t.Error("source files should include html.yaml")
	}
}

func TestCacheControlHeaderOnRenderedPage(t *testing.T) {
	base, _ := os.Getwd()
	cache := newRenderCache(1<<20, 5*time.Minute)
	defer cache.Close()

	mux := &virtualHostMux{
		cfg: &config{
			Base:  filepath.Join(base, "www"),
			Cache: cache,
			Site: siteSettings{
				Index:        []string{"index.yaml", "index.md"},
				ParentLevels: 1,
				CacheAge:     5 * time.Minute,
				StaticAge:    24 * time.Hour,
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "default"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	cc := resp.Header.Get("Cache-Control")
	if cc == "" {
		t.Error("rendered page should have Cache-Control header")
	}
	if !strings.Contains(cc, "max-age=300") {
		t.Errorf("expected max-age=300 (5 min), got %q", cc)
	}

	// Second request should be a cache hit (verify by checking header is still set)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req)
	resp2 := w2.Result()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("cached status = %d, want 200", resp2.StatusCode)
	}
	if w2.Body.String() != w.Body.String() {
		t.Error("cached response body differs from first response")
	}
}
