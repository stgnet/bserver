package main

// Passwordless per-vhost authentication.
//
// Each _auth.yaml on a vhost (or the legacy auth-* keys in _config.yaml) is
// an auth realm. A docroot file gates the whole vhost: the splash page and a
// small always-public set stay open, but every other path requires a valid
// bs_auth cookie. A file in a direct subdirectory gates just that subtree,
// with its own owner, approved users, signing secret and cookie; several
// subdirectories can each carry one. A visitor gets the cookie by requesting
// a one-time numeric code, which is emailed to an approved address, and
// entering it. The cookie is an HMAC-signed token carrying the recipient
// address and an expiry (default 7 days), so it can be verified statelessly
// on every request and survives server restarts (the signing secret is
// persisted to a file).
//
// An _auth.yaml that exists but cannot be operated — unusable, nested deeper
// than the realm search goes, or reached under a different spelling of its
// path — fails closed: its subtree is refused outright rather than served
// open (see authSubtreeDenied).
//
// The gate itself lives in server.go's ServeHTTP; this file provides the token
// scheme, the code store, the mailer, and the /auth/* endpoints.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	authCookieName  = "bs_auth"
	authCodeTTL     = 10 * time.Minute
	authCodeMaxTry  = 5                // wrong-code guesses before a code is burned
	authSendMinGap  = 30 * time.Second // minimum spacing between code emails, per address
	authSendPerHour = 5                // max code emails per hour, per address
	authSendGlobal  = 60               // max code emails per hour across all addresses
	authCodesMax    = 500              // hard ceiling on tracked in-flight codes
	authDialTimeout = 20 * time.Second
	authSMTPDeadln  = 60 * time.Second // overall deadline once connected (covers greet-pause)
)

// loadOrCreateSecret returns the HMAC signing key stored at path, generating and
// persisting a fresh 32-byte key (base64url-encoded, 0600) if the file does not
// yet exist. Persisting it means issued 7-day cookies stay valid across restarts.
func loadOrCreateSecret(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil {
		s := strings.TrimSpace(string(b))
		if len(s) < 16 {
			return nil, fmt.Errorf("secret too short (%d bytes); delete %s to regenerate", len(s), path)
		}
		return []byte(s), nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	enc := base64.RawURLEncoding.EncodeToString(raw)
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(path, []byte(enc), 0o600); err != nil {
		return nil, err
	}
	log.Printf("auth: generated new signing secret at %s", path)
	return []byte(enc), nil
}

// authUserKey is the request-context key under which the signed-in address is
// stashed by the gate in ServeHTTP. Its own unexported type keeps it from
// colliding with any other package's context keys.
type authUserKey struct{}

// authUserFrom returns the signed-in address for a request, or "" when the
// request did not carry a valid session (public paths, or auth not configured).
func authUserFrom(r *http.Request) string {
	if v, ok := r.Context().Value(authUserKey{}).(string); ok {
		return v
	}
	return ""
}

// --- Approved users --------------------------------------------------------

// The set of addresses allowed to sign in is auth-email plus the contents of
// auth-users-file, a plain list maintained by the site's own app (one address
// per line; blank lines and #-comments ignored). It is re-read whenever the
// file's mtime or size changes, so approving or banning someone takes effect on
// the next request without a restart — including for people who already hold an
// unexpired cookie, because validAuthCookie re-checks membership every time.
var (
	authUsersMu    sync.Mutex
	authUsersCache = map[string]*authUsersEntry{} // file path -> parsed contents
)

type authUsersEntry struct {
	modTime time.Time
	size    int64
	emails  map[string]bool
}

// loadAuthUsers returns the approved-address set from path, using the cached
// copy when the file is unchanged. A missing or unreadable file yields an empty
// set — never an error — so a deleted file degrades to "only auth-email may
// sign in" rather than opening the vhost up.
func loadAuthUsers(path string) map[string]bool {
	if path == "" {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	authUsersMu.Lock()
	defer authUsersMu.Unlock()
	if e, ok := authUsersCache[path]; ok && e.modTime.Equal(fi.ModTime()) && e.size == fi.Size() {
		return e.emails
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	emails := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		emails[normalizeEmail(line)] = true
	}
	authUsersCache[path] = &authUsersEntry{modTime: fi.ModTime(), size: fi.Size(), emails: emails}
	return emails
}

// normalizeEmail lowercases and trims an address so that comparisons, the code
// store and the users file all agree on one spelling. Addresses are treated
// case-insensitively in full; the local part is technically case-sensitive per
// RFC 5321, but no real mail provider honours that and matching case-sensitively
// would let "Crystal@..." and "crystal@..." become two accounts.
func normalizeEmail(addr string) string {
	return strings.ToLower(strings.TrimSpace(addr))
}

// authAllowed reports whether addr may request a code and hold a valid session.
// Approval sources are checked cheapest-first: the owner address, the inline
// users: list from _auth.yaml, the users file, and finally the allow: script
// hook (e.g. a database lookup), whose verdicts are briefly cached.
func authAllowed(s siteSettings, addr string) bool {
	addr = normalizeEmail(addr)
	if addr == "" {
		return false
	}
	if addr == normalizeEmail(s.AuthEmail) {
		return true // owner address: always permitted, so the site can't be locked out
	}
	for _, u := range s.AuthUsers {
		if addr == normalizeEmail(u) {
			return true
		}
	}
	if loadAuthUsers(s.AuthUsersFile)[addr] {
		return true
	}
	return authAllowScript(s, addr)
}

// authMultiUser reports whether the site accepts more than one address — i.e.
// whether the login page should ask which address is signing in. Any approval
// source beyond auth-email makes it a real question.
func (s siteSettings) authMultiUser() bool {
	return len(s.AuthUsers) > 0 || s.AuthUsersFile != "" || s.AuthAllowScript != ""
}

// --- Script hooks -----------------------------------------------------------
//
// _auth.yaml can delegate the two externally-visible auth actions to scripts,
// so a site can plug in its own infrastructure without bserver knowing about
// it: allow: decides whether an address may sign in (a database lookup, an
// LDAP query), send: delivers the one-time code (a local sendmail, an SMS
// gateway, an API call). Scripts run via the shell with AUTH_* values in the
// environment and the vhost's document root as working directory. The script
// text comes from _auth.yaml, which the site operator controls — the same
// trust level as _config.yaml's proxy and path settings. Visitor-supplied
// values are passed only through the environment, never interpolated into the
// command line.

const (
	authAllowScriptTimeout = 10 * time.Second
	authSendScriptTimeout  = 60 * time.Second
	authAllowCacheTTL      = time.Minute // how long an allow: verdict is reused
)

// Allow-script verdicts are cached briefly: the gate re-checks approval on
// every request, and running a subprocess per request would be far too costly.
// The TTL bounds how long a revoked user can keep browsing (one minute), which
// mirrors the users-file behaviour of taking effect on the next re-read.
var (
	authAllowCacheMu sync.Mutex
	authAllowCache   = map[string]authAllowVerdict{} // siteRoot+"\x00"+addr -> verdict
)

type authAllowVerdict struct {
	allowed bool
	until   time.Time
}

// authAllowScript asks the allow: hook whether addr may sign in. Exit status 0
// means allowed; anything else — including a script error or timeout — means
// not allowed (fail closed; the owner address never reaches this point).
func authAllowScript(s siteSettings, addr string) bool {
	if s.AuthAllowScript == "" {
		return false
	}
	key := s.SiteRoot + "\x00" + addr
	now := time.Now()
	authAllowCacheMu.Lock()
	if v, ok := authAllowCache[key]; ok && now.Before(v.until) {
		authAllowCacheMu.Unlock()
		return v.allowed
	}
	// Evict expired entries so unapproved-address probes cannot grow the map.
	for k, v := range authAllowCache {
		if !now.Before(v.until) {
			delete(authAllowCache, k)
		}
	}
	authAllowCacheMu.Unlock()

	out, err := runAuthScript(s.AuthAllowScript, s.SiteRoot, authAllowScriptTimeout,
		"AUTH_EMAIL="+addr)
	allowed := err == nil
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			log.Printf("auth: allow script for %s: %v: %s", maskEmail(addr), err, msg)
		}
	}
	authAllowCacheMu.Lock()
	authAllowCache[key] = authAllowVerdict{allowed: allowed, until: now.Add(authAllowCacheTTL)}
	authAllowCacheMu.Unlock()
	return allowed
}

// runAuthScript executes a hook script through the shell with extra AUTH_*
// variables appended to the server's environment, working directory dir, and
// a hard timeout. Returns the combined output for logging.
func runAuthScript(script, dir string, timeout time.Duration, env ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), env...)
	return cmd.CombinedOutput()
}

// plausibleEmail is a deliberately loose syntax check — enough to reject junk
// and header-injection attempts before an address reaches the mailer, without
// trying to out-guess RFC 5322 about what a real address may contain.
func plausibleEmail(addr string) bool {
	if len(addr) < 3 || len(addr) > 254 {
		return false
	}
	for i := 0; i < len(addr); i++ {
		if addr[i] < 0x20 || addr[i] == 0x7f || addr[i] == ' ' {
			return false // control bytes / spaces: SMTP header-injection hygiene
		}
	}
	at := strings.LastIndexByte(addr, '@')
	if at <= 0 || at == len(addr)-1 {
		return false
	}
	return strings.Contains(addr[at+1:], ".")
}

// --- Signed cookie token ---------------------------------------------------

// makeAuthToken builds "payload.signature", where payload is a base64url copy of
// "email|expiryUnix" and signature is its HMAC-SHA256 under secret.
func makeAuthToken(secret []byte, email string, expiry time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString(
		[]byte(email + "|" + strconv.FormatInt(expiry.Unix(), 10)))
	return payload + "." + authSign(secret, payload)
}

func authSign(secret []byte, payload string) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// parseAuthToken returns the address carried by a well-formed, correctly-signed,
// unexpired token. The signature is checked in constant time; only if it holds
// is the payload trusted. Whether that address is still permitted to sign in is
// a separate question, answered by authAllowed — the signature only proves the
// token was minted by us and not tampered with.
func parseAuthToken(secret []byte, token string, now time.Time) (string, bool) {
	dot := strings.IndexByte(token, '.')
	if dot < 0 {
		return "", false
	}
	payload, sig := token[:dot], token[dot+1:]
	if subtle.ConstantTimeCompare([]byte(sig), []byte(authSign(secret, payload))) != 1 {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", false
	}
	bar := strings.LastIndexByte(string(raw), '|')
	if bar < 0 {
		return "", false
	}
	email, expStr := string(raw[:bar]), string(raw[bar+1:])
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || now.Unix() >= exp {
		return "", false
	}
	return email, true
}

// validAuthCookie returns the signed-in address if the request carries a valid
// bs_auth cookie for an address that is still approved. All cookies of that name
// are checked, not just the first, to tolerate a stale duplicate (see
// anyCookieMatches for the same reasoning).
//
// Membership is re-checked on every request rather than trusted from the token,
// so revoking someone takes effect immediately instead of waiting out their
// cookie's remaining lifetime.
func validAuthCookie(r *http.Request, s siteSettings) (string, bool) {
	now := time.Now()
	for _, c := range r.Cookies() {
		if c.Name != s.authCookie() {
			continue
		}
		if email, ok := parseAuthToken(s.AuthSecret, c.Value, now); ok && authAllowed(s, email) {
			return normalizeEmail(email), true
		}
	}
	return "", false
}

func setAuthCookie(w http.ResponseWriter, s siteSettings, email string) {
	expiry := time.Now().Add(s.AuthTTL)
	http.SetCookie(w, &http.Cookie{
		Name:     s.authCookie(),
		Value:    makeAuthToken(s.AuthSecret, normalizeEmail(email), expiry),
		Path:     "/",
		Expires:  expiry,
		MaxAge:   int(s.AuthTTL.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearAuthCookie expires every auth session cookie the request carries —
// the vhost-wide bs_auth and any per-subtree bs_auth-<dir> variants — so one
// logout ends all of the visitor's sessions on the host.
func clearAuthCookie(w http.ResponseWriter, r *http.Request) {
	names := map[string]bool{authCookieName: true}
	for _, c := range r.Cookies() {
		if strings.HasPrefix(c.Name, authCookieName) {
			names[c.Name] = true
		}
	}
	for name := range names {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
		})
	}
}

// authRealmFor returns the realm whose scope covers path p, preferring the
// most specific (longest) scope when a whole-vhost realm and a subtree realm
// both match. nil means no realm covers p — the path is outside every gate.
func authRealmFor(p string, s siteSettings) *authRealm {
	var best *authRealm
	for i := range s.AuthRealms {
		realm := &s.AuthRealms[i]
		if realm.scope != "" && p != realm.scope && !strings.HasPrefix(p, realm.scope+"/") {
			continue
		}
		if best == nil || len(realm.scope) > len(best.scope) {
			best = realm
		}
	}
	return best
}

// authLoginRealm picks the realm a /auth/* request signs in to, from the
// next= parameter (query or form) that the login flow threads through every
// step. A next outside every subtree falls back to the whole-vhost realm when
// one exists, else the first operating realm; nil when no realm on the vhost
// is usable. Choosing a realm grants nothing by itself — the code send and
// verify steps still require an address that realm approves.
func authLoginRealm(r *http.Request, s siteSettings) *siteSettings {
	p := path.Clean(safeNext(r.FormValue("next")))
	if realm := authRealmFor(p, s); realm != nil && !realm.broken {
		return &realm.settings
	}
	for i := range s.AuthRealms {
		if !s.AuthRealms[i].broken {
			return &s.AuthRealms[i].settings
		}
	}
	return nil
}

// authScopeCookieName derives the session-cookie name for a subtree realm
// from its scope ("/log" -> "bs_auth-log"), so signing in to one realm never
// overwrites another realm's session. Bytes that are unsafe in a cookie name
// (including the leading slash) become '-'.
func authScopeCookieName(scope string) string {
	var b strings.Builder
	b.WriteString(authCookieName)
	for i := 0; i < len(scope); i++ {
		c := scope[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '_':
			b.WriteByte(c)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// authCookie returns the session-cookie name this realm issues and checks:
// the default bs_auth for a whole-vhost realm (and for settings built
// directly, e.g. in tests), bs_auth-<dir> for a subtree realm.
func (s siteSettings) authCookie() string {
	if s.AuthCookie != "" {
		return s.AuthCookie
	}
	return authCookieName
}

// authSubtreeDenied reports whether serving upath must be refused because an
// _auth.yaml exists on disk along its directory chain that no operating realm
// governs. Realms are matched against the request *path*; this check walks
// the *filesystem*, so the two agree exactly when serving is safe. Whatever
// makes them disagree — an _auth.yaml nested deeper than one directory
// level, a realm marked broken, a case-insensitive or Unicode-normalizing
// filesystem resolving /LOG/x to log/x, a symlink the scope match missed —
// lands here and fails closed instead of leaking the subtree. The deepest
// _auth.yaml on the chain is the one that must have a matching realm,
// mirroring authRealmFor's most-specific-scope-wins rule.
//
// TODO(perf): one Stat per path segment per request; see the note on
// findAuthFiles about a short-TTL filesystem metadata cache covering both.
func authSubtreeDenied(root, upath string, site siteSettings) bool {
	deepest, rel := "", ""
	for _, seg := range strings.Split(strings.TrimPrefix(upath, "/"), "/") {
		if seg == "" {
			break
		}
		rel += "/" + seg
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel), "_auth.yaml")); err == nil {
			deepest = rel
		}
	}
	if deepest == "" {
		return false
	}
	for i := range site.AuthRealms {
		realm := &site.AuthRealms[i]
		if !realm.broken && realm.scope == deepest {
			return false
		}
	}
	return true
}

// authPublic reports whether p is reachable without authentication: the splash
// page, favicon/robots, ACME + security.txt under /.well-known, and any extra
// prefixes the vhost lists in auth-public. Note that /auth/* is handled before
// this check in the gate, so it is not listed here.
func authPublic(p string, s siteSettings) bool {
	switch p {
	case "/", "/favicon.ico", "/robots.txt", "/sitemap.xml":
		return true
	}
	if strings.HasPrefix(p, "/.well-known/") {
		return true
	}
	for _, pre := range s.AuthPublic {
		pre = "/" + strings.Trim(pre, "/")
		if p == pre || strings.HasPrefix(p, pre+"/") {
			return true
		}
	}
	return false
}

// safeNext sanitizes a post-login redirect target so it can only point back into
// this site — a leading single slash and no scheme/host — defeating open
// redirects like //evil.com or https://evil.com. Backslashes are rejected
// anywhere because browsers normalize \ to / when parsing a Location header,
// so /\evil.com would become the protocol-relative //evil.com; control bytes
// are rejected as header-injection hygiene.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	for i := 0; i < len(next); i++ {
		if next[i] == '\\' || next[i] < 0x20 {
			return "/"
		}
	}
	return next
}

// --- One-time code store ---------------------------------------------------

type authCodeEntry struct {
	hash    [32]byte
	expiry  time.Time
	tries   int
	sentLog []time.Time // send timestamps within the trailing hour, for rate limiting
}

// authCodes is guarded by authCodesMu: issueCode and checkCode both mutate
// entry fields (hash, tries, sentLog), so concurrent /auth/send and
// /auth/verify requests must serialize on the same lock.
var (
	authCodesMu sync.Mutex
	authCodes   = map[string]*authCodeEntry{} // email -> entry
	// Sends across all addresses in the trailing hour. The per-address limits
	// below cannot bound abuse on a site that accepts more than one recipient:
	// whoever asks simply rotates addresses. This cap is what actually stops
	// the endpoint being used to relay mail to addresses of an attacker's
	// choosing, and it is why authSend refuses unapproved addresses outright.
	authSendGlobalLog []time.Time
)

func deleteAuthCode(email string) {
	authCodesMu.Lock()
	defer authCodesMu.Unlock()
	delete(authCodes, email)
}

func newAuthCode() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is catastrophic; fail closed with an unusable code.
		return ""
	}
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(b[:])%1_000_000)
}

// authUserError marks a refusal whose text is safe to show the visitor
// (rate-limit notices). Anything else — notably SMTP transport errors, which
// embed relay hostnames/IPs — is logged but shown as a generic failure.
type authUserError struct{ msg string }

func (e authUserError) Error() string { return e.msg }

// issueCode enforces the send rate limits, mints a fresh code, stores its hash,
// and returns the plaintext to mail. The returned error is a user-facing reason
// when a send is refused (too frequent / hourly cap).
func issueCode(email string) (string, error) {
	now := time.Now()
	authCodesMu.Lock()
	defer authCodesMu.Unlock()

	// Drop entries whose code has expired and whose send log has aged out.
	// Without this the map only ever shrinks on a successful verify, so a
	// stream of distinct addresses would grow it without bound.
	for k, v := range authCodes {
		if now.After(v.expiry) && (len(v.sentLog) == 0 || now.Sub(v.sentLog[len(v.sentLog)-1]) >= time.Hour) {
			delete(authCodes, k)
		}
	}

	// Global hourly cap, applied before any per-address bookkeeping so that
	// rotating addresses cannot walk around it.
	keptGlobal := authSendGlobalLog[:0]
	for _, t := range authSendGlobalLog {
		if now.Sub(t) < time.Hour {
			keptGlobal = append(keptGlobal, t)
		}
	}
	authSendGlobalLog = keptGlobal
	if len(authSendGlobalLog) >= authSendGlobal {
		return "", authUserError{"too many codes requested; try again later"}
	}

	e := authCodes[email]
	if e == nil {
		if len(authCodes) >= authCodesMax {
			return "", authUserError{"too many codes requested; try again later"}
		}
		e = &authCodeEntry{}
		authCodes[email] = e
	}

	// Prune send log to the trailing hour, then apply the two limits.
	kept := e.sentLog[:0]
	for _, t := range e.sentLog {
		if now.Sub(t) < time.Hour {
			kept = append(kept, t)
		}
	}
	e.sentLog = kept
	if len(e.sentLog) > 0 && now.Sub(e.sentLog[len(e.sentLog)-1]) < authSendMinGap {
		return "", authUserError{"please wait a moment before requesting another code"}
	}
	if len(e.sentLog) >= authSendPerHour {
		return "", authUserError{"too many codes requested; try again later"}
	}

	code := newAuthCode()
	if code == "" {
		return "", authUserError{"could not generate a code; try again"}
	}
	e.hash = sha256.Sum256([]byte(code))
	e.expiry = now.Add(authCodeTTL)
	e.tries = 0
	e.sentLog = append(e.sentLog, now)
	authSendGlobalLog = append(authSendGlobalLog, now)
	return code, nil
}

// checkCode verifies a submitted code, consuming it on success and burning it
// after too many wrong guesses.
func checkCode(email, code string) bool {
	authCodesMu.Lock()
	defer authCodesMu.Unlock()
	e, ok := authCodes[email]
	if !ok {
		return false
	}
	if time.Now().After(e.expiry) || e.tries >= authCodeMaxTry {
		delete(authCodes, email)
		return false
	}
	got := sha256.Sum256([]byte(strings.TrimSpace(code)))
	if subtle.ConstantTimeCompare(got[:], e.hash[:]) == 1 {
		delete(authCodes, email) // single use
		return true
	}
	e.tries++
	return false
}

// --- Mailer ----------------------------------------------------------------

// sendLoginCode mints a code for to and delivers it. Delivery is the send:
// script hook when _auth.yaml defines one — so a site can hand the code to
// sendmail, an SMS gateway, or any API without bserver knowing — and direct
// SMTP to s.AuthSMTP otherwise. The SMTP path dials with a timeout and sets an
// overall deadline so a slow/greylisting relay (a deliberately delayed 220
// banner is common) cannot hang the sender. No SMTP auth or STARTTLS is
// attempted — this targets a trusted local or internal relay.
func sendLoginCode(s siteSettings, to string) error {
	code, err := issueCode(to)
	if err != nil {
		return err
	}
	if s.AuthSendScript != "" {
		out, err := runAuthScript(s.AuthSendScript, s.SiteRoot, authSendScriptTimeout,
			"AUTH_EMAIL="+to, "AUTH_CODE="+code, "AUTH_FROM="+s.AuthFrom)
		if err != nil {
			if msg := strings.TrimSpace(string(out)); msg != "" {
				return fmt.Errorf("send script: %w: %s", err, msg)
			}
			return fmt.Errorf("send script: %w", err)
		}
		return nil
	}
	host := s.AuthSMTP
	if h, _, e := net.SplitHostPort(s.AuthSMTP); e == nil {
		host = h
	}
	helo, _ := os.Hostname()
	if helo == "" {
		helo = "localhost"
	}
	// A Message-ID and Date are mandatory for RFC 5322 compliance; strict MXs
	// (e.g. Google) reject messages that omit them.
	domain := s.AuthFrom
	if at := strings.LastIndexByte(s.AuthFrom, '@'); at >= 0 {
		domain = s.AuthFrom[at+1:]
	}
	var idbuf [16]byte
	_, _ = rand.Read(idbuf[:])
	msgID := "<" + base64.RawURLEncoding.EncodeToString(idbuf[:]) + "@" + domain + ">"
	// Subject and body text are site-configurable (mail-subject / mail-body in
	// _auth.yaml); $code in the body is replaced with the minted code. The
	// subject is forced onto one line since a stray newline would end the
	// header block early.
	subject := s.AuthMailSubject
	if subject == "" {
		subject = "Your login code"
	}
	subject = strings.Join(strings.Fields(subject), " ")
	text := s.AuthMailBody
	if text == "" {
		text = "Your login code is: $code\n\n" +
			"It expires in 10 minutes. If you did not request it, you can ignore this email.\n"
	}
	text = substituteVarTokens(text, func(name string) (string, bool) {
		if name == "code" {
			return code, true
		}
		return "", false
	})
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	if !strings.HasSuffix(text, "\r\n") {
		text += "\r\n"
	}
	body := "From: " + s.AuthFrom + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Date: " + time.Now().Format(time.RFC1123Z) + "\r\n" +
		"Message-ID: " + msgID + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		text

	conn, err := net.DialTimeout("tcp", s.AuthSMTP, authDialTimeout)
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(authSMTPDeadln))
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if err := c.Hello(helo); err != nil {
		return err
	}
	if err := c.Mail(s.AuthFrom); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write([]byte(body)); err != nil {
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// --- HTTP endpoints --------------------------------------------------------

// serveAuth dispatches the /auth/* endpoints. p is the cleaned request path.
func serveAuth(w http.ResponseWriter, r *http.Request, s siteSettings, p string) {
	switch p {
	case "/auth/login":
		authLoginPage(w, r, s, "")
	case "/auth/send":
		authSend(w, r, s)
	case "/auth/verify":
		authVerify(w, r, s)
	case "/auth/logout":
		clearAuthCookie(w, r)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	default:
		http.NotFound(w, r)
	}
}

func authSend(w http.ResponseWriter, r *http.Request, s siteSettings) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	next := safeNext(r.FormValue("next"))
	email := normalizeEmail(r.FormValue("email"))
	if email == "" && !s.authMultiUser() {
		// Single-recipient site: auth-email is the only address that could
		// ever be accepted, so don't make the visitor type it. This keeps the
		// original one-button login for sites that have not opted into
		// multiple users.
		email = normalizeEmail(s.AuthEmail)
	}

	// Refuse to mail anything but an approved address. This is the control that
	// keeps the endpoint from being used to send attacker-chosen recipients a
	// message from this host; the rate limits alone cannot do it, since they are
	// keyed per address. Unapproved and malformed addresses get the same neutral
	// answer, so this cannot be used to enumerate who has an account.
	if !plausibleEmail(email) || !authAllowed(s, email) {
		if plausibleEmail(email) {
			log.Printf("auth: refused login code for unapproved address %s", maskEmail(email))
		}
		authLoginPageFor(w, r, s, email,
			"If that address has access, a code is on its way. Otherwise, ask the site owner to approve it first.")
		return
	}

	// Mail is sent synchronously so we can surface a real failure to the user,
	// but the SMTP deadline bounds how long that can take.
	if err := sendLoginCode(s, email); err != nil {
		log.Printf("auth: sending login code to %s failed: %v", maskEmail(email), err)
		// Show rate-limit refusals verbatim, but never raw transport errors:
		// a dial failure would hand any visitor the relay's host/IP.
		msg := "Could not send the code right now. Please try again later."
		var ue authUserError
		if errors.As(err, &ue) {
			msg = ue.msg
		}
		authLoginPageFor(w, r, s, email, msg)
		return
	}
	http.Redirect(w, r, "/auth/login?sent=1&email="+urlQueryEscape(email)+"&next="+urlQueryEscape(next), http.StatusSeeOther)
}

func authVerify(w http.ResponseWriter, r *http.Request, s siteSettings) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	next := safeNext(r.FormValue("next"))
	email := normalizeEmail(r.FormValue("email"))
	if email == "" && !s.authMultiUser() {
		email = normalizeEmail(s.AuthEmail) // single-recipient site; see authSend
	}
	// Re-check approval here as well as in authSend: a code could have been
	// issued moments before the address was banned, and that code must not
	// still buy a session.
	if authAllowed(s, email) && checkCode(email, r.FormValue("code")) {
		setAuthCookie(w, s, email)
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/auth/login?err=1&email="+urlQueryEscape(email)+"&next="+urlQueryEscape(next), http.StatusSeeOther)
}

// authLoginPage renders the login page. errMsg, when non-empty, is an inline
// error to show (e.g. a mail failure). Query flags sent=1 / err=1 drive the
// "code sent" and "incorrect code" notices.
func authLoginPage(w http.ResponseWriter, r *http.Request, s siteSettings, errMsg string) {
	authLoginPageFor(w, r, s, normalizeEmail(r.URL.Query().Get("email")), errMsg)
}

// authLoginPageFor renders the login page with the address pre-filled, so a
// visitor who has just been mailed a code (or shown an error) does not have to
// retype it before entering the code.
//
// The page's markup lives entirely outside the server: in the vhost's
// _auth.yaml login: section, its auth-login.yaml, or the server-wide default
// auth-login.yaml in the www root (see renderAuthLoginPage). Go contributes
// only the runtime facts — no HTML is assembled here. A deployment with no
// template anywhere is a broken install and answers with a plain-text error,
// the same posture as a missing error.yaml.
func authLoginPageFor(w http.ResponseWriter, r *http.Request, s siteSettings, email, errMsg string) {
	next := safeNext(r.URL.Query().Get("next"))
	if v := safeNext(r.FormValue("next")); next == "/" && v != "/" {
		next = v
	}
	sent := r.URL.Query().Get("sent") == "1"
	if errMsg == "" && r.URL.Query().Get("err") == "1" {
		errMsg = "That code was incorrect or expired. Request a new one."
	}

	st := authLoginState{
		Next:      next,
		Email:     email,
		NoticeErr: errMsg != "",
		Sent:      sent,
		MultiUser: s.authMultiUser(),
	}
	if errMsg != "" {
		st.Notice = errMsg
	} else if sent {
		to := "your address"
		if email != "" {
			to = maskEmail(email)
		}
		st.Notice = "A login code was sent to " + to + ". Enter it below."
	}

	page := renderAuthLoginPage(s.SiteRoot, st, s.ParentLevels, r)
	if page == "" {
		log.Printf("auth: no auth-login template reachable from %q — check auth-login.yaml", s.SiteRoot)
		http.Error(w, "Sign-in page unavailable: no auth-login.yaml template found.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(page))
}

// maskEmail turns "someone@example.com" into "s*****e@example.com" for display,
// keeping the domain and the first/last local-part characters as a
// recognizability hint without printing the full address.
func maskEmail(addr string) string {
	at := strings.LastIndexByte(addr, '@')
	if at <= 0 {
		return addr
	}
	local, domain := addr[:at], addr[at:]
	if len(local) <= 2 {
		return string(local[0]) + "***" + domain
	}
	return string(local[0]) + strings.Repeat("*", len(local)-2) + string(local[len(local)-1]) + domain
}

// urlQueryEscape percent-escapes a value for use in a query string. Kept tiny
// and local to avoid a net/url import solely for this.
func urlQueryEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '-' || c == '_' || c == '.' || c == '~' ||
			('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
