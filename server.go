package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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
	Site        siteSettings // server-wide defaults (per-vhost _config.yaml can override)
}

// virtualHostMux dynamically serves based on cwd directories.
type virtualHostMux struct {
	cfg *config
	sync.Mutex
}

// proxyEntry caches a reverse proxy for a vhost whose index.yaml defines
// an http: backend target.
type proxyEntry struct {
	proxy   *httputil.ReverseProxy // nil if not a proxy vhost
	modTime time.Time              // mtime of index.yaml (zero if absent)
}

var proxyCache sync.Map // docRoot -> *proxyEntry

// getProxyForVhost checks whether the vhost at docRoot is a proxy vhost
// by reading its index.yaml for an "http:" key. Results are cached with
// mtime-based invalidation. Returns nil if not a proxy vhost.
func getProxyForVhost(docRoot string) *httputil.ReverseProxy {
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

	// Return cached proxy if mtime matches
	if cached, ok := proxyCache.Load(docRoot); ok {
		entry := cached.(*proxyEntry)
		if entry.modTime.Equal(currentMtime) {
			return entry.proxy
		}
	}

	// Read and parse index.yaml
	m := loadConfigMap(indexPath)
	backend, ok := configString(m, "http", "")
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
		log.Printf("Warning: invalid proxy backend %q in %s: %v", backend, indexPath, err)
		proxyCache.Store(docRoot, &proxyEntry{modTime: currentMtime})
		return nil
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy error for %s -> %s: %v", r.Host, target, err)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
	}

	log.Printf("Proxy vhost %s -> %s", docRoot, target)
	proxyCache.Store(docRoot, &proxyEntry{proxy: proxy, modTime: currentMtime})
	return proxy
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
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		// Only fall back to "default" for known vhosts (direct match or
		// one subdomain deeper). Reject unknown domains so that bogus
		// deeply-nested domains don't get served real content.
		// 421 Misdirected Request: server is not configured for this host.
		if !isKnownVhost(host, m.cfg.Base) {
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
	}

	// Check if this vhost is a proxy (index.yaml with http: backend)
	if proxy := getProxyForVhost(root); proxy != nil {
		proxy.ServeHTTP(w, r)
		return
	}

	// Resolve per-vhost settings (cached, mtime-invalidated)
	site := vhostSettings(root, m.cfg.Site)

	upath := path.Clean("/" + r.URL.Path)

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
			m.serveErrorPage(w, r, root, http.StatusNotFound, "", site)
			return
		}
	}

	if strings.HasSuffix(strings.ToLower(fsPath), ".php") {
		if st, err := os.Stat(fsPath); err == nil && !st.IsDir() {
			m.handlePHP(w, r, host, root, fsPath)
			return
		}
		// PHP file doesn't exist — fall through to 404
	}

	// YAML rendering: if the resolved path is a .yaml file, render it as HTML
	if strings.HasSuffix(strings.ToLower(fsPath), ".yaml") {
		if st, err := os.Stat(fsPath); err == nil && !st.IsDir() {
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
			// No directory — try sibling .yaml then .md
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
		}
	}

	if info != nil && info.IsDir() {
		indexPHP := filepath.Join(fsPath, "index.php")
		if st, err := os.Stat(indexPHP); err == nil && !st.IsDir() {
			m.handlePHP(w, r, host, root, indexPHP)
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

	m.serveErrorPage(w, r, root, http.StatusNotFound, "", site)
}

func (m *virtualHostMux) handlePHP(w http.ResponseWriter, r *http.Request, host, docroot, scriptFilename string) {
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

	// Forward HTTP request headers as HTTP_* env vars.
	// Go strips Host from r.Header and puts it in r.Host, so add it explicitly.
	env = append(env, "HTTP_HOST="+r.Host)
	for key, vals := range r.Header {
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

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, m.cfg.PHPCGI, "-d", "session.save_path=/tmp")
	cmd.Dir = docroot
	cmd.Env = env
	cmd.Stdin = &bodyBuf

	var stdoutBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = os.Stderr // PHP warnings/errors go to server log

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			log.Printf("php-cgi timeout after 30s for %s", scriptFilename)
			http.Error(w, "Gateway Timeout", http.StatusGatewayTimeout)
			return
		}
		log.Printf("php-cgi error for %s (cwd=%s): %v", scriptFilename, docroot, err)
		if stdoutBuf.Len() == 0 {
			http.Error(w, "CGI Error", http.StatusInternalServerError)
			return
		}
		// PHP may exit non-zero on fatal errors but still produce output
	}

	// Parse CGI response: headers separated from body by blank line
	output := stdoutBuf.Bytes()
	sep := []byte("\r\n\r\n")
	headerEnd := bytes.Index(output, sep)
	if headerEnd == -1 {
		sep = []byte("\n\n")
		headerEnd = bytes.Index(output, sep)
	}
	if headerEnd == -1 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(output)
		return
	}

	body := output[headerEnd+len(sep):]

	// Parse CGI headers, sanitizing names and values to prevent
	// HTTP response splitting via injected \r\n sequences.
	statusCode := http.StatusOK
	for _, line := range strings.Split(string(output[:headerEnd]), "\n") {
		line = strings.TrimRight(line, "\r")
		colon := strings.IndexByte(line, ':')
		if colon == -1 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])

		// Reject header names with invalid characters (RFC 7230: token)
		if !isValidHeaderName(key) {
			continue
		}
		// Strip \r and \n from values to prevent response splitting
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

	w.WriteHeader(statusCode)
	w.Write(body)
}

func (m *virtualHostMux) handleYAML(w http.ResponseWriter, r *http.Request, docRoot, yamlPath string, site siteSettings) {
	_, debug := r.URL.Query()["debug"]
	key := cacheKey(docRoot, yamlPath)

	// Skip cache for requests with query parameters or POST data, since
	// scripts may produce different output based on $_GET/$_POST values.
	dynamic := r.URL.RawQuery != "" || r.Method == http.MethodPost

	// Try cache (skip for debug mode and dynamic requests)
	if !debug && !dynamic && m.cfg.Cache != nil {
		if cached, ok := m.cfg.Cache.Get(key); ok {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(site.CacheAge.Seconds())))
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(cached))
			return
		}
	}

	output, sourceFiles, scriptHeaders := renderYAMLPage(docRoot, yamlPath, debug, site.ParentLevels, r)

	// Don't cache pages that emit per-request HTTP headers (e.g., Set-Cookie
	// for sessions) — each visitor needs their own headers.
	if !debug && !dynamic && len(scriptHeaders) == 0 && m.cfg.Cache != nil {
		m.cfg.Cache.Put(key, output, sourceFiles)
	}

	// Apply any HTTP headers emitted by PHP scripts (e.g., Set-Cookie for sessions)
	for key, vals := range scriptHeaders {
		for _, v := range vals {
			w.Header().Add(key, v)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !debug && !dynamic && len(scriptHeaders) == 0 {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(site.CacheAge.Seconds())))
	} else if len(scriptHeaders) > 0 {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(output))
}

func (m *virtualHostMux) handleMarkdown(w http.ResponseWriter, r *http.Request, docRoot, mdPath string, site siteSettings) {
	_, debug := r.URL.Query()["debug"]
	key := cacheKey(docRoot, mdPath)

	// Skip cache for requests with query parameters or POST data
	dynamic := r.URL.RawQuery != "" || r.Method == http.MethodPost

	// Try cache (skip for debug mode and dynamic requests)
	if !debug && !dynamic && m.cfg.Cache != nil {
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
	if !debug && !dynamic && len(scriptHeaders) == 0 && m.cfg.Cache != nil {
		m.cfg.Cache.Put(key, output, sourceFiles)
	}

	// Apply any HTTP headers emitted by PHP scripts (e.g., Set-Cookie for sessions)
	for key, vals := range scriptHeaders {
		for _, v := range vals {
			w.Header().Add(key, v)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !debug && !dynamic && len(scriptHeaders) == 0 {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(site.CacheAge.Seconds())))
	} else if len(scriptHeaders) > 0 {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(output))
}

// serveErrorPage renders an error page through the YAML system.
// If no error template is found, it falls back to a plain-text response.
func (m *virtualHostMux) serveErrorPage(w http.ResponseWriter, r *http.Request, docRoot string, statusCode int, message string, site siteSettings) {
	_, debug := r.URL.Query()["debug"]

	// Cache rendered error pages per docRoot+statusCode (skip for debug mode
	// or custom messages which are request-specific).
	useCache := !debug && message == "" && m.cfg.Cache != nil
	key := fmt.Sprintf("%s:error:%d", docRoot, statusCode)
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

// self-signed certificate cache
var selfSignedCache sync.Map

// self-signed certificate generator with disk cache
func getOrCreateSelfSignedCert(host, cacheDir string) (*tls.Certificate, error) {
	certFile := filepath.Join(cacheDir, host+".crt")
	keyFile := filepath.Join(cacheDir, host+".key")

	// return cached in memory
	if v, ok := selfSignedCache.Load(host); ok {
		return v.(*tls.Certificate), nil
	}

	// try disk cache
	if certPEM, err1 := os.ReadFile(certFile); err1 == nil {
		if keyPEM, err2 := os.ReadFile(keyFile); err2 == nil {
			cert, err := tls.X509KeyPair(certPEM, keyPEM)
			if err == nil {
				selfSignedCache.Store(host, &cert)
				return &cert, nil
			}
		}
	}

	// generate new cert
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
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
	selfSignedCache.Store(host, &cert)
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
	maxBodySizeMB := resolveInt("max-body-size", 10) // default 10 MB

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
		Site: siteSettings{
			CacheAge:     cacheMaxAge,
			StaticAge:    maxStaticAge,
			ParentLevels: maxParentLvls,
			Index:        indexPriority,
			Types:        allowedTypes,
		},
	}

	mux := &virtualHostMux{cfg: cfg}
	rl := newRateLimiter()

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
			if isKnownVhost(host, cfg.Base) {
				return nil
			}
			return fmt.Errorf("host %q not configured as a virtual host", host)
		},
	}

	// leFailCache tracks domains where Let's Encrypt recently failed,
	// so we don't retry on every TLS handshake (which spams LE and logs).
	var leFailCache sync.Map // host -> time.Time (retry-after)

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
				if !isKnownVhost(hello.ServerName, cfg.Base) {
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
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          newTLSErrorLogger(),
	}

	httpsListener, httpsErr := net.Listen("tcp", cfg.HTTPSAddr)
	if httpsErr != nil {
		log.Printf("Warning: cannot listen on %s: %v (HTTPS disabled, HTTP only)", cfg.HTTPSAddr, httpsErr)
	} else {
		httpsOK = true
	}

	// HTTP handler: if HTTPS is active, redirect; otherwise serve directly
	var httpHandler http.Handler
	if httpsOK {
		httpHandler = m.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

// loggingMiddleware logs each request with client IP, hostname, method, path, status, and duration.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(lrw, r)
		log.Printf("%s %s %s %s %d %s", clientIP(r), r.Host, r.Method, r.URL.Path, lrw.statusCode, time.Since(start).Round(time.Millisecond))
	})
}

// securityHeadersMiddleware adds standard security headers to all responses.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
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

// sanitizeHeaderValue removes \r and \n from a header value to prevent
// HTTP response splitting attacks.
func sanitizeHeaderValue(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	replacer := strings.NewReplacer("\r", "", "\n", "")
	return replacer.Replace(s)
}
