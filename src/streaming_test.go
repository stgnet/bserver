package main

// Streaming responses must survive the server's absolute WriteTimeout.
//
// Production symptom (2026-08-11): any proxied response that streamed for
// longer than the server's 120s WriteTimeout was reset mid-stream (h2
// INTERNAL_ERROR seen by the downstream proxy at exactly 2m00s), and none
// of the backend's flushed heartbeat lines ever reached the wire.  Root
// cause is twofold and both halves are covered here:
//
//  1. loggingResponseWriter wraps the real http.ResponseWriter without an
//     Unwrap method, so http.ResponseController - which httputil's reverse
//     proxy and the streaming-php path both rely on - cannot reach the
//     underlying Flush/SetWriteDeadline.  Every flush and deadline
//     extension is silently dropped.
//  2. The proxy path never clears the write deadline, so even with
//     working flushes a long stream dies at the absolute WriteTimeout.

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

// TestResponseControllerThroughLoggingWriter proves the logging middleware
// wrapper forwards Flush and SetWriteDeadline to the underlying writer.
// Without loggingResponseWriter.Unwrap, both calls return errors and
// streaming silently degrades to fully-buffered responses.
func TestResponseControllerThroughLoggingWriter(t *testing.T) {
	type result struct{ flush, deadline error }
	got := make(chan result, 1)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		got <- result{rc.Flush(), rc.SetWriteDeadline(time.Now().Add(time.Minute))}
	})
	srv := httptest.NewServer(loggingMiddleware(inner))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	r := <-got
	if r.flush != nil {
		t.Errorf("Flush through loggingResponseWriter failed: %v", r.flush)
	}
	if r.deadline != nil {
		t.Errorf("SetWriteDeadline through loggingResponseWriter failed: %v", r.deadline)
	}
}

// TestProxyStreamOutlivesWriteTimeout runs the real serving stack - proxy
// vhost resolved from an index.yaml, wrapped in loggingMiddleware, behind
// an http.Server with a WriteTimeout - against a backend that streams
// heartbeat lines for ~4x the timeout.  The full stream must arrive: the
// proxy path has to clear the absolute deadline and each flushed line has
// to reach the client.  This is the ollama/imagen channel in miniature.
func TestProxyStreamOutlivesWriteTimeout(t *testing.T) {
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
	vhost := filepath.Join(base, "proxy.test")
	if err := os.MkdirAll(vhost, 0o755); err != nil {
		t.Fatal(err)
	}
	// allow-private opts out of the SSRF guard, which would otherwise
	// refuse the loopback backend the test spins up.
	yaml := "http: " + backend.Listener.Addr().String() + "\nallow-private: true\n"
	if err := os.WriteFile(filepath.Join(vhost, "index.yaml"), []byte(yaml), 0o644); err != nil {
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

	req, err := http.NewRequest("GET", "http://"+ln.Addr().String()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "proxy.test"
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

func truncateForLog(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
