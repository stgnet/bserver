package main

// Tests for the hardening added alongside the streaming-timeout fix:
//
//   - httpoxy (CVE-2016-5385): a client-supplied `Proxy:` request header must
//     never reach a subprocess as HTTP_PROXY, or a visitor could hijack the
//     script's outbound HTTP traffic.
//   - The per-vhost `proxy-path` reverse proxy must survive the server's
//     absolute WriteTimeout the same way the vhost-level `http:` proxy does.

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestForwardableAsHTTPVar pins the header-forwarding policy that both the
// php-cgi and inline/data-source env builders rely on.
func TestForwardableAsHTTPVar(t *testing.T) {
	// Proxy is dropped regardless of casing (Go canonicalizes to "Proxy",
	// but the guard is case-insensitive so a hand-built header can't slip by).
	for _, k := range []string{"Proxy", "proxy", "PROXY", "pRoXy"} {
		if forwardableAsHTTPVar(k) {
			t.Errorf("forwardableAsHTTPVar(%q) = true, want false (httpoxy)", k)
		}
	}
	// Ordinary headers — including ones that merely start with "Proxy" — are
	// still forwarded; only the exact Proxy header is dangerous.
	for _, k := range []string{"User-Agent", "Accept", "Proxy-Connection", "X-Forwarded-For"} {
		if !forwardableAsHTTPVar(k) {
			t.Errorf("forwardableAsHTTPVar(%q) = false, want true", k)
		}
	}
}

// TestScriptEnvDropsProxyHeader proves the Proxy header never becomes
// HTTP_PROXY in the environment handed to inline scripts and data-source
// scripts (both go through buildScriptEnv). A neighbouring benign header must
// still survive, so we know the drop is surgical rather than a broken loop.
func TestScriptEnvDropsProxyHeader(t *testing.T) {
	r := httptest.NewRequest("GET", "/page", nil)
	r.Header.Set("Proxy", "http://attacker.example:8080") // httpoxy vector
	r.Header.Set("X-Custom", "keep-me")

	ctx := &renderContext{
		docRoot:     t.TempDir(),
		requestURI:  "/page",
		httpRequest: r,
		defs:        make(map[string]interface{}),
		filesLoaded: make(map[string]bool),
	}
	env := ctx.buildScriptEnv("")

	var sawCustom bool
	for _, e := range env {
		if strings.HasPrefix(e, "HTTP_PROXY=") {
			t.Errorf("Proxy header leaked into subprocess env: %q", e)
		}
		if e == "HTTP_X_CUSTOM=keep-me" {
			sawCustom = true
		}
	}
	if !sawCustom {
		t.Errorf("benign header was not forwarded; env=%v", env)
	}
}

// TestPathProxyStreamOutlivesWriteTimeout is the streaming test for the second
// proxy path: a vhost with `proxy-path` fronting a backend that streams for
// ~4x the server WriteTimeout. Without the rolling-deadline treatment in
// streamProxy the connection dies at the absolute deadline mid-stream.
func TestPathProxyStreamOutlivesWriteTimeout(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Error("backend writer is not a Flusher")
			return
		}
		for i := 0; i < 20; i++ {
			fmt.Fprintf(w, "{\"tick\":%d}\n", i)
			fl.Flush()
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Fprintln(w, `{"done":true}`)
	}))
	defer backend.Close()

	base := t.TempDir()
	vhost := filepath.Join(base, "pathproxy.test")
	if err := os.MkdirAll(vhost, 0o755); err != nil {
		t.Fatal(err)
	}
	// proxy-path-allow-private opts out of the SSRF guard for the loopback
	// backend the test spins up.
	cfg := "proxy-path: /stream/\n" +
		"proxy-path-backend: " + backend.Listener.Addr().String() + "\n" +
		"proxy-path-allow-private: true\n"
	if err := os.WriteFile(filepath.Join(vhost, "_config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := newTestMux(t, base)
	srv := &http.Server{
		Handler:      loggingMiddleware(mux),
		WriteTimeout: 500 * time.Millisecond, // production's 120s, scaled down
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	defer srv.Close()

	req, err := http.NewRequest("GET", "http://"+ln.Addr().String()+"/stream/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "pathproxy.test"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("stream died after %d bytes: %v", len(body), err)
	}
	if !strings.Contains(string(body), `"done":true`) {
		t.Errorf("stream cut short: got %d bytes without the final line: %q",
			len(body), truncateForLog(string(body)))
	}
	if !strings.Contains(string(body), `{"tick":0}`) {
		t.Errorf("first heartbeat missing from response: %q", truncateForLog(string(body)))
	}
}
