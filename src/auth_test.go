package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuthTokenRoundTrip(t *testing.T) {
	secret := []byte("test-secret-key-01234567890123456789")
	email := "crystal@example.com"
	now := time.Unix(1_700_000_000, 0)
	tok := makeAuthToken(secret, email, now.Add(7*24*time.Hour))

	got, ok := parseAuthToken(secret, tok, now)
	if !ok {
		t.Fatal("freshly minted token should validate")
	}
	if got != email {
		t.Fatalf("token should carry its address: got %q, want %q", got, email)
	}
}

func TestAuthTokenExpiry(t *testing.T) {
	secret := []byte("test-secret-key-01234567890123456789")
	email := "crystal@example.com"
	issued := time.Unix(1_700_000_000, 0)
	tok := makeAuthToken(secret, email, issued.Add(time.Hour))

	if _, ok := parseAuthToken(secret, tok, issued.Add(2*time.Hour)); ok {
		t.Error("expired token must be rejected")
	}
	if _, ok := parseAuthToken(secret, tok, issued.Add(30*time.Minute)); !ok {
		t.Error("token still within lifetime should validate")
	}
}

func TestAuthTokenTampering(t *testing.T) {
	secret := []byte("test-secret-key-01234567890123456789")
	email := "crystal@example.com"
	now := time.Unix(1_700_000_000, 0)
	tok := makeAuthToken(secret, email, now.Add(time.Hour))

	// Wrong signing key.
	if _, ok := parseAuthToken([]byte("different-secret-key-000000000000000"), tok, now); ok {
		t.Error("token signed with a different secret must be rejected")
	}
	// Flipped last byte of the signature.
	bad := tok[:len(tok)-1]
	if tok[len(tok)-1] == 'A' {
		bad += "B"
	} else {
		bad += "A"
	}
	if _, ok := parseAuthToken(secret, bad, now); ok {
		t.Error("token with a tampered signature must be rejected")
	}
	// Structurally broken tokens.
	for _, junk := range []string{"", "no-dot", "a.b.c", ".", "payload."} {
		if _, ok := parseAuthToken(secret, junk, now); ok {
			t.Errorf("malformed token %q must be rejected", junk)
		}
	}
}

// A correctly-signed token is not by itself a session: the address it carries
// must still be approved. This is what makes a ban take effect immediately
// rather than when the cookie eventually expires, and it also stands in for the
// address check that used to live inside the token comparison — a token minted
// for another vhost's user is rejected here unless that address is approved.
func TestAuthCookieRequiresApproval(t *testing.T) {
	dir := t.TempDir()
	usersFile := filepath.Join(dir, "users")
	if err := os.WriteFile(usersFile, []byte("friend@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := siteSettings{
		AuthEmail:     "crystal@example.com",
		AuthSecret:    []byte("test-secret-key-01234567890123456789"),
		AuthUsersFile: usersFile,
	}
	expiry := time.Now().Add(time.Hour)

	req := func(email string) *http.Request {
		r := httptest.NewRequest("GET", "/recipe?list", nil)
		r.AddCookie(&http.Cookie{Name: authCookieName, Value: makeAuthToken(s.AuthSecret, email, expiry)})
		return r
	}

	for _, email := range []string{"crystal@example.com", "friend@example.com"} {
		got, ok := validAuthCookie(req(email), s)
		if !ok {
			t.Errorf("approved address %q should hold a session", email)
		}
		if got != email {
			t.Errorf("session identity: got %q, want %q", got, email)
		}
	}
	if _, ok := validAuthCookie(req("stranger@example.com"), s); ok {
		t.Error("address absent from the users file must not hold a session")
	}

	// Revoking on disk takes effect without a restart, even though the cookie
	// itself is still perfectly valid and unexpired.
	if err := os.WriteFile(usersFile, []byte("# friend removed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := validAuthCookie(req("friend@example.com"), s); ok {
		t.Error("a revoked address must lose its session on the very next request")
	}
	// The owner address is never revocable this way, so the site cannot be
	// locked out by an empty or broken users file.
	if _, ok := validAuthCookie(req("crystal@example.com"), s); !ok {
		t.Error("owner address must keep access regardless of the users file")
	}
}

func TestAuthAllowedNormalizesCase(t *testing.T) {
	dir := t.TempDir()
	usersFile := filepath.Join(dir, "users")
	if err := os.WriteFile(usersFile, []byte("  Friend@Example.COM  \n\n# a comment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := siteSettings{AuthEmail: "Crystal@EXAMPLE.com", AuthUsersFile: usersFile}

	for _, addr := range []string{"friend@example.com", "FRIEND@EXAMPLE.COM", " friend@example.com "} {
		if !authAllowed(s, addr) {
			t.Errorf("%q should match the approved address regardless of case/space", addr)
		}
	}
	if !authAllowed(s, "crystal@example.com") {
		t.Error("owner address should match case-insensitively too")
	}
	if authAllowed(s, "") || authAllowed(s, "# a comment") {
		t.Error("blank lines and comments must not become approved addresses")
	}
}

// A missing users file must fail closed — only the owner may sign in — rather
// than being read as "no restrictions".
func TestAuthUsersFileMissingFailsClosed(t *testing.T) {
	s := siteSettings{AuthEmail: "crystal@example.com", AuthUsersFile: "/nonexistent/nope"}
	if !authAllowed(s, "crystal@example.com") {
		t.Error("owner must still be allowed when the users file is missing")
	}
	if authAllowed(s, "anyone@example.com") {
		t.Error("a missing users file must not admit anyone")
	}
}

// The proxied path (a web shell here) must be reachable only by the addresses
// listed for it, not by everyone who can sign in. This cannot be enforced by the
// app, so it is worth pinning down: a signed-in ordinary user gets 403, a listed
// one gets through.
func TestProxyPathUsersFile(t *testing.T) {
	dir := t.TempDir()
	usersFile := filepath.Join(dir, "users")
	superFile := filepath.Join(dir, "supers")
	if err := os.WriteFile(usersFile, []byte("friend@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(superFile, []byte("crystal@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := siteSettings{
		AuthEmail:      "crystal@example.com",
		AuthSecret:     []byte("test-secret-key-01234567890123456789"),
		AuthUsersFile:  usersFile,
		ProxyPathUsers: superFile,
	}
	// Both are legitimately signed in; only one may reach the backend.
	if !authAllowed(s, "friend@example.com") {
		t.Fatal("precondition: friend should be able to sign in at all")
	}
	if !loadAuthUsers(s.ProxyPathUsers)["crystal@example.com"] {
		t.Error("listed address should be allowed through to the proxy")
	}
	if loadAuthUsers(s.ProxyPathUsers)["friend@example.com"] {
		t.Error("signing in must not by itself grant access to the proxied path")
	}
}

// A site that has not opted into multiple users must keep the original
// one-button login: no address field, and auth-email assumed. Sites with any
// extra approval source get the field, because there the address is a real
// choice. The page renders from www/auth-login.yaml — no HTML lives in Go.
func TestLoginPageAsksForAddressOnlyWhenMultiUser(t *testing.T) {
	base := siteSettings{
		AuthEmail:  "owner@example.com",
		AuthSecret: []byte("test-secret-key-01234567890123456789"),
		SiteRoot:   "www",
	}
	render := func(s siteSettings) string {
		w := httptest.NewRecorder()
		authLoginPage(w, httptest.NewRequest("GET", "/auth/login", nil), s, "")
		return w.Body.String()
	}

	if strings.Contains(render(base), `name="email"`) {
		t.Error("single-recipient site must not ask for an address")
	}
	multi := base
	multi.AuthUsersFile = filepath.Join(t.TempDir(), "users")
	if !strings.Contains(render(multi), `name="email"`) {
		t.Error("multi-user site must ask which address is signing in")
	}
}

// The login dialog is site content, not server code: it renders from
// auth-login.yaml through the YAML pipeline, and the runtime facts reach it as
// pre-seeded $auth definitions — including inside format params.
func TestRenderAuthLoginPage(t *testing.T) {
	page := renderAuthLoginPage("www", authLoginState{
		Next:      "/private/recipes?p=2",
		Email:     "someone@example.com",
		Notice:    "That code was incorrect or expired. Request a new one.",
		NoticeErr: true,
		MultiUser: true,
	}, 0, nil)
	if page == "" {
		t.Fatal("default www/auth-login.yaml should render")
	}
	for _, want := range []string{
		`action="/auth/send"`,
		`action="/auth/verify"`,
		`name="next" value="/private/recipes?p=2"`, // $authnext resolved inside params
		`name="email"`,
		`value="someone@example.com"`,
		"That code was incorrect or expired",
		`class="auth-msg err"`, // $authnoticeclass resolved inside params
	} {
		if !strings.Contains(page, want) {
			t.Errorf("login page missing %q\npage:\n%s", want, page)
		}
	}
	// Before a code is sent, focus belongs on the (multi-user) address field
	// and nowhere else; $authfocuscode stays unseeded so the attribute is
	// dropped from the code input entirely.
	if !strings.Contains(page, `id="email"`) || strings.Count(page, `autofocus=`) != 1 {
		t.Errorf("exactly one field should carry autofocus, page:\n%s", page)
	}

	// Single-user page: no address input of any kind, and no notice block
	// (the .auth-msg CSS rule is always present; the rendered <p> is not).
	single := renderAuthLoginPage("www", authLoginState{Next: "/"}, 0, nil)
	if strings.Contains(single, `name="email"`) {
		t.Error("single-user page must not contain an email input")
	}
	if strings.Contains(single, `class="auth-msg`) {
		t.Error("page without a notice must not render the notice block")
	}
}

// A missing template is a broken install and answers plain text — the server
// itself contains no fallback HTML.
func TestLoginPageWithoutTemplate(t *testing.T) {
	s := siteSettings{
		AuthEmail:  "owner@example.com",
		AuthSecret: []byte("test-secret-key-01234567890123456789"),
		SiteRoot:   t.TempDir(), // no auth-login.yaml anywhere in reach
	}
	w := httptest.NewRecorder()
	authLoginPage(w, httptest.NewRequest("GET", "/auth/login", nil), s, "")
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 without a template, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); strings.Contains(ct, "text/html") {
		t.Errorf("no-template response must not claim to be HTML, got %q", ct)
	}
}

// _auth.yaml is the one-stop control file: settings, inline users, mail text.
func TestAuthFileSettings(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	authYAML := "email: owner@example.com\n" +
		"secret-file: " + secret + "\n" +
		"users:\n  - Friend@Example.com\n" +
		"mail-subject: Your Example code\n" +
		"mail-body: 'Code: $code'\n" +
		"ttl-days: 3\n"
	if err := os.WriteFile(filepath.Join(dir, "_auth.yaml"), []byte(authYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	s := vhostSettings(dir, siteSettings{})
	if s.AuthEmail != "owner@example.com" {
		t.Fatalf("email from _auth.yaml should enable the gate, got %q", s.AuthEmail)
	}
	if s.SiteRoot != dir {
		t.Errorf("SiteRoot should be the docroot, got %q", s.SiteRoot)
	}
	if len(s.AuthSecret) == 0 {
		t.Error("secret should have been created and loaded")
	}
	if s.AuthSMTP != "localhost:25" {
		t.Errorf("default smtp should be localhost:25, got %q", s.AuthSMTP)
	}
	if s.AuthTTL != 3*24*time.Hour {
		t.Errorf("ttl-days should apply, got %v", s.AuthTTL)
	}
	if s.AuthMailSubject != "Your Example code" || s.AuthMailBody != "Code: $code" {
		t.Errorf("mail text should apply, got %q / %q", s.AuthMailSubject, s.AuthMailBody)
	}
	if !s.authMultiUser() {
		t.Error("inline users list should make the site multi-user")
	}
	if !authAllowed(s, "friend@example.com") {
		t.Error("inline users entry should be approved (case-insensitively)")
	}
	if authAllowed(s, "stranger@example.com") {
		t.Error("unlisted address must not be approved")
	}
}

// The allow: hook delegates approval to a script — standing in for a database
// or directory lookup. Exit 0 allows; anything else, including a broken
// script, denies (the owner address never reaches the hook).
func TestAuthAllowScript(t *testing.T) {
	s := siteSettings{
		AuthEmail:       "owner@example.com",
		SiteRoot:        t.TempDir(),
		AuthAllowScript: `test "$AUTH_EMAIL" = "db-user@example.com"`,
	}
	if !s.authMultiUser() {
		t.Error("an allow script should make the site multi-user")
	}
	if !authAllowed(s, "db-user@example.com") {
		t.Error("address the script accepts should be approved")
	}
	if authAllowed(s, "other@example.com") {
		t.Error("address the script rejects must not be approved")
	}
	if !authAllowed(s, "owner@example.com") {
		t.Error("owner must be approved without consulting the script")
	}

	broken := s
	broken.SiteRoot = t.TempDir() // separate verdict-cache namespace
	broken.AuthAllowScript = "exit 3"
	if authAllowed(broken, "db-user@example.com") {
		t.Error("failing script must deny (fail closed)")
	}
}

// The send: hook owns code delivery: it receives the address and the minted
// code in the environment, and the code it received must actually verify.
func TestAuthSendScript(t *testing.T) {
	dir := t.TempDir()
	s := siteSettings{
		AuthEmail:      "owner@example.com",
		AuthFrom:       "noreply@example.com",
		SiteRoot:       dir,
		AuthSendScript: `printf '%s %s' "$AUTH_EMAIL" "$AUTH_CODE" > sent`,
	}
	deleteAuthCode("owner@example.com")
	if err := sendLoginCode(s, "owner@example.com"); err != nil {
		t.Fatalf("send script should succeed: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "sent")) // script cwd is the docroot
	if err != nil {
		t.Fatalf("send script should have written its output: %v", err)
	}
	parts := strings.Fields(string(b))
	if len(parts) != 2 || parts[0] != "owner@example.com" {
		t.Fatalf("script should receive address and code, got %q", b)
	}
	if !checkCode("owner@example.com", parts[1]) {
		t.Error("the code handed to the script should verify")
	}

	failing := s
	failing.AuthSendScript = "echo delivery down >&2; exit 1"
	deleteAuthCode("owner@example.com")
	if err := sendLoginCode(failing, "owner@example.com"); err == nil {
		t.Error("failing send script must surface an error")
	}
}

// The login: section of _auth.yaml can define the dialog inline, overriding
// any auth-login.yaml — everything about a site's auth in one file.
func TestAuthLoginOverlay(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"html.yaml":  "html:\n - body\nbody:\n - main\n",
		"_auth.yaml": "email: owner@example.com\nlogin:\n  auth-login:\n    - h1: Custom Dialog\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	page := renderAuthLoginPage(dir, authLoginState{Next: "/"}, 0, nil)
	if !strings.Contains(page, "Custom Dialog") {
		t.Errorf("login: section should supply the dialog, got:\n%s", page)
	}
}

func TestPlausibleEmail(t *testing.T) {
	ok := []string{"a@b.co", "crystal@example.com", "first.last+tag@sub.example.org"}
	bad := []string{
		"", "nope", "@example.com", "user@", "user@localhost",
		"user@example.com\r\nBcc: victim@example.com", // header injection
		"user name@example.com",
		strings.Repeat("x", 250) + "@example.com", // over length
	}
	for _, a := range ok {
		if !plausibleEmail(a) {
			t.Errorf("%q should be accepted", a)
		}
	}
	for _, a := range bad {
		if plausibleEmail(a) {
			t.Errorf("%q should be rejected", a)
		}
	}
}

// The per-address limits cannot bound a site that accepts many addresses, so
// the global cap is what actually stops the endpoint relaying mail. Rotating
// addresses must hit it.
func TestIssueCodeGlobalCap(t *testing.T) {
	// The code store and the global send log are package-level, so this test
	// both starts from a clean slate and restores one — otherwise filling the
	// hourly cap here would starve every later test in the package.
	authCodesMu.Lock()
	prevCodes, prevLog := authCodes, authSendGlobalLog
	authCodes = map[string]*authCodeEntry{}
	authSendGlobalLog = nil
	authCodesMu.Unlock()
	t.Cleanup(func() {
		authCodesMu.Lock()
		authCodes, authSendGlobalLog = prevCodes, prevLog
		authCodesMu.Unlock()
	})

	sent := 0
	for i := 0; i < authSendGlobal+10; i++ {
		if _, err := issueCode(fmt.Sprintf("user%d@example.com", i)); err == nil {
			sent++
		}
	}
	if sent != authSendGlobal {
		t.Errorf("global cap: sent %d codes, want exactly %d", sent, authSendGlobal)
	}
}

func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"/recipe?list":        "/recipe?list",
		"/":                   "/",
		"":                    "/",
		"//evil.com":          "/", // protocol-relative open redirect
		"https://evil.com":    "/", // absolute URL
		"javascript:alert(1)": "/", // scheme injection
		"/a/b/c":              "/a/b/c",
		"/\\evil.com":         "/", // browsers normalize \ to / => //evil.com
		"/\\/evil.com":        "/", // same, with an extra slash
		"/a\\b":               "/", // backslash anywhere is rejected
		"/a\r\nSet-Cookie: x": "/", // control bytes rejected
	}
	for in, want := range cases {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCodeIssueAndCheck(t *testing.T) {
	email := "codetest@example.com" // unique key so other tests don't interfere
	deleteAuthCode(email)

	code, err := issueCode(email)
	if err != nil {
		t.Fatalf("issueCode: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("expected 6-digit code, got %q", code)
	}
	if checkCode(email, "000000") && code != "000000" {
		t.Error("a wrong code should not verify")
	}
	if !checkCode(email, code) {
		t.Error("the correct code should verify")
	}
	if checkCode(email, code) {
		t.Error("a code must be single-use (rejected after first success)")
	}
}

func TestCodeSendRateLimit(t *testing.T) {
	email := "ratetest@example.com"
	deleteAuthCode(email)

	if _, err := issueCode(email); err != nil {
		t.Fatalf("first issue should succeed: %v", err)
	}
	// A second immediate request is throttled by the minimum-gap rule.
	if _, err := issueCode(email); err == nil {
		t.Error("a second immediate code request should be rate-limited")
	} else if !strings.Contains(err.Error(), "wait") {
		t.Errorf("unexpected rate-limit error: %v", err)
	}
}

func TestMaskEmail(t *testing.T) {
	cases := map[string]string{
		"crystal@example.com": "c*****l@example.com",
		"ab@x.com":            "a***@x.com",
		"a@x.com":             "a***@x.com",
	}
	for in, want := range cases {
		if got := maskEmail(in); got != want {
			t.Errorf("maskEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

// substituteVarTokens must match whole tokens: one name being a prefix of
// another ($authnotice / $authnoticeclass) can never corrupt the longer one,
// and unresolved tokens stay in place.
func TestSubstituteVarTokens(t *testing.T) {
	resolve := func(name string) (string, bool) {
		switch name {
		case "authnotice":
			return "N", true
		case "authnoticeclass":
			return "C", true
		}
		return "", false
	}
	if got := substituteVarTokens("msg $authnoticeclass and $authnotice", resolve); got != "msg C and N" {
		t.Errorf("token substitution got %q", got)
	}
	if got := substituteVarTokens("keep $unknown intact, $ too", resolve); got != "keep $unknown intact, $ too" {
		t.Errorf("unresolved tokens should stay put, got %q", got)
	}
}

// The signed-in identity must reach inline YAML scripts the same way it
// reaches php-cgi: as REMOTE_USER, set from the request context the auth gate
// populated — never from a client header, which arrives as HTTP_REMOTE_USER.
func TestScriptEnvRemoteUser(t *testing.T) {
	newCtx := func(r *http.Request) *renderContext {
		return &renderContext{
			docRoot:     t.TempDir(),
			requestURI:  "/page",
			httpRequest: r,
			defs:        make(map[string]interface{}),
			filesLoaded: make(map[string]bool),
		}
	}
	hasRemoteUser := func(env []string) (string, bool) {
		for _, e := range env {
			if v, ok := strings.CutPrefix(e, "REMOTE_USER="); ok {
				return v, true
			}
		}
		return "", false
	}

	r := httptest.NewRequest("GET", "/page", nil)
	r.Header.Set("Remote-User", "spoofed@example.com") // must not become REMOTE_USER
	if v, ok := hasRemoteUser(newCtx(r).buildScriptEnv("")); ok {
		t.Errorf("unauthenticated request must not export REMOTE_USER, got %q", v)
	}

	signed := r.WithContext(context.WithValue(r.Context(), authUserKey{}, "friend@example.com"))
	env := newCtx(signed).buildScriptEnv("")
	if v, ok := hasRemoteUser(env); !ok || v != "friend@example.com" {
		t.Errorf("signed-in request should export REMOTE_USER=friend@example.com, got %q (present=%v)", v, ok)
	}
	for _, e := range env {
		if strings.HasPrefix(e, "HTTP_REMOTE_USER=") && !strings.HasPrefix(e, "HTTP_REMOTE_USER=spoofed") {
			t.Errorf("unexpected header mangling: %s", e)
		}
	}
}

// An _auth.yaml in a direct subdirectory becomes a realm gating only that
// subtree, with its own cookie; a docroot file adds a whole-vhost realm and
// the most specific scope governs each path.
func TestAuthFileInSubdirectory(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.MkdirAll(filepath.Join(dir, "log"), 0o755); err != nil {
		t.Fatal(err)
	}
	authYAML := "email: owner@example.com\nsecret-file: " + secret + "\n"
	if err := os.WriteFile(filepath.Join(dir, "log", "_auth.yaml"), []byte(authYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	s := vhostSettings(dir, siteSettings{})
	if len(s.AuthRealms) != 1 {
		t.Fatalf("want one realm, got %d", len(s.AuthRealms))
	}
	realm := authRealmFor("/log/entry.json", s)
	if realm == nil || realm.broken {
		t.Fatal("paths under the subdirectory should be governed by its realm")
	}
	if realm.settings.AuthEmail != "owner@example.com" {
		t.Fatalf("realm should carry the subdirectory's owner, got %q", realm.settings.AuthEmail)
	}
	if got := realm.settings.authCookie(); got != "bs_auth-log" {
		t.Fatalf("subtree realm should issue its own cookie, got %q", got)
	}
	if authRealmFor("/log", s) == nil {
		t.Error("the subdirectory itself should be in scope")
	}
	for _, p := range []string{"/", "/index.php", "/logs"} {
		if authRealmFor(p, s) != nil {
			t.Errorf("%s must not be governed by any realm", p)
		}
	}

	// A docroot _auth.yaml adds a whole-vhost realm; the subtree realm keeps
	// governing its own directory because the most specific scope wins.
	if err := os.WriteFile(filepath.Join(dir, "_auth.yaml"),
		[]byte("email: root@example.com\nsecret-file: "+secret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s = vhostSettings(dir, siteSettings{})
	if len(s.AuthRealms) != 2 {
		t.Fatalf("want two realms, got %d", len(s.AuthRealms))
	}
	if s.AuthEmail != "root@example.com" {
		t.Fatalf("docroot _auth.yaml should gate the vhost, got %q", s.AuthEmail)
	}
	if realm := authRealmFor("/anything", s); realm == nil || realm.settings.AuthEmail != "root@example.com" {
		t.Error("paths outside the subtree belong to the docroot realm")
	}
	if realm := authRealmFor("/log/entry.json", s); realm == nil || realm.settings.AuthEmail != "owner@example.com" {
		t.Error("the subtree realm must keep governing its directory")
	}
}

// Two subdirectories each carrying an _auth.yaml operate as independent
// realms: separate owners, separate signing secrets, separate cookies — a
// session in one buys nothing in the other.
func TestAuthMultipleSubdirRealms(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"admin", "reports"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
		y := "email: " + sub + "@example.com\nsecret-file: " + filepath.Join(dir, sub+".secret") + "\n"
		if err := os.WriteFile(filepath.Join(dir, sub, "_auth.yaml"), []byte(y), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := vhostSettings(dir, siteSettings{})
	if len(s.AuthRealms) != 2 {
		t.Fatalf("want two realms, got %d", len(s.AuthRealms))
	}
	admin := authRealmFor("/admin/x", s)
	reports := authRealmFor("/reports/y", s)
	if admin == nil || reports == nil || admin.broken || reports.broken {
		t.Fatal("both subtrees must be governed by operating realms")
	}
	if admin.settings.AuthEmail != "admin@example.com" || reports.settings.AuthEmail != "reports@example.com" {
		t.Error("each realm should carry its own owner")
	}
	if admin.settings.authCookie() == reports.settings.authCookie() {
		t.Error("realms must issue distinct cookies")
	}

	// A valid admin-realm session presented to the reports realm is worthless:
	// different cookie name, different secret, different approved set.
	r := httptest.NewRequest("GET", "/reports/y", nil)
	tok := makeAuthToken(admin.settings.AuthSecret, "admin@example.com", time.Now().Add(time.Hour))
	r.AddCookie(&http.Cookie{Name: admin.settings.authCookie(), Value: tok})
	if _, ok := validAuthCookie(r, reports.settings); ok {
		t.Error("one realm's session must not unlock another realm")
	}
	if _, ok := validAuthCookie(r, admin.settings); !ok {
		t.Error("the session must remain valid for its own realm")
	}
}

// An _auth.yaml that cannot be operated — or one nested deeper than the gate
// searches — must fail closed: its subtree is refused outright, never served
// open.
func TestAuthUnoperatedFileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	// Broken: an owner but no secret-file, so the realm cannot sign cookies.
	if err := os.MkdirAll(filepath.Join(dir, "private"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "private", "_auth.yaml"),
		[]byte("email: owner@example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Nested too deep: realms are only built one directory level down.
	if err := os.MkdirAll(filepath.Join(dir, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "b", "_auth.yaml"),
		[]byte("email: deep@example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := vhostSettings(dir, siteSettings{})
	realm := authRealmFor("/private/x", s)
	if realm == nil || !realm.broken {
		t.Error("an unusable _auth.yaml should yield a broken realm, not an open subtree")
	}
	if !authSubtreeDenied(dir, "/private/x", s) {
		t.Error("the broken realm's subtree must be refused")
	}
	if !authSubtreeDenied(dir, "/a/b/x", s) || !authSubtreeDenied(dir, "/a/b", s) {
		t.Error("a nested _auth.yaml's subtree must be refused")
	}
	for _, p := range []string{"/", "/a", "/a/x", "/open/file"} {
		if authSubtreeDenied(dir, p, s) {
			t.Errorf("%s should not be refused", p)
		}
	}
}

// A symlinked subdirectory behaves exactly like a real one: its _auth.yaml is
// found and operated, so content served through the link is gated rather than
// leaked (ReadDir alone would miss it — the entry is not a directory).
func TestAuthSymlinkedSubdir(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "_auth.yaml"),
		[]byte("email: owner@example.com\nsecret-file: "+filepath.Join(target, "secret")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(target, filepath.Join(dir, "vault")); err != nil {
		t.Fatal(err)
	}
	s := vhostSettings(dir, siteSettings{})
	realm := authRealmFor("/vault/x", s)
	if realm == nil || realm.broken || realm.settings.AuthEmail != "owner@example.com" {
		t.Fatal("a symlinked subdirectory's _auth.yaml must operate like a real one")
	}
	if authSubtreeDenied(dir, "/vault/x", s) {
		t.Error("an operating symlinked realm serves gated content, it is not refused")
	}
}
