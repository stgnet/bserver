package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// Version is the build version of bserver. Override at build time with:
//
//	go build -ldflags "-X main.Version=1.2.3"
var Version = "dev"

// config holds runtime configuration.
type config struct {
	Base        string // web content root (www directory)
	HTTPAddr    string
	HTTPSAddr   string
	CacheDir    string
	LEEmail     string
	PHPCGI      string // path to php-cgi
	Cache       *renderCache
	MaxBodySize int64        // max request body size in bytes (0 = unlimited)
	DebugToken  string       // if non-empty, ?debug=<token> is required to enable debug output
	Site        siteSettings // server-wide defaults (per-vhost _config.yaml can override)
}

// debugEnabled reports whether debug output should be emitted for this
// request. When DebugToken is non-empty, the query parameter must match it
// exactly via constant-time compare. When DebugToken is empty, the bare
// ?debug parameter is honored (legacy / dev mode).
func (cfg *config) debugEnabled(r *http.Request) bool {
	q := r.URL.Query()
	vals, ok := q["debug"]
	if !ok {
		return false
	}
	if cfg.DebugToken == "" {
		return true
	}
	for _, v := range vals {
		if subtle.ConstantTimeCompare([]byte(v), []byte(cfg.DebugToken)) == 1 {
			return true
		}
	}
	return false
}

// virtualHostMux dynamically serves based on cwd directories.
type virtualHostMux struct {
	cfg *config
	rl  *rateLimiter // may be nil in tests
	sync.Mutex
}

// proxyEntry caches a reverse proxy for a vhost whose index.yaml defines
// an http: backend target.
type proxyEntry struct {
	proxy      *httputil.ReverseProxy // nil if not a proxy vhost or setup failed
	apiKey     string                 // if set, requires Authorization: Bearer <key>
	setupError string                 // non-empty if vhost intended-as-proxy but setup failed
	modTime    time.Time              // mtime of index.yaml (zero if absent)
}

var proxyCache sync.Map // docRoot -> *proxyEntry

func proxyCacheSize() int {
	n := 0
	proxyCache.Range(func(_, _ any) bool { n++; return true })
	return n
}

// getProxyForVhost checks whether the vhost at docRoot is a proxy vhost
// by reading its index.yaml for an "http:" key. Results are cached with
// mtime-based invalidation.
//
// Returns nil if the vhost is not configured as a proxy (no http: key).
// Returns an entry with setupError set when the vhost intends to proxy
// but configuration is rejected (invalid URL, SSRF guard). Returns an
// entry with proxy set on success.
func getProxyForVhost(docRoot string, vmux *virtualHostMux) *proxyEntry {
	indexPath := filepath.Join(docRoot, "index.yaml")

	var currentMtime time.Time
	info, err := os.Stat(indexPath)
	if err != nil {
		// No index.yaml — not a proxy vhost; cache the absence.
		if cached, ok := proxyCache.Load(docRoot); ok {
			entry := cached.(*proxyEntry)
			if entry.modTime.IsZero() {
				return nil
			}
		}
		proxyCache.Store(docRoot, &proxyEntry{modTime: time.Time{}})
		return nil
	}
	currentMtime = info.ModTime()

	// Return cached entry if mtime matches
	if cached, ok := proxyCache.Load(docRoot); ok {
		entry := cached.(*proxyEntry)
		if entry.modTime.Equal(currentMtime) {
			if entry.proxy == nil && entry.setupError == "" {
				return nil
			}
			return entry
		}
	}

	// Read and parse index.yaml
	idx := loadConfigMap(indexPath)
	backend, ok := configString(idx, "http", "")
	if !ok || backend == "" {
		proxyCache.Store(docRoot, &proxyEntry{modTime: currentMtime})
		return nil
	}

	// Ensure the backend has a scheme
	if !strings.Contains(backend, "://") {
		backend = "http://" + backend
	}

	target, err := url.Parse(backend)
	if err != nil {
		msg := fmt.Sprintf("invalid proxy backend %q: %v", backend, err)
		log.Printf("Warning: %s in %s", msg, indexPath)
		entry := &proxyEntry{modTime: currentMtime, setupError: msg}
		proxyCache.Store(docRoot, entry)
		return entry
	}

	// SSRF guard: refuse to proxy to loopback / link-local / private /
	// unspecified / multicast addresses. An attacker who can write a vhost
	// index.yaml could otherwise turn bserver into an SSRF gateway to
	// cloud metadata services or internal-only services. Operators that
	// genuinely want to proxy to a private backend can opt in via
	// `allow-private: true`.
	allowPrivate, _ := configBool(idx, "allow-private", false)
	if !allowPrivate {
		if reason := unsafeProxyTarget(target); reason != "" {
			msg := fmt.Sprintf("refusing proxy backend %q: %s", backend, reason)
			log.Printf("Warning: %s in %s", msg, indexPath)
			entry := &proxyEntry{modTime: currentMtime, setupError: msg}
			proxyCache.Store(docRoot, entry)
			return entry
		}
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	defaultDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		defaultDirector(r)
		r.Host = target.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy error for %s -> %s: %v", r.Host, target, err)
		site := vhostSettings(docRoot, vmux.cfg.Site)
		msg := fmt.Sprintf("Backend service at %s is not responding: %v", target.Host, err)
		vmux.serveErrorPage(w, r, docRoot, http.StatusBadGateway, msg, site, false)
	}

	apiKey, _ := configString(idx, "api-key", "")
	log.Printf("Proxy vhost %s -> %s", docRoot, target)
	entry := &proxyEntry{proxy: proxy, apiKey: apiKey, modTime: currentMtime}
	proxyCache.Store(docRoot, entry)
	return entry
}

// pathUnderPrefix reports whether upath is the prefix itself or nested
// under it, e.g. "/terminal" or "/terminal/ws" for prefix "/terminal/".
func pathUnderPrefix(upath, prefix string) bool {
	prefix = "/" + strings.Trim(prefix, "/")
	return upath == prefix || strings.HasPrefix(upath, prefix+"/")
}

// anyCookieMatches reports whether any cookie of the given name matches the
// value. Checking all (not just the first via r.Cookie) matters when a
// client holds a stale duplicate — e.g. a host-only cookie plus an older
// domain-scoped one of the same name; r.Cookie would return only the first.
func anyCookieMatches(r *http.Request, name, value string) bool {
	for _, c := range r.Cookies() {
		if c.Name == name && subtle.ConstantTimeCompare([]byte(c.Value), []byte(value)) == 1 {
			return true
		}
	}
	return false
}

var pathProxyCache sync.Map // backend string -> *httputil.ReverseProxy

// getPathProxy returns a cached reverse proxy for a host:port backend used
// by a vhost's ProxyPath. Websocket upgrades are handled by the standard
// library's ReverseProxy.
//
// Like the vhost `http:` proxy, the backend is refused if it resolves to a
// loopback/link-local/private/etc. address, so an attacker who can write a
// vhost `_config.yaml` cannot turn a path proxy into an SSRF gateway. Path
// proxies commonly front a local companion service (e.g. a web terminal on
// localhost), so operators opt in to private backends with
// `proxy-path-allow-private: true`.
func getPathProxy(backend string, allowPrivate bool) *httputil.ReverseProxy {
	if backend == "" {
		return nil
	}
	if p, ok := pathProxyCache.Load(backend); ok {
		return p.(*httputil.ReverseProxy)
	}
	b := backend
	if !strings.Contains(b, "://") {
		b = "http://" + b
	}
	target, err := url.Parse(b)
	if err != nil {
		log.Printf("invalid proxy-path-backend %q: %v", backend, err)
		return nil
	}
	if !allowPrivate {
		if reason := unsafeProxyTarget(target); reason != "" {
			log.Printf("refusing proxy-path-backend %q: %s (set proxy-path-allow-private: true to allow)", backend, reason)
			return nil
		}
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	defaultDirector := rp.Director
	rp.Director = func(r *http.Request) {
		defaultDirector(r)
		r.Host = target.Host
	}
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("path-proxy error -> %s: %v", target, err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}
	pathProxyCache.Store(backend, rp)
	return rp
}

// proxyStreamIdle bounds how long a single write to a proxied client may
// block before the connection is torn down. It is applied as a *rolling*
// deadline (reset on every write by streamDeadlineWriter), so a proxied
// response that streams for any total duration survives, while a client that
// stops reading is cut off after this much idle time — reclaiming the
// goroutine, connection, and kernel/socket buffers it was pinning. It stands
// in for the server's absolute WriteTimeout, which cannot tell an
// hours-long-but-healthy stream from a wedged one.
const proxyStreamIdle = 120 * time.Second

// streamDeadlineWriter wraps a ResponseWriter and pushes the connection's
// write deadline forward on every write, converting the server's single
// absolute WriteTimeout into a rolling idle timeout suitable for long-lived
// proxied streams (ollama, imagen, SSE, ndjson). Refreshing the deadline
// immediately before each write is what makes a slow backend safe: the gap
// between chunks is not bounded (no write is in flight), only the duration of
// an individual blocked write is. Unwrap exposes the underlying writer so
// http.ResponseController (used by httputil.ReverseProxy to flush) and
// http.Flusher reach the real connection through this wrapper.
type streamDeadlineWriter struct {
	http.ResponseWriter
	rc   *http.ResponseController
	idle time.Duration
}

func (sw *streamDeadlineWriter) Write(p []byte) (int, error) {
	_ = sw.rc.SetWriteDeadline(time.Now().Add(sw.idle))
	return sw.ResponseWriter.Write(p)
}

func (sw *streamDeadlineWriter) WriteHeader(code int) {
	_ = sw.rc.SetWriteDeadline(time.Now().Add(sw.idle))
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *streamDeadlineWriter) Unwrap() http.ResponseWriter {
	return sw.ResponseWriter
}

// streamProxy serves a reverse proxy through a rolling write deadline so a
// stream that outlives the server's absolute WriteTimeout is not cut off,
// without leaving stalled clients unbounded. Both proxy paths (vhost `http:`
// and per-vhost `proxy-path:`) funnel through here so the streaming fix and
// the resource bound stay in one place.
func streamProxy(w http.ResponseWriter, r *http.Request, proxy *httputil.ReverseProxy) {
	rc := http.NewResponseController(w)
	// Arm the deadline once up front and check the error a single time: if the
	// writer chain can't carry a deadline (a wrapper missing Unwrap), streaming
	// still works but is bounded by the absolute WriteTimeout — log it once so
	// the regression is visible instead of silently degrading long streams.
	if err := rc.SetWriteDeadline(time.Now().Add(proxyStreamIdle)); err != nil {
		log.Printf("proxy stream for %s: write deadline unsupported (%v); long streams may be cut at WriteTimeout", r.Host, err)
	}
	proxy.ServeHTTP(&streamDeadlineWriter{ResponseWriter: w, rc: rc, idle: proxyStreamIdle}, r)
}

func (m *virtualHostMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(host)

	// Reject Host headers containing path separators or traversal sequences
	// to prevent filesystem path manipulation (e.g., Host: ../../etc).
	if strings.ContainsAny(host, "/\\") || strings.Contains(host, "..") {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	root := filepath.Join(m.cfg.Base, host)
	hostFallback := false
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		// Base domains (single dot, e.g. "b-haven.net") and IP addresses
		// fall through to default so that any domain pointed at this server
		// gets content. Subdomains (multiple dots) are only allowed if they
		// match a known vhost, to reject bogus deeply-nested scanner domains.
		isIP := net.ParseIP(host) != nil
		if !isIP && strings.Count(host, ".") > 1 && !isKnownVhost(host, m.cfg.Base) {
			http.Error(w, "Misdirected Request", http.StatusMisdirectedRequest)
			return
		}
		defaultRoot := filepath.Join(m.cfg.Base, "default")
		if st, err := os.Stat(defaultRoot); err != nil || !st.IsDir() {
			log.Printf("404: host %q not found, default also unavailable (base=%s)", host, m.cfg.Base)
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		root = defaultRoot
		hostFallback = true
	}

	// Resolve per-vhost settings (cached, mtime-invalidated)
	site := vhostSettings(root, m.cfg.Site)

	// Per-vhost IP allowlist: if configured, only listed client IPs may reach
	// this vhost at all — pages, static files, and proxied paths alike. Checked
	// before proxy handling so a gated backend (e.g. a /terminal/ web shell) is
	// covered too.
	if len(site.AllowIPs) > 0 && !ipAllowed(clientIP(r), site.AllowIPs) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Per-vhost passwordless auth: every _auth.yaml on the vhost is an auth
	// realm. One in the docroot (or the legacy auth-* keys in _config.yaml)
	// gates the whole vhost; one in a direct subdirectory gates just that
	// subtree, with its own owner, approved users and session cookie. The most
	// specific realm governs a path. Everything a realm covers, outside its
	// always-public set (splash, /auth/*, assets), requires that realm's valid
	// session cookie, obtained via a one-time code mailed to an approved
	// address. Checked before proxy handling so a gated backend (e.g. the
	// /terminal/ web shell) is covered too. See auth.go.
	if len(site.AuthRealms) > 0 {
		p := path.Clean("/" + r.URL.Path)
		if strings.HasPrefix(p, "/auth/") {
			if rs := authLoginRealm(r, site); rs != nil {
				serveAuth(w, r, *rs, p)
			} else {
				http.NotFound(w, r) // every realm broken; nothing to sign in to
			}
			return
		}
		if realm := authRealmFor(p, site); realm != nil {
			if realm.broken {
				// A present-but-unusable _auth.yaml fails closed: refusing its
				// subtree is recoverable, serving it open is not.
				m.serveErrorPage(w, r, root, http.StatusNotFound, "", site, hostFallback)
				return
			}
			authUser, signedIn := validAuthCookie(r, realm.settings)
			if !authPublic(p, realm.settings) && !signedIn {
				if r.Method == http.MethodGet || r.Method == http.MethodHead {
					next := r.URL.Path
					if r.URL.RawQuery != "" {
						next += "?" + r.URL.RawQuery
					}
					http.Redirect(w, r, "/auth/login?next="+urlQueryEscape(next), http.StatusSeeOther)
				} else {
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
				}
				return
			}
			// Carry the signed-in address down to the CGI layer, which exports
			// it as REMOTE_USER so a PHP app can tell its users apart. Set only
			// when a valid cookie was actually presented, so a public page never
			// sees a stale or spoofed identity.
			if signedIn {
				r = r.WithContext(context.WithValue(r.Context(), authUserKey{}, authUser))
			}
		}
	}

	// Check if this vhost is a proxy (index.yaml with http: backend)
	if entry := getProxyForVhost(root, m); entry != nil {
		if entry.proxy != nil {
			if entry.apiKey != "" {
				// Accept the key either as a Bearer Authorization header
				// (API clients) or as a bs_proxy_auth cookie. The cookie
				// lets a same-site page that has already authenticated the
				// user (e.g. a Google-gated PHP page) grant browser access
				// to the proxied backend — including websockets, which
				// cannot carry a custom Authorization header — without
				// prompting the user for a separate login.
				ok := subtle.ConstantTimeCompare(
					[]byte(r.Header.Get("Authorization")),
					[]byte("Bearer "+entry.apiKey)) == 1
				if !ok {
					ok = anyCookieMatches(r, "bs_proxy_auth", entry.apiKey)
				}
				if !ok {
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}
				r.Header.Del("Authorization")
			}
			// Proxied backends (ollama, imagen) stream responses that can run
			// for many minutes; the server's absolute WriteTimeout would cut
			// them off mid-stream. Serve through a rolling write deadline so an
			// active stream survives indefinitely while a client that stops
			// reading is still reaped (see streamProxy).
			streamProxy(w, r, entry.proxy)
			return
		}
		// vhost intended as a proxy but setup was rejected — surface a
		// recognizable 502 instead of falling through to YAML rendering,
		// which would just render the bare index.yaml (e.g., a blank page).
		log.Printf("proxy unavailable for %s: %s", r.Host, entry.setupError)
		m.serveErrorPage(w, r, root, http.StatusBadGateway,
			"Proxy backend unavailable: "+entry.setupError, site, hostFallback)
		return
	}

	upath := path.Clean("/" + r.URL.Path)

	// Fail closed on any _auth.yaml the gate above is not operating: one
	// nested deeper than a direct subdirectory, one whose realm is broken, or
	// a path reaching the same directory under a different spelling (a
	// case-insensitive or Unicode-normalizing filesystem). Placed before all
	// serving paths — path proxy, path-args scripts, static files, rendering
	// — so nothing downstream can leak a subtree its _auth.yaml meant to gate.
	if authSubtreeDenied(root, upath, site) {
		log.Printf("auth: refusing %s%s: _auth.yaml present on this subtree but not operating", host, upath)
		m.serveErrorPage(w, r, root, http.StatusNotFound, "", site, hostFallback)
		return
	}

	// Path-based reverse proxy (e.g. a web terminal at /terminal/): serve a
	// backend under this vhost's own path and cert, no separate proxy vhost.
	// Gated by a bs_proxy_auth cookie (or Bearer key) so a same-origin
	// authenticated page — e.g. a Google-gated PHP page that sets the
	// cookie — can grant browser + websocket access without a second login.
	if site.ProxyPath != "" && pathUnderPrefix(upath, site.ProxyPath) {
		if site.ProxyKey != "" {
			ok := anyCookieMatches(r, "bs_proxy_auth", site.ProxyKey)
			if !ok {
				ok = subtle.ConstantTimeCompare(
					[]byte(r.Header.Get("Authorization")),
					[]byte("Bearer "+site.ProxyKey)) == 1
			}
			if !ok {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			r.Header.Del("Authorization")
		}
		// Per-user restriction on the proxied path. The auth gate above has
		// already established who this is; here we ask whether that person is on
		// the shorter list allowed to reach the backend. The app cannot enforce
		// this itself — nothing it serves sits between the visitor and the proxy.
		if site.ProxyPathUsers != "" {
			u := authUserFrom(r)
			if u == "" || !loadAuthUsers(site.ProxyPathUsers)[u] {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
		}
		if rp := getPathProxy(site.ProxyBackend, site.ProxyAllowPrivate); rp != nil {
			// Same rolling-deadline treatment as the vhost-level proxy above:
			// a path-proxied stream (SSE/ndjson) must also outlive the server's
			// absolute WriteTimeout without leaving stalled clients unbounded.
			streamProxy(w, r, rp)
		} else {
			m.serveErrorPage(w, r, root, http.StatusBadGateway, "proxy backend misconfigured", site, hostFallback)
		}
		return
	}

	// Block requests for hidden files/directories (/.git/*, /.env, ...) and
	// vendor trees by default. Without this guard the allowed-types check is
	// skipped for extensionless files like .git/index and .git/HEAD, letting
	// an attacker fetch version-control metadata and reconstruct the repo.
	// .well-known is exempt; operators can adjust via the allow-paths and
	// block-paths keys in _config.yaml.
	if site.pathBlocked(upath) {
		// A blocked path may still be a path-args URL whose *argument*
		// segments merely look like hidden files (e.g. /verify/token/_abc);
		// tryPathArgs re-checks pathBlocked on the script prefix itself.
		if m.tryPathArgs(w, r, host, root, upath, site) {
			return
		}
		m.serveErrorPage(w, r, root, http.StatusNotFound, "", site, hostFallback)
		return
	}

	// Favicon: generate from _favicon.yaml (or defaults) when no real file exists
	if upath == "/favicon.ico" {
		icoPath := filepath.Join(root, "favicon.ico")
		if st, err := os.Stat(icoPath); err == nil && !st.IsDir() {
			w.Header().Set("Content-Type", "image/x-icon")
			if cc := staticFileCacheControl(st.ModTime(), site.StaticAge); cc != "" {
				w.Header().Set("Cache-Control", cc)
			}
			http.ServeFile(w, r, icoPath)
			return
		}
		m.serveFavicon(w, r, root, site)
		return
	}

	fsPath := filepath.Join(root, filepath.FromSlash(upath))

	info, err := os.Stat(fsPath)
	if err == nil && info.IsDir() {
		found := false
		// First look for index files (index.yaml, index.md, etc.)
		for _, idx := range site.Index {
			cand := filepath.Join(fsPath, idx)
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				fsPath = cand
				found = true
				break
			}
		}
		// If no index file, look for name.* files (e.g., bhaven/bhaven.yaml)
		if !found {
			dirName := filepath.Base(fsPath)
			for _, idx := range site.Index {
				ext := filepath.Ext(idx)
				cand := filepath.Join(fsPath, dirName+ext)
				if st, err := os.Stat(cand); err == nil && !st.IsDir() {
					fsPath = cand
					break
				}
			}
		}
	}

	// Block file types not in the allowed types list
	if ext := filepath.Ext(fsPath); ext != "" {
		if !isAllowedType(ext, site.Types) {
			// The "extension" may just be a dot inside a path-args value
			// (e.g. /confirm/email/user@example.com).
			if m.tryPathArgs(w, r, host, root, upath, site) {
				return
			}
			m.serveErrorPage(w, r, root, http.StatusNotFound, "", site, hostFallback)
			return
		}
	}

	if strings.HasSuffix(strings.ToLower(fsPath), ".php") {
		if st, err := os.Stat(fsPath); err == nil && !st.IsDir() {
			m.handlePHP(w, r, host, root, fsPath, site)
			return
		}
		// PHP file doesn't exist — fall through to 404
	}

	// YAML rendering: if the resolved path is a .yaml file, render it as HTML
	if strings.HasSuffix(strings.ToLower(fsPath), ".yaml") {
		if st, err := os.Stat(fsPath); err == nil && !st.IsDir() {
			// raw-yaml allowlist: serve the file bytes untouched (data
			// lists like packageslist.yaml fetched by other systems).
			// no-cache keeps daily-regenerated feeds fresh; ServeFile
			// still answers If-Modified-Since with 304.
			if site.rawYAMLAllowed(upath) {
				w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache")
				http.ServeFile(w, r, fsPath)
				return
			}
			m.handleYAML(w, r, root, fsPath, site)
			return
		}
	}

	// Markdown rendering: if the resolved path is a .md file, render it within the YAML page structure
	if strings.HasSuffix(strings.ToLower(fsPath), ".md") {
		if st, err := os.Stat(fsPath); err == nil && !st.IsDir() {
			m.handleMarkdown(w, r, root, fsPath, site)
			return
		}
	}

	if st, err := os.Stat(fsPath); err == nil && !st.IsDir() {
		if ctype := mime.TypeByExtension(filepath.Ext(fsPath)); ctype != "" {
			w.Header().Set("Content-Type", ctype)
		}
		// Set Cache-Control for static files based on file age
		if cc := staticFileCacheControl(st.ModTime(), site.StaticAge); cc != "" {
			w.Header().Set("Cache-Control", cc)
		}
		http.ServeFile(w, r, fsPath)
		return
	}

	// If the path has no extension and doesn't exist as a file, try:
	// 1. A subdirectory with the same name (directory takes precedence)
	// 2. A sibling .yaml or .md file
	if filepath.Ext(fsPath) == "" {
		// Check for a subdirectory first (reuse stat result from above)
		if info != nil && info.IsDir() {
			// Directory exists but no index/name file was found above;
			// this was already handled in the directory block, so fall through
		} else {
			// No directory — try sibling .yaml, .md, then .php
			yamlPath := fsPath + ".yaml"
			if st, err := os.Stat(yamlPath); err == nil && !st.IsDir() {
				m.handleYAML(w, r, root, yamlPath, site)
				return
			}
			mdPath := fsPath + ".md"
			if st, err := os.Stat(mdPath); err == nil && !st.IsDir() {
				m.handleMarkdown(w, r, root, mdPath, site)
				return
			}
			phpPath := fsPath + ".php"
			if st, err := os.Stat(phpPath); err == nil && !st.IsDir() {
				m.handlePHP(w, r, host, root, phpPath, site)
				return
			}
		}
	}

	if info != nil && info.IsDir() {
		indexPHP := filepath.Join(fsPath, "index.php")
		if st, err := os.Stat(indexPHP); err == nil && !st.IsDir() {
			m.handlePHP(w, r, host, root, indexPHP, site)
			return
		}
	}

	// Fallback: if the requested file has an extension and doesn't exist,
	// try replacing the extension with .yaml or .md
	if ext := filepath.Ext(fsPath); ext != "" {
		base := strings.TrimSuffix(fsPath, ext)
		yamlPath := base + ".yaml"
		if st, err := os.Stat(yamlPath); err == nil && !st.IsDir() {
			m.handleYAML(w, r, root, yamlPath, site)
			return
		}
		mdPath := base + ".md"
		if st, err := os.Stat(mdPath); err == nil && !st.IsDir() {
			m.handleMarkdown(w, r, root, mdPath, site)
			return
		}
	}

	// Last resort: interpret the URL as /script/name/value... path args
	if m.tryPathArgs(w, r, host, root, upath, site) {
		return
	}

	m.serveErrorPage(w, r, root, http.StatusNotFound, "", site, hostFallback)
}

// pathArgScriptExts lists the script types, in sibling-lookup priority
// order, that can receive path-embedded GET arguments. Static types are
// deliberately excluded: an argument-bearing URL must resolve to something
// that generates page output, and matching static files would silently
// alias unlimited URLs onto one document.
var pathArgScriptExts = []string{".yaml", ".md", ".php"}

// isPathArgScript reports whether p is a page-generating script type that
// is also permitted by the site's allowed-types list.
func isPathArgScript(p string, site siteSettings) bool {
	ext := strings.ToLower(filepath.Ext(p))
	for _, e := range pathArgScriptExts {
		if ext == e {
			return isAllowedType(ext, site.Types)
		}
	}
	return false
}

// resolvePathArgScript resolves a candidate path-args script prefix the
// same way a direct request for it would resolve: an existing script file
// (explicit /script.php/name/value), a directory's index or name file, or
// an extensionless sibling script. Returns "" when the prefix resolves to
// nothing — or to a non-script (static) file, which is not eligible to
// receive arguments.
func resolvePathArgScript(fsBase string, site siteSettings) string {
	if st, err := os.Stat(fsBase); err == nil {
		if !st.IsDir() {
			if isPathArgScript(fsBase, site) {
				return fsBase
			}
			return ""
		}
		// Directory: mirror the index-file resolution of a direct request.
		// The first index that exists decides — if it is a static type, the
		// direct request would serve static content, so no args here either.
		for _, idx := range site.Index {
			cand := filepath.Join(fsBase, idx)
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				if isPathArgScript(cand, site) {
					return cand
				}
				return ""
			}
		}
		dirName := filepath.Base(fsBase)
		for _, idx := range site.Index {
			cand := filepath.Join(fsBase, dirName+filepath.Ext(idx))
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				if isPathArgScript(cand, site) {
					return cand
				}
				return ""
			}
		}
		return ""
	}
	if filepath.Ext(fsBase) != "" {
		return ""
	}
	// Extensionless sibling lookup, same order as the main handler.
	for _, ext := range pathArgScriptExts {
		cand := fsBase + ext
		if st, err := os.Stat(cand); err == nil && !st.IsDir() && isAllowedType(ext, site.Types) {
			return cand
		}
	}
	return ""
}

// pathArgsQuery encodes path segments taken pairwise as a query string:
// [a 1 b 2] -> "a=1&b=2". An odd trailing segment becomes a bare
// parameter with no value, matching how "?name" (no "=") arrives.
func pathArgsQuery(segs []string) string {
	var qb strings.Builder
	for j := 0; j < len(segs); j += 2 {
		if qb.Len() > 0 {
			qb.WriteByte('&')
		}
		qb.WriteString(url.QueryEscape(segs[j]))
		if j+1 < len(segs) {
			qb.WriteByte('=')
			qb.WriteString(url.QueryEscape(segs[j+1]))
		}
	}
	return qb.String()
}

// tryPathArgs attempts to serve an otherwise-unresolved request whose URL
// embeds GET arguments as trailing path segments:
//
//	/script/name/value[/name2/value2...]  ≡  /script?name=value&name2=value2
//
// Some email security scanners and link rewriters mangle or refuse to
// forward ?name=value query strings; this URL form survives them. No
// configuration is required: the longest leading prefix that resolves to a
// page-generating script (.yaml, .md, or .php) wins, and the remaining
// segments are folded into r.URL.RawQuery exactly as a ?query would
// arrive, appended after any real query already present. The parameters
// are passed blindly — a script that ignores them still renders normally.
// Because the merged query is non-empty, the render cache already treats
// these requests as dynamic and never caches them.
//
// Returns true if the request was handled (rendered or delegated),
// false if no prefix resolves to a script and normal 404 handling
// should proceed.
func (m *virtualHostMux) tryPathArgs(w http.ResponseWriter, r *http.Request, host, root, upath string, site siteSettings) bool {
	segs := splitPath(upath)
	if len(segs) < 2 {
		return false
	}
	for i := len(segs) - 1; i >= 1; i-- {
		prefix := "/" + strings.Join(segs[:i], "/")
		// The script itself must pass the path-block rules even when the
		// full URL was let through (and vice versa: blocked arg values
		// don't disqualify an allowed script).
		if site.pathBlocked(prefix) {
			continue
		}
		script := resolvePathArgScript(filepath.Join(root, filepath.FromSlash(prefix)), site)
		if script == "" {
			continue
		}
		q := pathArgsQuery(segs[i:])
		if r.URL.RawQuery != "" {
			r.URL.RawQuery += "&" + q
		} else {
			r.URL.RawQuery = q
		}
		switch strings.ToLower(filepath.Ext(script)) {
		case ".php":
			m.handlePHP(w, r, host, root, script, site)
		case ".yaml":
			m.handleYAML(w, r, root, script, site)
		default: // ".md"
			m.handleMarkdown(w, r, root, script, site)
		}
		return true
	}
	return false
}

func (m *virtualHostMux) handlePHP(w http.ResponseWriter, r *http.Request, host, docroot, scriptFilename string, site siteSettings) {
	// Compute SCRIPT_NAME and PATH_INFO from the URL
	scriptName := r.URL.Path
	pathInfo := ""
	if i := strings.Index(strings.ToLower(scriptName), ".php"); i != -1 {
		pathInfo = scriptName[i+4:]
		scriptName = scriptName[:i+4]
	} else {
		// URL has no .php (e.g. "/" resolved to index.php) — derive
		// SCRIPT_NAME from the actual script path relative to docroot.
		if rel, err := filepath.Rel(docroot, scriptFilename); err == nil {
			scriptName = "/" + filepath.ToSlash(rel)
		}
	}

	// Determine server port and scheme
	port := "80"
	scheme := "http"
	if r.TLS != nil {
		port = "443"
		scheme = "https"
	}
	remoteAddr := r.RemoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		remoteAddr = h
	}

	// Build standard CGI environment
	env := []string{
		"REDIRECT_STATUS=200",
		"GATEWAY_INTERFACE=CGI/1.1",
		"SERVER_SOFTWARE=bserver",
		"SERVER_PROTOCOL=" + r.Proto,
		"SERVER_NAME=" + host,
		"SERVER_PORT=" + port,
		"REQUEST_SCHEME=" + scheme,
		"REQUEST_METHOD=" + r.Method,
		"REQUEST_URI=" + r.URL.RequestURI(),
		"QUERY_STRING=" + r.URL.RawQuery,
		"DOCUMENT_ROOT=" + docroot,
		"SCRIPT_FILENAME=" + scriptFilename,
		"SCRIPT_NAME=" + scriptName,
		"PATH_INFO=" + pathInfo,
		"REMOTE_ADDR=" + remoteAddr,
	}

	// Identity of the signed-in visitor, for apps that support more than one
	// user. REMOTE_USER is the conventional CGI variable for this and reaches
	// PHP as $_SERVER['REMOTE_USER']. It is set only when the auth gate verified
	// a cookie on this request, and — because it is assembled here rather than
	// copied from a header — a client cannot forge it by sending its own.
	if u := authUserFrom(r); u != "" {
		env = append(env, "REMOTE_USER="+u)
	}

	// Forward HTTP request headers as HTTP_* env vars.
	// Go strips Host from r.Header and puts it in r.Host, so add it explicitly.
	env = append(env, "HTTP_HOST="+r.Host)
	for key, vals := range r.Header {
		if !forwardableAsHTTPVar(key) {
			continue
		}
		envKey := "HTTP_" + strings.ReplaceAll(strings.ToUpper(key), "-", "_")
		env = append(env, envKey+"="+strings.Join(vals, ", "))
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		env = append(env, "CONTENT_TYPE="+ct)
	}

	// Buffer the request body so CONTENT_LENGTH is always accurate.
	// r.Body may be a streaming reader and the header-based ContentLength
	// may be -1 (unknown/chunked), which would prevent php-cgi from
	// reading POST data.
	var bodyBuf bytes.Buffer
	if r.Body != nil {
		io.Copy(&bodyBuf, r.Body)
		r.Body.Close()
	}
	if bodyBuf.Len() > 0 {
		env = append(env, fmt.Sprintf("CONTENT_LENGTH=%d", bodyBuf.Len()))
	} else if r.ContentLength >= 0 {
		env = append(env, fmt.Sprintf("CONTENT_LENGTH=%d", r.ContentLength))
	}

	// Inherit PATH so php-cgi can find shared libraries etc.
	if p := os.Getenv("PATH"); p != "" {
		env = append(env, "PATH="+p)
	}

	phpTimeout := site.PHPTimeout
	if phpTimeout <= 0 {
		phpTimeout = 60 * time.Second
	}
	streamAfter := site.PHPStreamAfter
	if streamAfter < 0 {
		streamAfter = 0
	}

	// Pipe php-cgi stdout through os.Pipe so we can SetReadDeadline on the
	// reader (cmd.StdoutPipe gives an io.ReadCloser that doesn't expose it).
	pr, pw, err := os.Pipe()
	if err != nil {
		log.Printf("php-cgi pipe error for %s: %v", scriptFilename, err)
		http.Error(w, "CGI Error", http.StatusInternalServerError)
		return
	}
	defer pr.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel() // kills php-cgi on return

	// -d output_buffering=0 -d implicit_flush=1 makes every echo flush to
	// stdout immediately so the grace-period buffering and subsequent
	// streaming reflect script progress rather than SAPI-level buffering.
	cmd := exec.CommandContext(ctx, m.cfg.PHPCGI,
		"-d", "session.save_path=/var/lib/bserver-sessions",
		"-d", "output_buffering=0",
		"-d", "implicit_flush=1",
	)
	cmd.Dir = docroot
	cmd.Env = env
	cmd.Stdin = &bodyBuf
	cmd.Stdout = pw
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		pw.Close()
		log.Printf("php-cgi start error for %s: %v", scriptFilename, err)
		http.Error(w, "CGI Error", http.StatusInternalServerError)
		return
	}
	pw.Close() // parent's write end; child keeps its dup, we'll see EOF when it exits

	var waitErr error
	var waited bool
	waitProc := func() {
		if !waited {
			waitErr = cmd.Wait()
			waited = true
		}
	}
	defer waitProc()

	// Phase 1 + 2: read into buffer, first until end-of-headers, then until
	// the grace period expires or the process finishes.
	var buf bytes.Buffer
	readBuf := make([]byte, 4096)
	headerEnd := -1
	headerSepLen := 0
	deadline := time.Now().Add(phpTimeout) // idle deadline for header phase
	gracedeadline := time.Time{}           // set once headers are found
	finishedQuickly := false
	timedOut := false

	for {
		// Pick the tighter of idle and grace deadlines.
		d := deadline
		if !gracedeadline.IsZero() && gracedeadline.Before(d) {
			d = gracedeadline
		}
		if err := pr.SetReadDeadline(d); err != nil {
			break
		}

		n, rerr := pr.Read(readBuf)
		if n > 0 {
			buf.Write(readBuf[:n])
			deadline = time.Now().Add(phpTimeout) // reset idle deadline

			if headerEnd == -1 {
				b := buf.Bytes()
				if idx := bytes.Index(b, []byte("\r\n\r\n")); idx >= 0 {
					headerEnd, headerSepLen = idx, 4
				} else if idx := bytes.Index(b, []byte("\n\n")); idx >= 0 {
					headerEnd, headerSepLen = idx, 2
				}
				if headerEnd >= 0 && streamAfter > 0 {
					gracedeadline = time.Now().Add(streamAfter)
				} else if headerEnd >= 0 {
					// streaming disabled (0s grace) — switch immediately
					gracedeadline = time.Now()
				}
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				finishedQuickly = true
				break
			}
			if os.IsTimeout(rerr) {
				// Which deadline fired?
				if !gracedeadline.IsZero() && !time.Now().Before(gracedeadline) {
					break // grace expired — switch to streaming
				}
				// idle timeout before grace (never saw headers, or silent)
				timedOut = true
				break
			}
			// Unexpected read error — stop reading, fall through.
			break
		}
	}

	if timedOut {
		cancel()
		waitProc()
		log.Printf("php-cgi idle timeout after %s for %s", phpTimeout, scriptFilename)
		http.Error(w, "Gateway Timeout", http.StatusGatewayTimeout)
		return
	}

	// No complete header block seen — treat buffered output as raw body.
	if headerEnd == -1 {
		waitProc()
		if waitErr != nil {
			log.Printf("php-cgi error for %s (cwd=%s): %v", scriptFilename, docroot, waitErr)
			if buf.Len() == 0 {
				http.Error(w, "CGI Error", http.StatusInternalServerError)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(buf.Bytes())
		return
	}

	// Parse CGI headers (shared by buffered and streaming paths). Sanitize
	// names and values to prevent HTTP response splitting via injected
	// \r\n sequences.
	statusCode := http.StatusOK
	for _, line := range strings.Split(string(buf.Bytes()[:headerEnd]), "\n") {
		line = strings.TrimRight(line, "\r")
		colon := strings.IndexByte(line, ':')
		if colon == -1 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])

		if !isValidHeaderName(key) {
			continue
		}
		val = sanitizeHeaderValue(val)

		if strings.EqualFold(key, "Status") {
			if parts := strings.SplitN(val, " ", 2); len(parts) > 0 {
				if code, err := strconv.Atoi(parts[0]); err == nil {
					statusCode = code
				}
			}
			continue
		}
		w.Header().Add(key, val)
	}

	// Location without explicit Status implies 302
	if w.Header().Get("Location") != "" && statusCode == http.StatusOK {
		statusCode = http.StatusFound
	}

	body := buf.Bytes()[headerEnd+headerSepLen:]

	if finishedQuickly {
		// Script finished within grace period — send as a single response.
		waitProc()
		if waitErr != nil {
			// Exit non-zero but we have output; log and still serve it.
			log.Printf("php-cgi error for %s (cwd=%s): %v", scriptFilename, docroot, waitErr)
		}
		w.WriteHeader(statusCode)
		w.Write(body)
		return
	}

	// Streaming path: flush headers and buffered body, then copy the rest
	// chunk-by-chunk with per-flush transport deadline extensions.
	rc := http.NewResponseController(w)
	w.WriteHeader(statusCode)
	if _, werr := w.Write(body); werr != nil {
		return
	}
	_ = rc.Flush()
	_ = rc.SetWriteDeadline(time.Now().Add(phpTimeout + 30*time.Second))

	for {
		if err := pr.SetReadDeadline(time.Now().Add(phpTimeout)); err != nil {
			return
		}
		n, rerr := pr.Read(readBuf)
		if n > 0 {
			if _, werr := w.Write(readBuf[:n]); werr != nil {
				return
			}
			_ = rc.Flush()
			_ = rc.SetWriteDeadline(time.Now().Add(phpTimeout + 30*time.Second))
		}
		if rerr != nil {
			if rerr == io.EOF {
				return
			}
			if os.IsTimeout(rerr) {
				log.Printf("php-cgi idle timeout after %s for %s (streaming)", phpTimeout, scriptFilename)
				return
			}
			return
		}
	}
}

func (m *virtualHostMux) handleYAML(w http.ResponseWriter, r *http.Request, docRoot, yamlPath string, site siteSettings) {
	debug := m.cfg.debugEnabled(r)
	key := cacheKey(docRoot, hostOnly(r.Host), yamlPath)

	// Skip cache for requests with query parameters or POST data, since
	// scripts may produce different output based on $_GET/$_POST values.
	dynamic := r.URL.RawQuery != "" || r.Method == http.MethodPost

	// A vhost opts out of caching entirely with cache-age: 0 in its
	// _config.yaml (site.cacheEnabled). Its pages are then re-rendered on
	// every request and served with Cache-Control: no-store.
	useCache := !debug && !dynamic && site.cacheEnabled() && m.cfg.Cache != nil

	// Try cache (skip for debug mode and dynamic requests)
	if useCache {
		if cached, ok := m.cfg.Cache.Get(key); ok {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(site.CacheAge.Seconds())))
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(cached))
			return
		}
	}

	// Cold render: bound the global concurrent render count.
	if !acquirePageRenderSlot(r.Context()) {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer func() { <-pageRenderSem }()

	// Re-check cache: a sibling request may have rendered this key while
	// we were queued for a slot.
	if useCache {
		if cached, ok := m.cfg.Cache.Get(key); ok {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(site.CacheAge.Seconds())))
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(cached))
			return
		}
	}

	// Only a yaml file that itself defines main is a page (the same test
	// nav auto-discovery uses). Anything else - data lists, partial
	// definition files like title.yaml or navbar.yaml, config - used to
	// render a ghost page assembled from whichever nav target's
	// definitions loaded first. Now: raw-yaml allowlist or 404.
	if data, err := os.ReadFile(yamlPath); err == nil {
		if parsed, perr := parseYAMLOrdered(data); perr == nil {
			doc, isMap := parsed.(*OrderedMap)
			if !isMap || !doc.Has("main") {
				m.serveErrorPage(w, r, docRoot, http.StatusNotFound, "", site, false)
				return
			}
		}
	}

	output, sourceFiles, scriptHeaders := renderYAMLPage(docRoot, yamlPath, debug, site.ParentLevels, r)

	// Don't cache pages that emit per-request HTTP headers (e.g., Set-Cookie
	// for sessions) — each visitor needs their own headers.
	if useCache && len(scriptHeaders) == 0 {
		m.cfg.Cache.Put(key, output, sourceFiles)
	}

	// Apply any HTTP headers emitted by PHP scripts (e.g., Set-Cookie for sessions)
	for key, vals := range scriptHeaders {
		for _, v := range vals {
			w.Header().Add(key, v)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !site.cacheEnabled() || len(scriptHeaders) > 0 {
		w.Header().Set("Cache-Control", "no-store")
	} else if !debug && !dynamic {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(site.CacheAge.Seconds())))
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(output))
}

func (m *virtualHostMux) handleMarkdown(w http.ResponseWriter, r *http.Request, docRoot, mdPath string, site siteSettings) {
	debug := m.cfg.debugEnabled(r)
	key := cacheKey(docRoot, hostOnly(r.Host), mdPath)

	// Skip cache for requests with query parameters or POST data
	dynamic := r.URL.RawQuery != "" || r.Method == http.MethodPost

	// A vhost opts out of caching entirely with cache-age: 0 in its
	// _config.yaml (site.cacheEnabled). Its pages are then re-rendered on
	// every request and served with Cache-Control: no-store.
	useCache := !debug && !dynamic && site.cacheEnabled() && m.cfg.Cache != nil

	// Try cache (skip for debug mode and dynamic requests)
	if useCache {
		if cached, ok := m.cfg.Cache.Get(key); ok {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(site.CacheAge.Seconds())))
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(cached))
			return
		}
	}

	// Cold render: bound the global concurrent render count.
	if !acquirePageRenderSlot(r.Context()) {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer func() { <-pageRenderSem }()

	// Re-check cache: a sibling request may have rendered this key while
	// we were queued for a slot.
	if useCache {
		if cached, ok := m.cfg.Cache.Get(key); ok {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(site.CacheAge.Seconds())))
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(cached))
			return
		}
	}

	output, sourceFiles, scriptHeaders := renderMarkdownPage(docRoot, mdPath, debug, site.ParentLevels, r)

	// Don't cache pages that emit per-request HTTP headers (e.g., Set-Cookie
	// for sessions) — each visitor needs their own headers.
	if useCache && len(scriptHeaders) == 0 {
		m.cfg.Cache.Put(key, output, sourceFiles)
	}

	// Apply any HTTP headers emitted by PHP scripts (e.g., Set-Cookie for sessions)
	for key, vals := range scriptHeaders {
		for _, v := range vals {
			w.Header().Add(key, v)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !site.cacheEnabled() || len(scriptHeaders) > 0 {
		w.Header().Set("Cache-Control", "no-store")
	} else if !debug && !dynamic {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(site.CacheAge.Seconds())))
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(output))
}

// Error page rendering is serialized through errorRenderSem to bound memory
// during 404 floods: a scanner hitting many unique paths would otherwise
// spawn one full YAML render per concurrent request (~MBs each). With a
// single slot, renders happen one at a time; extra requests wait up to
// errorRenderMaxWaiting deep, and anything beyond that gets a bare status
// code with no body.
var (
	errorRenderSem     = make(chan struct{}, 1)
	errorRenderWaiting atomic.Int32
)

const errorRenderMaxWaiting = 50

// pageRenderSem caps concurrent YAML/Markdown renders. A scanner that
// enumerates many distinct URLs on a known vhost can otherwise fan out
// dozens of concurrent renders, each parsing ~10 YAML files and
// allocating 10-15 MB — drove heap to 520 MB on May 18 03:41 in a
// single 60 s tick. The cap converts unbounded heap fan-out into
// bounded queueing. Cache hits bypass this entirely; only cold-render
// requests acquire a slot.
var pageRenderSem = make(chan struct{}, 16)

// pageRenderQueueTimeout caps how long a request will wait for a render
// slot. Well under the 120 s HTTP write deadline so the queue path
// fails before the connection times out, freeing the slot for someone
// else. Real renders typically complete in <1 s.
const pageRenderQueueTimeout = 10 * time.Second

// acquirePageRenderSlot blocks up to pageRenderQueueTimeout (or the
// request context deadline, whichever fires first) for a render slot.
// Returns true on success — caller MUST release via `<-pageRenderSem`.
// Returns false on timeout / client disconnect; caller should serve a
// bare 503 (no body) so the failed request is cheap.
func acquirePageRenderSlot(ctx context.Context) bool {
	timer := time.NewTimer(pageRenderQueueTimeout)
	defer timer.Stop()
	select {
	case pageRenderSem <- struct{}{}:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// serveErrorPage renders an error page through the YAML system.
// If no error template is found, it falls back to a plain-text response.
//
// hostFallback is true when the request's Host has no matching directory
// under www/ and is being served from the default vhost. In that case we
// skip rendering and caching entirely — scanners send arbitrary Host
// headers and unique paths, so caching their 404s would explode the
// render cache (host is part of the key) without serving any real user.
func (m *virtualHostMux) serveErrorPage(w http.ResponseWriter, r *http.Request, docRoot string, statusCode int, message string, site siteSettings, hostFallback bool) {
	if hostFallback {
		w.WriteHeader(statusCode)
		return
	}

	debug := m.cfg.debugEnabled(r)

	// Cache rendered error pages per docRoot+statusCode+path (skip for debug
	// mode, custom messages which are request-specific, and vhosts that
	// disabled caching with cache-age: 0).
	useCache := !debug && message == "" && site.cacheEnabled() && m.cfg.Cache != nil
	key := fmt.Sprintf("%s:error:%s:%d:%s", docRoot, hostOnly(r.Host), statusCode, r.URL.Path)
	if useCache {
		if cached, ok := m.cfg.Cache.Get(key); ok {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(statusCode)
			w.Write([]byte(cached))
			return
		}
	}

	// Bounded serial queue. Overflow → bare status code, no body.
	if errorRenderWaiting.Add(1) > errorRenderMaxWaiting {
		errorRenderWaiting.Add(-1)
		w.WriteHeader(statusCode)
		return
	}
	select {
	case errorRenderSem <- struct{}{}:
		errorRenderWaiting.Add(-1)
	case <-r.Context().Done():
		errorRenderWaiting.Add(-1)
		return
	}
	defer func() { <-errorRenderSem }()

	// Re-check rate-limit state: during a scanner flood, many concurrent
	// requests pass the middleware's isBlocked check (counter not yet at
	// threshold) and then pile up behind this serializer. By the time a
	// queued request acquires the slot, the IP may have been blocked by an
	// earlier request's result. Without this check, all queued requests
	// would render full 404 pages anyway, defeating the block.
	if m.rl != nil {
		if blocked, _ := m.rl.isBlocked(clientIP(r)); blocked {
			w.WriteHeader(statusCode)
			return
		}
	}

	// Re-check cache: a prior queued request may have rendered this key
	// while we were waiting.
	if useCache {
		if cached, ok := m.cfg.Cache.Get(key); ok {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(statusCode)
			w.Write([]byte(cached))
			return
		}
	}

	output, sourceFiles := renderErrorPage(docRoot, statusCode, message, debug, site.ParentLevels, r)
	if output == "" {
		http.Error(w, fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)), statusCode)
		return
	}

	if useCache {
		m.cfg.Cache.Put(key, output, sourceFiles)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(statusCode)
	w.Write([]byte(output))
}

// serveFavicon generates and serves a favicon from _favicon.yaml or defaults.
func (m *virtualHostMux) serveFavicon(w http.ResponseWriter, r *http.Request, docRoot string, site siteSettings) {
	data, err := getCachedFavicon(docRoot)
	if err != nil {
		log.Printf("favicon error for %s: %v", docRoot, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(site.StaticAge.Seconds())))
	w.Write(data)
}

// selfSignedEntry holds a self-signed certificate with an expiry time
// so it doesn't permanently shadow a newly-issued LE cert.
type selfSignedEntry struct {
	cert    *tls.Certificate
	created time.Time
}

const selfSignedTTL = 10 * time.Minute

// maxSelfSignedCacheEntries bounds the in-memory self-signed cert cache. The
// cache is keyed by SNI, which an unauthenticated client fully controls, so
// without a cap a flood of unique hostnames would grow it without limit
// (a memory-exhaustion amplification, cheap for the attacker). At capacity
// we sweep TTL-expired entries; if none can be freed, the fresh cert is
// still served but not cached (the next handshake regenerates it — bounded
// CPU, no unbounded heap).
const maxSelfSignedCacheEntries = 1024

// self-signed certificate cache
var selfSignedCache sync.Map

func selfSignedCacheSize() int {
	n := 0
	selfSignedCache.Range(func(_, _ any) bool { n++; return true })
	return n
}

// storeSelfSigned caches a self-signed cert while keeping the cache bounded.
func storeSelfSigned(host string, entry *selfSignedEntry) {
	if selfSignedCacheSize() >= maxSelfSignedCacheEntries {
		now := time.Now()
		freed := false
		selfSignedCache.Range(func(k, v any) bool {
			if now.Sub(v.(*selfSignedEntry).created) >= selfSignedTTL {
				selfSignedCache.Delete(k)
				freed = true
			}
			return true
		})
		if !freed {
			return // at capacity with all-fresh entries; serve without caching
		}
	}
	selfSignedCache.Store(host, entry)
}

// leFailCache tracks domains where Let's Encrypt recently failed, so we don't
// retry on every TLS handshake (which spams LE and logs). Package-level so the
// memory monitor can report its size.
var leFailCache sync.Map // host -> time.Time (retry-after)

func leFailCacheSize() int {
	n := 0
	leFailCache.Range(func(_, _ any) bool { n++; return true })
	return n
}

// validCertHost reports whether an SNI value is safe to use as the basename
// of an on-disk certificate cache file. The TLS SNI (hello.ServerName) is
// fully attacker-controlled and Go's crypto/tls only rejects a trailing dot
// — not path separators or "..". Without this guard, an SNI like
// "../../tmp/x" would make filepath.Join(cacheDir, host+".crt") escape the
// cache directory, allowing reads/writes of arbitrary .crt/.key paths. This
// mirrors the Host-header validation in ServeHTTP. It also bounds the length
// so a pathological SNI can't create absurd filenames.
func validCertHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if strings.ContainsAny(host, "/\\") || strings.Contains(host, "..") {
		return false
	}
	// Reject control characters and NUL, which have no place in a hostname
	// and could confuse filesystem or logging layers.
	for i := 0; i < len(host); i++ {
		if host[i] < 0x20 || host[i] == 0x7f {
			return false
		}
	}
	return true
}

// self-signed certificate generator with disk cache
func getOrCreateSelfSignedCert(host, cacheDir string) (*tls.Certificate, error) {
	// No SNI (e.g. clients connecting to an IP address) — use a stable
	// placeholder so these connections still receive a self-signed cert
	// instead of a handshake failure, matching prior behavior.
	if host == "" {
		host = "default"
	}
	// The SNI is attacker-controlled; refuse anything that could escape the
	// cert-cache directory or otherwise isn't a plausible hostname.
	if !validCertHost(host) {
		return nil, fmt.Errorf("invalid certificate host %q", host)
	}

	certFile := filepath.Join(cacheDir, host+".crt")
	keyFile := filepath.Join(cacheDir, host+".key")

	// return cached in memory if not expired
	if v, ok := selfSignedCache.Load(host); ok {
		entry := v.(*selfSignedEntry)
		if time.Since(entry.created) < selfSignedTTL {
			return entry.cert, nil
		}
		selfSignedCache.Delete(host)
	}

	// try disk cache
	if certPEM, err1 := os.ReadFile(certFile); err1 == nil {
		if keyPEM, err2 := os.ReadFile(keyFile); err2 == nil {
			cert, err := tls.X509KeyPair(certPEM, keyPEM)
			if err == nil {
				storeSelfSigned(host, &selfSignedEntry{cert: &cert, created: time.Now()})
				return &cert, nil
			}
		}
	}

	// generate new cert
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	// Random 128-bit serial number — RFC 5280 requires up to 20 octets and
	// uniqueness; using crypto/rand avoids the predictability of a
	// timestamp-based serial.
	serialMax := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialMax)
	if err != nil {
		return nil, err
	}
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{host},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	// write to disk
	if err := os.MkdirAll(cacheDir, 0700); err == nil {
		_ = os.WriteFile(certFile, certPEM, 0600)
		_ = os.WriteFile(keyFile, keyPEM, 0600)
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	storeSelfSigned(host, &selfSignedEntry{cert: &cert, created: time.Now()})
	return &cert, nil
}

func main() {
	var (
		baseDir     string
		showVersion bool
	)

	flag.StringVar(&baseDir, "base", "", "web content root directory (default: www subdirectory of cwd)")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Printf("bserver %s\n", Version)
		os.Exit(0)
	}

	// Determine base directory: -base flag > BASE_DIR env > ./www
	if baseDir == "" {
		baseDir = os.Getenv("BASE_DIR")
	}

	var base string
	if baseDir != "" {
		abs, err := filepath.Abs(baseDir)
		if err != nil {
			log.Printf("Invalid base directory %q: %v", baseDir, err)
			os.Exit(1)
		}
		base = abs
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			log.Printf("Getwd error: %v", err)
			os.Exit(1)
		}
		base = filepath.Join(cwd, "www")
	}
	// Resolve symlinks so paths match what child processes see.
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}

	// Load _config.yaml from the base directory.
	// Precedence: environment variable > _config.yaml value > built-in default.
	yamlCfg := loadConfigMap(filepath.Join(base, "_config.yaml"))
	if yamlCfg != nil {
		log.Printf("Loaded configuration from %s/_config.yaml", base)
	}

	resolve := func(yamlKey, envVar, def string) string {
		if v, ok := configString(yamlCfg, yamlKey, ""); ok {
			def = v
		}
		if envVar != "" {
			if v := os.Getenv(envVar); v != "" {
				return v
			}
		}
		return def
	}
	resolveInt := func(yamlKey string, def int) int {
		if v, ok := configInt(yamlCfg, yamlKey, 0); ok {
			return v
		}
		return def
	}

	httpAddr := resolve("http", "HTTP_ADDR", ":80")
	httpsAddr := resolve("https", "HTTPS_ADDR", ":443")
	leEmail := resolve("email", "LE_EMAIL", "")
	if leEmail == "" {
		leEmail = defaultLEEmail(base)
	}
	cacheDir := resolve("cert-cache", "CERT_CACHE", "./cert-cache")
	cacheMaxSizeMB := resolveInt("cache-size", 1024)
	cacheMaxAgeSec := resolveInt("cache-age", int(defaultCacheMaxAge.Seconds()))
	maxStaticAgeSec := resolveInt("static-age", 86400)
	maxParentLvls := resolveInt("parent-levels", DefaultMaxParentLevels)
	maxBodySizeMB := resolveInt("max-body-size", 10)       // default 10 MB
	jsHeapMB := resolveInt("js-heap-mb", 128)              // per-script heap-growth cap (0 disables)
	phpTimeoutSec := resolveInt("php-timeout", 60)         // idle timeout: kill php-cgi if silent this long
	phpStreamAfterSec := resolveInt("php-stream-after", 5) // buffer php-cgi output this long before streaming

	// Memory monitor tunables (see memmon.go for defaults)
	memLogIntervalSec := resolveInt("mem-log-interval", int(defaultMemInterval.Seconds()))
	memHeapThresholdMB := resolveInt("mem-heap-threshold-mb", defaultHeapThresholdMB)
	memGoroutineThreshold := resolveInt("mem-goroutine-threshold", defaultGoroutineThreshold)
	memGrowthMBPer5Min := resolveInt("mem-growth-mb-per-5min", defaultGrowthMBPer5Min)
	memDumpDir := resolve("mem-dump-dir", "", defaultMemDumpDir)
	memDumpCooldownMin := resolveInt("mem-dump-cooldown-min", int(defaultMemDumpCooldown.Minutes()))
	memDumpMaxFiles := resolveInt("mem-dump-max-files", defaultMemDumpMaxFiles)
	pprofAddr := resolve("pprof-addr", "", defaultPprofAddr)

	// Optional debug token: when set, ?debug=<token> is required to enable
	// debug HTML output. When empty, ?debug works without authentication
	// (intended for development environments only).
	debugToken := resolve("debug-token", "DEBUG_TOKEN", "")

	// PHP: _config.yaml > PHP_CGI env > auto-detect
	phpcgi := resolve("php", "PHP_CGI", "")
	if phpcgi == "" {
		phpcgi = findPHPCGI()
	}

	// Index priority: _config.yaml > INDEX env > default
	var indexPriority []string
	if idx, ok := configIndex(yamlCfg, "index"); ok {
		indexPriority = idx
	} else if v := os.Getenv("INDEX"); v != "" {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				indexPriority = append(indexPriority, p)
			}
		}
	} else {
		indexPriority = []string{"index.yaml", "index.md", "index.php", "index.html", "index.htm"}
	}

	// Allowed file types: _config.yaml > TYPES env > default
	var allowedTypes []string
	if types, ok := configIndex(yamlCfg, "types"); ok {
		allowedTypes = normalizeTypes(types)
	} else if v := os.Getenv("TYPES"); v != "" {
		var raw []string
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				raw = append(raw, p)
			}
		}
		allowedTypes = normalizeTypes(raw)
	} else {
		allowedTypes = []string{
			"yaml", "md", "php", "html", "htm",
			"css", "js", "map", "wasm",
			"gif", "jpg", "jpeg", "png", "svg", "ico", "webp", "avif", "bmp",
			"woff", "woff2", "ttf", "eot", "otf",
			"pdf", "txt", "xml", "csv",
			"mp3", "mp4", "webm", "ogg", "wav", "mp2",
			"zip", "gz",
		}
	}

	// Path blocking: extra denies / exemptions beyond the built-in defaults
	// (hidden dotfiles and vendor are always denied; .well-known is always
	// exempt). _config.yaml > BLOCK_PATHS/ALLOW_PATHS env > none.
	resolvePathList := func(yamlKey, envKey string) []string {
		if v, ok := configIndex(yamlCfg, yamlKey); ok {
			return normalizePathPatterns(v)
		}
		if v := os.Getenv(envKey); v != "" {
			return normalizePathPatterns(strings.Split(v, ","))
		}
		return nil
	}
	blockedPaths := resolvePathList("block-paths", "BLOCK_PATHS")
	allowedPaths := resolvePathList("allow-paths", "ALLOW_PATHS")

	// Warn if php-cgi was not found
	if phpcgi == "" {
		log.Printf("Warning: php-cgi not found in PATH or common locations; .php files will not work (set php in _config.yaml or PHP_CGI env)")
	} else if _, err := os.Stat(phpcgi); err != nil {
		log.Printf("Warning: php-cgi not found at %s; .php files will not work", phpcgi)
	} else {
		log.Printf("Using php-cgi: %s", phpcgi)
	}

	cacheMaxAge := time.Duration(cacheMaxAgeSec) * time.Second
	maxStaticAge := time.Duration(maxStaticAgeSec) * time.Second

	// Initialize render cache (unless disabled with cache-size: 0)
	var cache *renderCache
	if cacheMaxSizeMB > 0 {
		configuredMax := int64(cacheMaxSizeMB) * (1 << 20) // MB to bytes
		effectiveMax := detectAvailableRAM(configuredMax)
		cache = newRenderCache(effectiveMax, cacheMaxAge)
		log.Printf("Render cache: %s max, %s max age, fsnotify file watching",
			formatBytes(effectiveMax), cacheMaxAge)
	} else {
		log.Printf("Render cache disabled (cache-size: 0)")
	}

	// Start memory monitor so we have a diagnostic trail before any OOM.
	mm := newMemMonitor(memMonitorConfig{
		Interval:           time.Duration(memLogIntervalSec) * time.Second,
		HeapThresholdMB:    memHeapThresholdMB,
		GoroutineThreshold: memGoroutineThreshold,
		GrowthMBPer5Min:    memGrowthMBPer5Min,
		DumpDir:            memDumpDir,
		DumpCooldown:       time.Duration(memDumpCooldownMin) * time.Minute,
		DumpMaxFiles:       memDumpMaxFiles,
		PprofAddr:          pprofAddr,
	}, cache, map[string]cacheProvider{
		"proxy":       proxyCacheSize,
		"vhostConfig": vhostConfigCacheSize,
		"favicon":     faviconCacheSize,
		"selfSigned":  selfSignedCacheSize,
		"leFail":      leFailCacheSize,
	})
	mm.Start()
	maybeStartPprof(pprofAddr)

	// Per-script JS heap-growth cap. Configured value is capped at 25% of
	// available RAM (same heuristic detectAvailableRAM uses for the render
	// cache) so a constrained box doesn't promise more than it has.
	if jsHeapMB > 0 {
		jsHeapBytes := int64(jsHeapMB) * (1 << 20)
		effective := detectAvailableRAM(jsHeapBytes)
		if effective < jsHeapBytes {
			log.Printf("Warning: js-heap-mb (%s) capped to %s by available RAM",
				formatBytes(jsHeapBytes), formatBytes(effective))
		}
		SetJSHeapLimit(effective)
		log.Printf("JS script heap-growth cap: %s per invocation", formatBytes(effective))
	} else {
		log.Printf("JS script heap-growth cap disabled (js-heap-mb: 0)")
	}

	maxBodySize := int64(maxBodySizeMB) * (1 << 20) // MB to bytes

	cfg := &config{
		Base:        base,
		HTTPAddr:    httpAddr,
		HTTPSAddr:   httpsAddr,
		CacheDir:    cacheDir,
		LEEmail:     leEmail,
		PHPCGI:      phpcgi,
		Cache:       cache,
		MaxBodySize: maxBodySize,
		DebugToken:  debugToken,
		Site: siteSettings{
			CacheAge:       cacheMaxAge,
			StaticAge:      maxStaticAge,
			ParentLevels:   maxParentLvls,
			Index:          indexPriority,
			Types:          allowedTypes,
			PHPTimeout:     time.Duration(phpTimeoutSec) * time.Second,
			PHPStreamAfter: time.Duration(phpStreamAfterSec) * time.Second,
			BlockedPaths:   blockedPaths,
			AllowedPaths:   allowedPaths,
		},
	}

	rl := newRateLimiter()
	mux := &virtualHostMux{cfg: cfg, rl: rl}

	// Wrap mux with logging, security headers, body size limit, and rate limiting
	var handler http.Handler = mux
	handler = securityHeadersMiddleware(handler)
	if maxBodySize > 0 {
		handler = maxBodySizeMiddleware(maxBodySize, handler)
	}
	handler = loggingMiddleware(handler)
	handler = rateLimitMiddleware(rl, handler)

	m := &autocert.Manager{
		Cache:  autocert.DirCache(cfg.CacheDir),
		Prompt: autocert.AcceptTOS,
		Email:  cfg.LEEmail,
		HostPolicy: func(ctx context.Context, host string) error {
			// Issue LE certs only for vhosts that actually exist under
			// www/. Allowing arbitrary base domains (e.g. random.com)
			// turned bserver into an LE-challenge amplifier: scanners
			// sending diverse SNIs piled up hundreds of in-flight ACME
			// orders and thousands of TLS handshakes blocked on per-host
			// mutexes (May 22 alert: 2613 goroutines, 846 active ACME
			// calls, leFail cache at 715).
			if certAllowed(host, cfg.Base) {
				return nil
			}
			return fmt.Errorf("host %q not configured as a virtual host", host)
		},
	}

	// Try to start HTTPS server
	httpsOK := false
	httpsSrv := &http.Server{
		Addr:    cfg.HTTPSAddr,
		Handler: handler,
		TLSConfig: &tls.Config{
			GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
				if !isPublicDomain(hello.ServerName) {
					return getOrCreateSelfSignedCert(hello.ServerName, cfg.CacheDir)
				}
				// Only attempt Let's Encrypt for vhosts we actually
				// serve. Allowing any single-dot domain (the prior
				// condition was multi-dot only) made bserver an
				// amplifier for scanner-driven ACME calls: a flood of
				// SNIs for random base domains triggered hundreds of
				// in-flight LE orders and a per-hostname mutex pileup.
				if !certAllowed(hello.ServerName, cfg.Base) {
					return getOrCreateSelfSignedCert(hello.ServerName, cfg.CacheDir)
				}
				// Skip LE if we recently failed for this host
				if retryAfter, ok := leFailCache.Load(hello.ServerName); ok {
					if time.Now().Before(retryAfter.(time.Time)) {
						return getOrCreateSelfSignedCert(hello.ServerName, cfg.CacheDir)
					}
					leFailCache.Delete(hello.ServerName)
				}
				cert, err := m.GetCertificate(hello)
				if err != nil {
					log.Printf("Let's Encrypt failed for %s, falling back to self-signed: %v", hello.ServerName, err)
					leFailCache.Store(hello.ServerName, time.Now().Add(1*time.Hour))
					return getOrCreateSelfSignedCert(hello.ServerName, cfg.CacheDir)
				}
				return cert, nil
			},
			NextProtos: []string{"h2", "http/1.1", acmeALPNProto},
		},
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second, // extended per-flush via ResponseController for streaming PHP
		IdleTimeout:       120 * time.Second,
		ErrorLog:          newTLSErrorLogger(),
	}

	httpsListener, httpsErr := net.Listen("tcp", cfg.HTTPSAddr)
	if httpsErr != nil {
		log.Printf("Warning: cannot listen on %s: %v (HTTPS disabled, HTTP only)", cfg.HTTPSAddr, httpsErr)
	} else {
		httpsOK = true
	}

	// HTTP handler: if HTTPS is active, redirect; otherwise serve directly.
	// Skip redirect for:
	//   - IP addresses and .local hosts (LE can't issue certs for them)
	//   - vhosts with `allow-http: true` in _config.yaml (constrained IoT clients)
	var httpHandler http.Handler
	if httpsOK {
		httpHandler = m.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Host
			if hp, _, err := net.SplitHostPort(h); err == nil {
				h = hp
			}
			if net.ParseIP(h) != nil || strings.HasSuffix(strings.ToLower(h), ".local") || vhostAllowsHTTP(cfg, r) {
				handler.ServeHTTP(w, r)
				return
			}
			u := *r.URL
			u.Scheme = "https"
			u.Host = hostOnly(r.Host)
			http.Redirect(w, r, u.String(), http.StatusPermanentRedirect)
		}))
	} else {
		httpHandler = handler
	}

	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpHandler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Try to listen on the configured HTTP port; fall back to 8000-8099
	httpListener, httpErr := net.Listen("tcp", cfg.HTTPAddr)
	if httpErr != nil {
		log.Printf("Warning: cannot listen on %s: %v (trying alternative ports)", cfg.HTTPAddr, httpErr)
		for port := 8000; port < 8100; port++ {
			altAddr := fmt.Sprintf(":%d", port)
			httpListener, httpErr = net.Listen("tcp", altAddr)
			if httpErr == nil {
				cfg.HTTPAddr = altAddr
				httpSrv.Addr = altAddr
				log.Printf("Using alternative HTTP port: %s", altAddr)
				break
			}
		}
		if httpErr != nil {
			log.Printf("Error: could not open any HTTP port: %v", httpErr)
			os.Exit(1)
		}
	}

	// Drop privileges AFTER opening privileged ports but BEFORE serving
	dropPrivileges()

	// Start servers
	errCh := make(chan error, 2)
	go func() {
		log.Printf("HTTP  -> %s", cfg.HTTPAddr)
		errCh <- httpSrv.Serve(httpListener)
	}()
	if httpsOK {
		go func() {
			log.Printf("HTTPS -> %s (dynamic domains from cwd, fallback self-signed)", cfg.HTTPSAddr)
			tlsListener := tls.NewListener(httpsListener, httpsSrv.TLSConfig)
			errCh <- httpsSrv.Serve(tlsListener)
		}()
	}

	// Wait for a signal (SIGINT, SIGTERM) or a server error
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Printf("Received signal %v, shutting down gracefully...", sig)
	case err := <-errCh:
		log.Printf("Server error: %v, shutting down...", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}
	if httpsOK {
		if err := httpsSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTPS shutdown error: %v", err)
		}
	}
	if cache != nil {
		cache.Close()
	}
	rl.Close()
	mm.Close()
	log.Printf("Server stopped")
}

const acmeALPNProto = "acme-tls/1"

// newTLSErrorLogger creates a logger that suppresses the noisy TLS handshake
// errors that Go's net/http server logs for every failed TLS connection.
// These are almost entirely from port scanners, bots, and clients that
// disconnect mid-handshake — normal internet noise that floods the logs.
func newTLSErrorLogger() *log.Logger {
	w := &tlsErrorLogWriter{}
	return log.New(w, "", 0)
}

type tlsErrorLogWriter struct{}

func (w *tlsErrorLogWriter) Write(p []byte) (int, error) {
	msg := string(p)
	if strings.Contains(msg, "TLS handshake error") {
		return len(p), nil // suppress
	}
	// Pass non-TLS errors through to the standard logger
	log.Print(strings.TrimRight(msg, "\n"))
	return len(p), nil
}

// findPHPCGI searches for the php-cgi executable. It first checks $PATH
// via exec.LookPath, then tries common installation locations.
// Returns the path if found, or empty string if not found.
func findPHPCGI() string {
	// First try $PATH (covers any platform where php-cgi is installed)
	if p, err := exec.LookPath("php-cgi"); err == nil {
		return p
	}
	// Check common locations
	for _, p := range []string{
		"/usr/local/bin/php-cgi",
		"/opt/homebrew/bin/php-cgi",
		"/usr/bin/php-cgi",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// vhostAllowsHTTP reports whether the vhost resolved from r.Host opts out of
// the HTTP→HTTPS redirect via `allow-http: true` in its _config.yaml.
func vhostAllowsHTTP(cfg *config, r *http.Request) bool {
	host := strings.ToLower(hostOnly(r.Host))
	if host == "" || strings.ContainsAny(host, "/\\") || strings.Contains(host, "..") {
		return false
	}
	root := filepath.Join(cfg.Base, host)
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return false
	}
	return vhostSettings(root, cfg.Site).AllowHTTP
}

func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

// defaultLEEmail derives a Let's Encrypt contact email from the first
// public domain found in the vhost directories. Returns "" if none found.
func defaultLEEmail(base string) string {
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "default" || name == "old" || strings.HasPrefix(name, ".") {
			continue
		}
		if isPublicDomain(name) {
			email := "admin@" + name
			log.Printf("No LE email configured; using default: %s", email)
			return email
		}
	}
	return ""
}

// isPublicDomain returns true if the domain looks like a real public domain
// that could potentially have a Let's Encrypt certificate issued for it.
func isPublicDomain(host string) bool {
	host = strings.ToLower(host)

	// IP addresses are not valid for Let's Encrypt
	if net.ParseIP(host) != nil {
		return false
	}

	// Must contain at least one dot (single-label names aren't public)
	if !strings.Contains(host, ".") {
		return false
	}

	// Known non-public suffixes
	nonPublic := []string{
		".local", ".localhost", ".test", ".example",
		".invalid", ".internal", ".lan", ".home",
		".localdomain", ".corp", ".private",
	}
	for _, suffix := range nonPublic {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}

	return true
}

// isKnownVhost returns true if the domain has a matching virtual host
// directory under the base www path, or is exactly one subdomain level
// deeper than a known vhost. This allows, for example, www.example.com
// and api.example.com to work when only www/example.com exists, while
// rejecting deeply nested bogus domains like a.b.c.d.example.com.
// certAllowed reports whether to obtain a Let's Encrypt certificate for
// host. It is intentionally stricter than isKnownVhost: a cert is issued
// only for a hostname that has its own vhost directory, or its "www."
// alias of one. isKnownVhost also treats any single subdomain of a vhost
// as known so it can serve that content from the parent dir — but issuing
// a cert per such name let scanner traffic (e.g. random.example.com when
// www/example.com exists) drive one Let's Encrypt order each and exhaust
// the 50-certs-per-registered-domain weekly limit. Content still serves
// for those names; they just fall back to a self-signed cert.
func certAllowed(host, base string) bool {
	host = strings.ToLower(host)
	dir := filepath.Join(base, host)
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		return true
	}
	if rest, ok := strings.CutPrefix(host, "www."); ok && rest != "" {
		dir = filepath.Join(base, rest)
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

func isKnownVhost(serverName, base string) bool {
	host := strings.ToLower(serverName)

	// Direct match: www/<host> exists as a directory
	dir := filepath.Join(base, host)
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		return true
	}

	// One level up: strip the first label and check the parent domain.
	// This allows www.example.com when www/example.com exists.
	if dot := strings.IndexByte(host, '.'); dot >= 0 {
		parent := host[dot+1:]
		if parent != "" {
			dir = filepath.Join(base, parent)
			if st, err := os.Stat(dir); err == nil && st.IsDir() {
				return true
			}
		}
	}

	return false
}

// dropPrivileges attempts to drop to the 'nobody' user after binding
// privileged ports. Failures are logged as warnings; the server continues
// as the current user.
func dropPrivileges() {
	nobody, err := user.Lookup("nobody")
	if err != nil {
		log.Printf("Warning: cannot find user 'nobody': %v (continuing as current user)", err)
		return
	}
	uid, err := strconv.Atoi(nobody.Uid)
	if err != nil {
		log.Printf("Warning: cannot convert UID: %v (continuing as current user)", err)
		return
	}
	gid, err := strconv.Atoi(nobody.Gid)
	if err != nil {
		log.Printf("Warning: cannot convert GID: %v (continuing as current user)", err)
		return
	}
	if err := syscall.Setgroups([]int{gid}); err != nil {
		log.Printf("Warning: cannot set supplementary groups: %v (continuing as current user)", err)
		return
	}
	if err := syscall.Setgid(gid); err != nil {
		log.Printf("Warning: cannot set GID %d: %v (continuing as current user)", gid, err)
		return
	}
	if err := syscall.Setuid(uid); err != nil {
		log.Printf("Warning: cannot set UID %d: %v (continuing as current user)", uid, err)
		return
	}
	log.Printf("Dropped privileges to nobody (UID=%d GID=%d)", uid, gid)
}

// loggingResponseWriter wraps http.ResponseWriter to capture the status code.
type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}

// Unwrap exposes the wrapped ResponseWriter so http.ResponseController can
// reach the connection through this middleware. Without it, SetWriteDeadline
// and Flush (used by handlePHP and the reverse-proxy stream path) silently
// return ErrNotSupported and the server's 120s WriteTimeout kills
// long-streaming responses mid-body.
func (lrw *loggingResponseWriter) Unwrap() http.ResponseWriter {
	return lrw.ResponseWriter
}

// Hijack forwards to the underlying ResponseWriter if it supports
// http.Hijacker. Required for WebSocket upgrades and the rate limiter's
// connection-close drop strategy to work through the logging middleware.
func (lrw *loggingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := lrw.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// Flush forwards to the underlying ResponseWriter if it supports
// http.Flusher. Required for streaming responses (SSE, chunked PHP output)
// to work through the logging middleware.
func (lrw *loggingResponseWriter) Flush() {
	if fl, ok := lrw.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

// Push forwards to the underlying ResponseWriter if it supports
// http.Pusher (HTTP/2 server push).
func (lrw *loggingResponseWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := lrw.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

// loggingMiddleware logs each request with client IP, hostname, method, path, status, and duration.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(lrw, r)
		log.Printf("%s %s %s %s %d %s", clientIP(r), r.Host, r.Method, r.URL.Path, lrw.statusCode, time.Since(start).Round(time.Millisecond))
	})
}

// defaultCSP is a side-protection Content-Security-Policy applied to every
// response. It deliberately does NOT restrict script-src or style-src
// because bserver renders user-authored YAML/markdown/PHP/JS pages whose
// inline content is unpredictable per-vhost — a strict script-src would
// silently break legitimate pages. Instead it blocks the directives that
// no normal page needs:
//
//	object-src 'none'      — no <object>/<embed>/<applet>
//	base-uri 'self'        — defeat injected <base href="evil.com">
//	frame-ancestors 'self' — modern X-Frame-Options
//	form-action 'self'     — injected <form action=evil> won't submit
//
// Operators that want a stricter CSP (e.g. nonce-based script-src) can
// add a `csp:` key to _config.yaml in a future change; this default is
// the no-config baseline.
const defaultCSP = "object-src 'none'; base-uri 'self'; frame-ancestors 'self'; form-action 'self'"

// securityHeadersMiddleware adds standard security headers to all responses.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", defaultCSP)
		next.ServeHTTP(w, r)
	})
}

// maxBodySizeMiddleware limits the size of request bodies to prevent
// memory exhaustion from oversized POST requests.
func maxBodySizeMiddleware(maxBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.ContentLength > maxBytes {
			http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		next.ServeHTTP(w, r)
	})
}

// isValidHeaderName checks if s is a valid HTTP header field name (RFC 7230 token).
func isValidHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		// RFC 7230 token characters: !#$%&'*+-.^_`|~ DIGIT ALPHA
		if c <= ' ' || c >= 0x7f || c == ':' || c == '"' || c == '/' ||
			c == '(' || c == ')' || c == ',' || c == ';' || c == '<' ||
			c == '=' || c == '>' || c == '?' || c == '@' || c == '[' ||
			c == ']' || c == '{' || c == '}' || c == '\\' {
			return false
		}
	}
	return true
}

// unsafeProxyTarget returns a non-empty reason string if target points at
// an address class that should not be reachable through a public proxy:
// loopback, link-local, multicast, private (RFC1918 / ULA), unspecified,
// or interface-local. An empty hostname is also rejected. DNS names are
// resolved; if any resolved IP is unsafe, the target is rejected.
func unsafeProxyTarget(target *url.URL) string {
	host := target.Hostname()
	if host == "" {
		return "empty host"
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		resolved, err := net.LookupIP(host)
		if err != nil {
			return fmt.Sprintf("dns lookup failed: %v", err)
		}
		ips = resolved
	}
	for _, ip := range ips {
		if ip.IsLoopback() {
			return fmt.Sprintf("loopback address %s", ip)
		}
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Sprintf("link-local address %s", ip)
		}
		if ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
			return fmt.Sprintf("multicast address %s", ip)
		}
		if ip.IsUnspecified() {
			return fmt.Sprintf("unspecified address %s", ip)
		}
		if ip.IsPrivate() {
			return fmt.Sprintf("private address %s", ip)
		}
	}
	return ""
}

// forwardableAsHTTPVar reports whether a request header may be exported to a
// subprocess (php-cgi or an inline/data-source script) as an HTTP_<NAME> CGI
// variable. The Proxy header is refused because HTTP_PROXY is honored as a
// proxy selector by many HTTP clients (libcurl, Guzzle, Python requests, Go),
// so forwarding a client-supplied `Proxy:` value would let any visitor route
// the subprocess's outbound traffic through an attacker-controlled proxy —
// the "httpoxy" class, CVE-2016-5385 et al. Proxy has no legitimate
// request-header use, so dropping it costs nothing.
func forwardableAsHTTPVar(key string) bool {
	return !strings.EqualFold(key, "Proxy")
}

// sanitizeHeaderValue removes \r and \n from a header value to prevent
// HTTP response splitting attacks.
func sanitizeHeaderValue(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	replacer := strings.NewReplacer("\r", "", "\n", "")
	return replacer.Replace(s)
}
