package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// siteSettings holds per-site configuration that can be overridden by each
// virtual host's _config.yaml. Server-wide defaults come from www/_config.yaml.
type siteSettings struct {
	CacheAge          time.Duration
	StaticAge         time.Duration
	ParentLevels      int
	Index             []string
	Types             []string      // allowed file extensions (without dots), e.g. ["html", "css", "jpg"]
	PHPTimeout        time.Duration // idle timeout: kill php-cgi if no output for this long
	PHPStreamAfter    time.Duration // buffer php-cgi output for this long before switching to chunked streaming
	AllowHTTP         bool          // serve this vhost over plain HTTP instead of redirecting to HTTPS
	BlockedPaths      []string      // extra path patterns to deny, beyond the built-in dotfile/vendor defaults
	AllowedPaths      []string      // path patterns to exempt from blocking, overriding the defaults and BlockedPaths
	ProxyPath         string        // request path prefix reverse-proxied to ProxyBackend (e.g. "/terminal/")
	ProxyBackend      string        // host:port backend that ProxyPath forwards to
	ProxyKey          string        // required bs_proxy_auth cookie / Bearer value for ProxyPath (empty = open)
	ProxyAllowPrivate bool          // allow ProxyBackend to be a loopback/private/link-local address
	RawYAML           []string      // site-relative .yaml files served raw (text/yaml) instead of rendered as pages
	AllowIPs          []*net.IPNet  // client IP allowlist (IPs/CIDRs); empty = allow all
	AuthEmail         string        // passwordless auth owner/recipient; when set, the vhost is gated behind an emailed login code (empty = no auth gate)
	AuthSMTP          string        // SMTP relay host:port used to send login codes (default localhost:25)
	AuthFrom          string        // From/envelope sender for login-code mail (default = AuthEmail)
	AuthSecret        []byte        // HMAC key that signs bs_auth cookies, loaded from AuthSecretFile
	AuthSecretFile    string        // path the HMAC key is loaded from (created if absent)
	AuthPublic        []string      // extra always-public path prefixes, beyond the built-in splash/asset set
	AuthScope         string        // path prefix this realm's gate covers ("" = whole vhost); set from a subdirectory _auth.yaml's location
	AuthCookie        string        // session-cookie name this realm issues and checks ("" = the default bs_auth)
	AuthRealms        []authRealm   // every auth gate operating on the vhost, one per _auth.yaml (plus the legacy _config.yaml auth-* keys)
	AuthTTL           time.Duration // bs_auth cookie lifetime (default 7 days)
	AuthUsers         []string      // additional approved addresses listed inline in _auth.yaml
	AuthUsersFile     string        // file of additional approved addresses, one per line
	AuthAllowScript   string        // _auth.yaml allow: hook — shell script deciding whether an address may sign in (e.g. a database check)
	AuthSendScript    string        // _auth.yaml send: hook — shell script that delivers the one-time code (replaces built-in SMTP)
	AuthMailSubject   string        // subject for login-code mail (default "Your login code")
	AuthMailBody      string        // body template for login-code mail; $code is replaced with the code
	ProxyPathUsers    string        // file of addresses allowed to reach ProxyPath (empty = any signed-in user)
	SiteRoot          string        // vhost document root, set by vhostSettings (used to find templates and run hooks)
}

// cacheEnabled reports whether rendered pages for this site may be served
// from (and stored in) the render cache. A vhost disables caching entirely
// by setting cache-age: 0 in its _config.yaml; its pages are then rendered
// fresh on every request and sent with Cache-Control: no-store.
func (s siteSettings) cacheEnabled() bool {
	return s.CacheAge > 0
}

// loadConfigMap loads a _config.yaml file and returns its contents as a map.
// Returns nil if the file does not exist or cannot be parsed.
func loadConfigMap(path string) map[string]interface{} {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]interface{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		log.Printf("Warning: cannot parse %s: %v", path, err)
		return nil
	}
	return m
}

// configString extracts a string value from a config map.
// Returns the value and true if the key exists, or def and false if not.
func configString(m map[string]interface{}, key, def string) (string, bool) {
	if m == nil {
		return def, false
	}
	if v, ok := m[key]; ok {
		return fmt.Sprintf("%v", v), true
	}
	return def, false
}

// configInt extracts an integer value from a config map.
// Returns the value and true if the key exists, or def and false if not.
func configInt(m map[string]interface{}, key string, def int) (int, bool) {
	if m == nil {
		return def, false
	}
	v, ok := m[key]
	if !ok {
		return def, false
	}
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	case string:
		var i int
		if _, err := fmt.Sscanf(n, "%d", &i); err == nil {
			return i, true
		}
	}
	return def, false
}

// configBool extracts a boolean value from a config map.
// Accepts native bools, and the strings "true"/"false"/"yes"/"no"/"1"/"0"
// (case-insensitive). Returns the value and true if the key exists, or def
// and false if not.
func configBool(m map[string]interface{}, key string, def bool) (bool, bool) {
	if m == nil {
		return def, false
	}
	v, ok := m[key]
	if !ok {
		return def, false
	}
	switch b := v.(type) {
	case bool:
		return b, true
	case string:
		switch strings.ToLower(strings.TrimSpace(b)) {
		case "true", "yes", "1":
			return true, true
		case "false", "no", "0":
			return false, true
		}
	case int:
		return b != 0, true
	}
	return def, false
}

// configIndex extracts an index priority list from a config map.
// Supports both YAML lists and comma-separated strings.
// Returns the list and true if the key exists, or nil and false if not.
func configIndex(m map[string]interface{}, key string) ([]string, bool) {
	if m == nil {
		return nil, false
	}
	v, ok := m[key]
	if !ok {
		return nil, false
	}
	switch val := v.(type) {
	case string:
		var parts []string
		for _, p := range strings.Split(val, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				parts = append(parts, p)
			}
		}
		return parts, true
	case []interface{}:
		var parts []string
		for _, item := range val {
			if s := fmt.Sprintf("%v", item); s != "" {
				parts = append(parts, s)
			}
		}
		return parts, true
	}
	return nil, false
}

// applySiteSettings extracts per-site settings from a config map,
// overriding the provided defaults for any keys present.
func applySiteSettings(m map[string]interface{}, defaults siteSettings) siteSettings {
	s := defaults
	if m == nil {
		return s
	}
	if v, ok := configInt(m, "cache-age", 0); ok {
		s.CacheAge = time.Duration(v) * time.Second
	}
	if v, ok := configInt(m, "static-age", 0); ok {
		s.StaticAge = time.Duration(v) * time.Second
	}
	if v, ok := configInt(m, "parent-levels", 0); ok {
		s.ParentLevels = v
	}
	if idx, ok := configIndex(m, "index"); ok {
		s.Index = idx
	}
	if types, ok := configIndex(m, "types"); ok {
		s.Types = normalizeTypes(types)
	}
	if v, ok := configInt(m, "php-timeout", 0); ok && v > 0 {
		s.PHPTimeout = time.Duration(v) * time.Second
	}
	if v, ok := configInt(m, "php-stream-after", 0); ok && v >= 0 {
		s.PHPStreamAfter = time.Duration(v) * time.Second
	}
	if v, ok := configBool(m, "allow-http", false); ok {
		s.AllowHTTP = v
		if v {
			log.Printf("Warning: allow-http=true — HTTPS redirect disabled; session cookies and other secrets may transit in cleartext")
		}
	}
	if v, ok := configIndex(m, "block-paths"); ok {
		s.BlockedPaths = normalizePathPatterns(v)
	}
	if v, ok := configIndex(m, "allow-paths"); ok {
		s.AllowedPaths = normalizePathPatterns(v)
	}
	if v, ok := configIndex(m, "raw-yaml"); ok {
		s.RawYAML = normalizePathPatterns(v)
	}
	// Path-based reverse proxy: serve a backend under a path prefix of this
	// vhost (reusing its cert), gated by a shared key. The key can be given
	// literally or read from a file outside the webroot (preferred, so the
	// secret never risks being web-served).
	if v, ok := configString(m, "proxy-path", ""); ok {
		s.ProxyPath = v
	}
	if v, ok := configString(m, "proxy-path-backend", ""); ok {
		s.ProxyBackend = v
	}
	if v, ok := configString(m, "proxy-path-key", ""); ok {
		s.ProxyKey = v
	}
	if v, ok := configString(m, "proxy-path-key-file", ""); ok && v != "" {
		if b, err := os.ReadFile(v); err == nil {
			s.ProxyKey = strings.TrimSpace(string(b))
		} else {
			log.Printf("Warning: proxy-path-key-file %q unreadable: %v", v, err)
		}
	}
	// Restrict the proxied path to specific signed-in addresses. Needed when a
	// vhost is shared by several people but the backend is not for all of them —
	// a web shell, say. A page-level check inside the app cannot do this: the
	// proxy is served here, before any app code runs.
	if v, ok := configString(m, "proxy-path-users-file", ""); ok && v != "" {
		s.ProxyPathUsers = v
	}
	if v, ok := configBool(m, "proxy-path-allow-private", false); ok {
		s.ProxyAllowPrivate = v
	}
	// Client IP allowlist. When set, only listed IPs/CIDRs may reach this vhost
	// at all (pages, static files, and proxied paths). The list can be given
	// inline via allow-ip and/or in a file (one IP/CIDR per line, # comments)
	// via allow-ip-file — handy for a secret/managed list outside the webroot.
	if v, ok := configIndex(m, "allow-ip"); ok {
		s.AllowIPs = parseIPNets(v)
	}
	if v, ok := configString(m, "allow-ip-file", ""); ok && v != "" {
		if b, err := os.ReadFile(v); err == nil {
			var lines []string
			for _, ln := range strings.Split(string(b), "\n") {
				if ln = strings.TrimSpace(ln); ln != "" && !strings.HasPrefix(ln, "#") {
					lines = append(lines, ln)
				}
			}
			s.AllowIPs = append(s.AllowIPs, parseIPNets(lines)...)
		} else {
			log.Printf("Warning: allow-ip-file %q unreadable: %v", v, err)
		}
	}
	// Passwordless auth gate, legacy keys. The preferred home for auth settings
	// is the vhost's _auth.yaml (see applyAuthFile), but the original auth-*
	// keys in _config.yaml keep working; _auth.yaml values override them.
	// Defaults and the signing secret are applied by finalizeAuthSettings once
	// both sources have been read.
	if v, ok := configString(m, "auth-email", ""); ok && v != "" {
		s.AuthEmail = v
		if sv, ok := configString(m, "auth-smtp", ""); ok && sv != "" {
			s.AuthSMTP = sv
		}
		if fv, ok := configString(m, "auth-from", ""); ok && fv != "" {
			s.AuthFrom = fv
		}
		if dv, ok := configInt(m, "auth-ttl-days", 0); ok && dv > 0 {
			s.AuthTTL = time.Duration(dv) * 24 * time.Hour
		}
		if pv, ok := configIndex(m, "auth-public"); ok {
			s.AuthPublic = normalizePathPatterns(pv)
		}
		if uv, ok := configString(m, "auth-users-file", ""); ok && uv != "" {
			s.AuthUsersFile = uv
		}
		if sf, ok := configString(m, "auth-secret-file", ""); ok && sf != "" {
			s.AuthSecretFile = sf
		}
	}
	return s
}

// applyAuthFile applies a vhost's _auth.yaml, the one file that controls the
// passwordless auth gate. The underscore prefix means it can never be served
// as web content, like _config.yaml. When the vhost is gated, everything
// outside the always-public set (splash, /auth/*, assets) requires a valid
// bs_auth cookie, obtained via a one-time code delivered to an approved
// address. See auth.go.
//
// Keys (all optional; values here override _config.yaml's legacy auth-* keys):
//
//	email:        owner address; setting it enables the gate, and it can always sign in
//	secret-file:  path for the HMAC cookie-signing key (created 0600 if absent); required to enable
//	smtp:         relay host:port for the built-in mailer (default localhost:25)
//	from:         From/envelope sender for code mail (default = email)
//	ttl-days:     signed-in cookie lifetime in days (default 7)
//	public:       extra always-public path prefixes
//	users:        inline list of additional approved addresses
//	users-file:   file of additional approved addresses, one per line
//	mail-subject: subject line for the code mail
//	mail-body:    body template for the code mail; $code is replaced with the code
//	send:         shell script that delivers the code instead of the built-in
//	              mailer; runs with AUTH_EMAIL, AUTH_CODE, AUTH_FROM in the
//	              environment and the docroot as working directory
//	allow:        shell script deciding whether an address may sign in (e.g. a
//	              database lookup); runs with AUTH_EMAIL set, exit 0 = allowed
//	login:        page definitions for the sign-in dialog, overriding
//	              auth-login.yaml (read by the renderer, not here)
//
// Multi-user note: email is the owner and can always sign in, so a mistake in
// users/users-file/allow can never lock the site out. Removing an address (or
// the allow script starting to refuse it) revokes access on the next request
// — within a minute for allow-script verdicts, which are briefly cached —
// even if that person still holds an unexpired cookie.
//
// Location note: _auth.yaml normally lives in the vhost docroot and gates the
// whole vhost. Placed instead in a direct subdirectory (e.g. log/_auth.yaml)
// it gates only that subtree, leaving the rest of the vhost open. Several
// subdirectories can each carry their own _auth.yaml — every file becomes an
// independent realm with its own owner, users and session cookie — see
// findAuthFiles and authRealm.
func applyAuthFile(path string, s *siteSettings) {
	m := loadConfigMap(path)
	if m == nil {
		return
	}
	if v, ok := configString(m, "email", ""); ok && v != "" {
		s.AuthEmail = v
	}
	if v, ok := configString(m, "smtp", ""); ok && v != "" {
		s.AuthSMTP = v
	}
	if v, ok := configString(m, "from", ""); ok && v != "" {
		s.AuthFrom = v
	}
	if v, ok := configInt(m, "ttl-days", 0); ok && v > 0 {
		s.AuthTTL = time.Duration(v) * 24 * time.Hour
	}
	if v, ok := configIndex(m, "public"); ok {
		s.AuthPublic = normalizePathPatterns(v)
	}
	if v, ok := configString(m, "secret-file", ""); ok && v != "" {
		s.AuthSecretFile = v
	}
	if v, ok := configIndex(m, "users"); ok {
		s.AuthUsers = v
	}
	if v, ok := configString(m, "users-file", ""); ok && v != "" {
		s.AuthUsersFile = v
	}
	if v, ok := configString(m, "mail-subject", ""); ok && v != "" {
		s.AuthMailSubject = v
	}
	if v, ok := configString(m, "mail-body", ""); ok && v != "" {
		s.AuthMailBody = v
	}
	if v, ok := configString(m, "send", ""); ok && v != "" {
		s.AuthSendScript = v
	}
	if v, ok := configString(m, "allow", ""); ok && v != "" {
		s.AuthAllowScript = v
	}
}

// finalizeAuthSettings fills auth defaults and loads the signing secret once
// both config sources (_config.yaml auth-* keys and _auth.yaml) have been
// applied. Enabling the gate requires a secret file: without one, cookies
// would be signed with an ephemeral key and every sign-in would be lost on
// restart, so the gate is disabled loudly instead.
func finalizeAuthSettings(s *siteSettings) {
	if s.AuthEmail == "" {
		return
	}
	if s.AuthSMTP == "" {
		s.AuthSMTP = "localhost:25"
	}
	if s.AuthFrom == "" {
		s.AuthFrom = s.AuthEmail
	}
	if s.AuthTTL == 0 {
		s.AuthTTL = 7 * 24 * time.Hour
	}
	if s.AuthSecretFile == "" {
		log.Printf("Warning: auth enabled but no secret-file configured — auth gate disabled for this vhost")
		s.AuthEmail = ""
		return
	}
	if secret, err := loadOrCreateSecret(s.AuthSecretFile); err != nil {
		log.Printf("Warning: auth secret-file %q unusable: %v — auth gate disabled for this vhost", s.AuthSecretFile, err)
		s.AuthEmail = ""
	} else {
		s.AuthSecret = secret
	}
}

// parseIPNets converts a list of IP addresses and CIDRs into matchers. A bare
// address becomes a single-host net (/32 for IPv4, /128 for IPv6). Invalid
// entries are logged and skipped so one typo can't silently open the vhost.
func parseIPNets(entries []string) []*net.IPNet {
	var out []*net.IPNet
	for _, e := range entries {
		if e = strings.TrimSpace(e); e == "" {
			continue
		}
		if strings.Contains(e, "/") {
			if _, n, err := net.ParseCIDR(e); err == nil {
				out = append(out, n)
			} else {
				log.Printf("Warning: invalid allow-ip CIDR %q: %v", e, err)
			}
			continue
		}
		ip := net.ParseIP(e)
		if ip == nil {
			log.Printf("Warning: invalid allow-ip address %q", e)
			continue
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return out
}

// ipAllowed reports whether ip falls within any allowlist entry. An empty list
// allows everything (feature disabled). An unparseable ip is denied.
func ipAllowed(ip string, nets []*net.IPNet) bool {
	if len(nets) == 0 {
		return true
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

// normalizePathPatterns trims whitespace from each pattern and drops empties.
// Path patterns are case-sensitive (filesystem paths on Linux are too).
func normalizePathPatterns(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// pathBlocked reports whether the cleaned URL path should be denied (404).
//
// Precedence:
//  1. Allow list — the built-in "/.well-known" exemption plus any allow-paths
//     from _config.yaml. A match here always wins, so an operator can expose a
//     path that the defaults below would otherwise deny.
//  2. Built-in denies — any hidden segment (a dot-prefixed file or directory,
//     e.g. .git, .env) and any "vendor" directory at any depth.
//  3. block-paths from _config.yaml — additional operator-defined denies.
func (s siteSettings) pathBlocked(upath string) bool {
	for _, p := range s.AllowedPaths {
		if pathMatchesPattern(upath, p) {
			return false
		}
	}
	if pathMatchesPattern(upath, "/.well-known") {
		return false
	}
	if hasHiddenSegment(upath) || pathMatchesPattern(upath, "vendor") {
		return true
	}
	for _, p := range s.BlockedPaths {
		if pathMatchesPattern(upath, p) {
			return true
		}
	}
	return false
}

// rawYAMLAllowed reports whether the cleaned URL path is on the site's
// raw-yaml allowlist: those files are served as text/yaml bytes rather than
// rendered as pages. Matching is by exact site-relative path
// (case-sensitive), so an entry exposes one file, never a pattern.
func (s siteSettings) rawYAMLAllowed(upath string) bool {
	rel := strings.TrimPrefix(upath, "/")
	for _, p := range s.RawYAML {
		if rel == strings.TrimPrefix(p, "/") {
			return true
		}
	}
	return false
}

// hasHiddenSegment reports whether any segment of the cleaned URL path begins
// with a dot (a hidden file or directory, e.g. "/.git/index" or "/.env") or
// an underscore (the convention for non-page files: _config.yaml, CLI helper
// scripts, migration tools). Both are denied by default; allow-paths exempts.
func hasHiddenSegment(upath string) bool {
	for _, seg := range splitPath(upath) {
		if (seg[0] == '.' && seg != "." && seg != "..") || seg[0] == '_' {
			return true
		}
	}
	return false
}

// pathMatchesPattern reports whether the cleaned request path is matched by
// pattern. Two pattern forms:
//
//   - Bare name (single segment, no slash), e.g. "vendor": matches if ANY
//     segment of the path equals it — i.e. the named directory at any depth.
//   - Rooted prefix (leading slash or multiple segments), e.g. "/vendor" or
//     "vendor/public": matches the path only from the docroot, when the
//     pattern's segments are a leading prefix of the path's segments.
//
// Matching is segment-aware, so "vendor" never matches "/vendored/x".
func pathMatchesPattern(upath, pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || pattern == "/" {
		return false
	}
	rooted := strings.HasPrefix(pattern, "/")
	pat := splitPath(pattern)
	if len(pat) == 0 {
		return false
	}
	if len(pat) > 1 {
		rooted = true // multi-segment patterns are inherently rooted prefixes
	}
	segs := splitPath(upath)
	if !rooted {
		for _, s := range segs {
			if s == pat[0] {
				return true
			}
		}
		return false
	}
	if len(pat) > len(segs) {
		return false
	}
	for i, p := range pat {
		if segs[i] != p {
			return false
		}
	}
	return true
}

// splitPath splits a slash path into its non-empty segments.
func splitPath(p string) []string {
	t := strings.Trim(p, "/")
	if t == "" {
		return nil
	}
	return strings.Split(t, "/")
}

// normalizeTypes lowercases each entry and strips any leading dot.
func normalizeTypes(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		t = strings.TrimSpace(strings.ToLower(t))
		t = strings.TrimPrefix(t, ".")
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// isAllowedType checks whether a file extension (with or without leading dot)
// is in the allowed types list. Returns true if the list is empty (no filtering).
func isAllowedType(ext string, types []string) bool {
	if len(types) == 0 {
		return true
	}
	ext = strings.TrimPrefix(strings.ToLower(ext), ".")
	if ext == "" {
		return true // no extension — handled elsewhere (sibling lookup etc.)
	}
	for _, t := range types {
		if t == ext {
			return true
		}
	}
	return false
}

// --- Per-vhost config caching ---

type vhostConfigEntry struct {
	settings  siteSettings
	modTime   time.Time // mtime of _config.yaml (zero if file absent)
	authStamp string    // fingerprint of every _auth.yaml location + mtime (see authFilesStamp)
}

// authRealm is one auth gate operating on a vhost. Scope "" is the classic
// whole-vhost gate (docroot _auth.yaml or _config.yaml auth-* keys); a direct
// subdirectory's _auth.yaml yields a realm whose scope is that subtree, with
// its own owner, approved users, signing secret and session cookie. The most
// specific scope governs a given path (see authRealmFor).
//
// broken marks an _auth.yaml that was found but cannot be operated (no email,
// no usable secret file): its whole scope is refused outright rather than
// served open. A present-but-broken gate must fail closed — refusing the
// subtree is recoverable, leaking it is not.
type authRealm struct {
	scope    string
	broken   bool
	settings siteSettings // full settings with this realm's auth config applied (zero when broken)
}

// authFileLoc is one _auth.yaml found on a vhost: where it lives, the path
// prefix it governs ("" = whole vhost) and its mtime (for cache invalidation).
type authFileLoc struct {
	path    string
	scope   string
	modTime time.Time
}

// findAuthFiles locates every _auth.yaml a vhost operates: one in the docroot
// (governs the whole vhost) plus one in each direct subdirectory (governs
// just that subtree), in sorted order. Symlinked subdirectories are treated
// exactly like real ones — content below them is served through the symlink,
// so the gate must see them the same way (os.Stat follows links where
// ReadDir's entry type does not). Directories whose names begin with "." or
// "_" are not searched: their contents are never served. Files nested deeper
// than one level are not operated as realms; authSubtreeDenied refuses their
// subtrees instead of serving them open.
//
// TODO(perf): this is a ReadDir plus a Stat per subdirectory on every request
// (vhostSettings runs per request). A short-TTL (~60s) cache of directory
// listings and stat results on the input side of the filesystem would
// collapse this — and authSubtreeDenied's per-segment stats — to nothing for
// requests landing within the cache window.
func findAuthFiles(docRoot string) []authFileLoc {
	var found []authFileLoc
	docFile := filepath.Join(docRoot, "_auth.yaml")
	if info, err := os.Stat(docFile); err == nil {
		found = append(found, authFileLoc{docFile, "", info.ModTime()})
	}
	entries, err := os.ReadDir(docRoot)
	if err != nil {
		return found
	}
	for _, e := range entries {
		name := e.Name()
		if name[0] == '.' || name[0] == '_' {
			continue
		}
		if st, err := os.Stat(filepath.Join(docRoot, name)); err != nil || !st.IsDir() {
			continue
		}
		p := filepath.Join(docRoot, name, "_auth.yaml")
		if info, err := os.Stat(p); err == nil {
			found = append(found, authFileLoc{p, "/" + name, info.ModTime()})
		}
	}
	return found
}

// authFilesStamp fingerprints the set of _auth.yaml files so the settings
// cache invalidates when any of them appears, moves, changes or disappears.
func authFilesStamp(files []authFileLoc) string {
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "%s|%d;", f.path, f.modTime.UnixNano())
	}
	return b.String()
}

var vhostConfigCache sync.Map // docRoot -> *vhostConfigEntry

func vhostConfigCacheSize() int {
	n := 0
	vhostConfigCache.Range(func(_, _ any) bool { n++; return true })
	return n
}

// vhostSettings returns the effective site settings for a given docRoot,
// checking for a per-vhost _config.yaml override. Results are cached with
// mtime-based invalidation.
func vhostSettings(docRoot string, defaults siteSettings) siteSettings {
	configPath := filepath.Join(docRoot, "_config.yaml")

	// Check file mtimes
	var currentMtime time.Time
	if info, err := os.Stat(configPath); err == nil {
		currentMtime = info.ModTime()
	}
	authFiles := findAuthFiles(docRoot)
	authStamp := authFilesStamp(authFiles)

	// Return cached if mtimes match
	if cached, ok := vhostConfigCache.Load(docRoot); ok {
		entry := cached.(*vhostConfigEntry)
		if entry.modTime.Equal(currentMtime) && entry.authStamp == authStamp {
			return entry.settings
		}
	}

	// Load and cache
	var settings siteSettings
	if currentMtime.IsZero() {
		settings = defaults
	} else {
		m := loadConfigMap(configPath)
		settings = applySiteSettings(m, defaults)
	}
	settings.SiteRoot = docRoot

	// preAuth is the config-only baseline each subtree realm builds on: a
	// subdirectory's _auth.yaml states its own owner and secret rather than
	// inheriting them from the docroot realm, so a subtree file that omits
	// them yields a broken (refused) realm instead of one silently owned by
	// the docroot's address.
	preAuth := settings

	var realms []authRealm
	haveDocrootFile := len(authFiles) > 0 && authFiles[0].scope == ""
	if haveDocrootFile {
		applyAuthFile(authFiles[0].path, &settings)
	}
	finalizeAuthSettings(&settings)
	if settings.AuthEmail != "" {
		settings.AuthCookie = authCookieName
		realms = append(realms, authRealm{scope: "", settings: settings})
	} else if haveDocrootFile {
		log.Printf("auth: %s cannot be operated — refusing all access to the vhost rather than serving it open", authFiles[0].path)
		realms = append(realms, authRealm{scope: "", broken: true})
	}
	for _, f := range authFiles {
		if f.scope == "" {
			continue
		}
		rs := preAuth
		rs.AuthScope = f.scope
		applyAuthFile(f.path, &rs)
		finalizeAuthSettings(&rs)
		if rs.AuthEmail == "" {
			log.Printf("auth: %s cannot be operated — refusing all access to %s rather than serving it open", f.path, f.scope)
			realms = append(realms, authRealm{scope: f.scope, broken: true})
			continue
		}
		rs.AuthCookie = authScopeCookieName(f.scope)
		realms = append(realms, authRealm{scope: f.scope, settings: rs})
	}
	settings.AuthRealms = realms

	vhostConfigCache.Store(docRoot, &vhostConfigEntry{
		settings:  settings,
		modTime:   currentMtime,
		authStamp: authStamp,
	})

	return settings
}
