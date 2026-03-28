# Security Review — bserver

**Date:** 2026-03-28
**Reviewer:** Claude (automated security analysis)
**Scope:** Full source review of all `.go` files, shell scripts, CI config, and YAML templates.

---

## Executive Summary

bserver is a YAML/Markdown-driven web server with virtual hosting, auto-TLS, PHP-CGI support, and embedded script execution (Python, Node.js, PHP, shell). The architecture is generally well-considered — it drops privileges, applies security headers, rate-limits misbehaving IPs, and restricts file types. However, the design of executing user-authored scripts from YAML definitions introduces several high-severity risks. Below are findings organized by severity.

---

## Critical Findings

### 1. Arbitrary Code Execution via YAML Script Definitions (script.go, datasource.go)

**Severity: CRITICAL**
**Files:** `script.go:122-246`, `datasource.go:54-108`

Any YAML file in a vhost's directory tree can define inline scripts (`script: python`, `script: sh`, etc.) that are executed server-side with the privileges of the bserver process (or `nobody` after privilege drop). This is by design, but creates a critical attack surface:

- **If an attacker can write or modify any `.yaml` file** in a vhost directory (e.g., via a file upload vulnerability in a PHP script, a symlink attack, or compromised credentials), they achieve **full remote code execution**.
- Data source scripts (`$name` definitions in `datasource.go:89`) run the raw user code directly — no wrapper, no sandboxing — via `exec.Command(interpreter, flag, code)`.
- The `renderScript` function in `script.go` similarly executes arbitrary code, only adding a loop wrapper around it.

**Recommendation:**
- Add a config option to disable script execution entirely (default-off for production).
- Consider running scripts in a sandboxed environment (e.g., `nsjail`, `bubblewrap`, or at minimum `setrlimit`).
- Add an explicit allowlist of script files rather than allowing any YAML to trigger execution.

### 2. POST Body Passed via Environment Variable (script.go:106-111)

**Severity: CRITICAL**
**File:** `script.go:106-111`

```go
env = append(env, "_POST_DATA="+string(ctx.postBody))
```

The entire HTTP POST body is placed into an environment variable. Environment variables have OS-level size limits (typically ~128KB on Linux, varies by system). A large POST body will:
- Cause `exec.Command` to fail silently or crash the child process.
- On some systems, excessively large env blocks can cause resource exhaustion.

More critically, **the POST body is not sanitized** — it is passed raw. If any shell script reads environment variables without proper quoting, this enables **command injection**. The shell wrapper (`shScriptWrapper`) doesn't export `_POST_DATA`, but the env var is still available to the child process.

**Recommendation:**
- Pass POST data via **stdin** to the child process instead of an environment variable.
- Enforce a maximum POST body size (e.g., 10MB) at the HTTP handler level.
- At minimum, validate that the env block won't exceed OS limits.

### 3. No Request Body Size Limit (server.go:356-360)

**Severity: HIGH**
**File:** `server.go:356-360`

```go
var bodyBuf bytes.Buffer
if r.Body != nil {
    io.Copy(&bodyBuf, r.Body)
    r.Body.Close()
}
```

The PHP CGI handler reads the entire request body into memory with no size limit. Similarly, `script.go:98-105` reads the full body via `io.ReadAll(r.Body)`. An attacker can send a multi-gigabyte POST request to exhaust server memory and cause a denial of service.

**Recommendation:**
- Use `http.MaxBytesReader` to wrap `r.Body` before reading, e.g.:
  ```go
  r.Body = http.MaxBytesReader(w, r.Body, 10<<20) // 10MB limit
  ```
- Apply this globally in middleware or at each handler entry point.

---

## High Findings

### 4. Path Traversal Risk in Virtual Host Resolution (server.go:137)

**Severity: HIGH**
**File:** `server.go:130-154`

```go
host = strings.ToLower(host)
root := filepath.Join(m.cfg.Base, host)
```

The `Host` header is used directly to construct the filesystem path. While `filepath.Join` normalizes `..`, the `host` value could contain characters like `..` if an attacker sends a crafted `Host` header (e.g., `Host: ../../etc`). The `isKnownVhost` check guards against unknown hosts, but only after the initial `os.Stat(root)` — meaning the **stat itself leaks information** about the filesystem (timing side-channel for directory existence).

Additionally, `isKnownVhost` strips one subdomain label and checks the parent — this could potentially match unintended directories if the base directory contains common names.

**Recommendation:**
- Validate that the resolved `host` string contains no path separators or `..` sequences before using it in `filepath.Join`.
- Add: `if strings.ContainsAny(host, "/\\..") { return 421 }`

### 5. PHP-CGI Header Injection (server.go:416-433)

**Severity: HIGH**
**File:** `server.go:416-433`

The CGI response parser splits headers on `\n` and passes them through to the client via `w.Header().Add(key, val)`. If the PHP script outputs a header with a crafted value containing `\r\n`, this could enable **HTTP response splitting** on older HTTP libraries. While Go's `net/http` typically rejects `\r\n` in header values, the parsing logic itself doesn't validate or sanitize header names or values.

**Recommendation:**
- Validate that header names contain only valid token characters.
- Strip or reject header values containing `\r` or `\n`.
- Consider using Go's `cgi` package or `httputil.ReverseProxy` for proper CGI response handling.

### 6. Reverse Proxy SSRF (server.go:73-128)

**Severity: HIGH**
**File:** `server.go:99-128`

The proxy mode reads a backend URL from `index.yaml`'s `http:` key and creates a reverse proxy to it. If the `http:` value points to an internal service (e.g., `http://169.254.169.254/` for cloud metadata, `http://localhost:6379/` for Redis), bserver becomes an **SSRF proxy**.

```go
if !strings.Contains(backend, "://") {
    backend = "http://" + backend
}
target, err := url.Parse(backend)
```

There is no validation that the backend target is safe.

**Recommendation:**
- Restrict proxy targets to non-loopback, non-link-local, non-private IP ranges.
- Or require explicit allowlisting of proxy targets in `_config.yaml`.

### 7. Script Execution Inherits PATH and HOME (script.go:30-36)

**Severity: MEDIUM-HIGH**
**File:** `script.go:29-36`

```go
if p := os.Getenv("PATH"); p != "" {
    env = append(env, "PATH="+p)
}
if h := os.Getenv("HOME"); h != "" {
    env = append(env, "HOME="+h)
}
```

While the comment says "Only essential variables are passed," inheriting the server's `PATH` means scripts can find and execute any binary on the system. After privilege drop to `nobody`, this is somewhat mitigated, but the attack surface is still broad. `HOME` could point to directories with sensitive files (e.g., `.ssh/`, `.aws/`).

**Recommendation:**
- Set a hardcoded minimal `PATH` (e.g., `/usr/local/bin:/usr/bin:/bin`).
- Set `HOME` to a temporary or restricted directory (e.g., `/tmp` or `/nonexistent`).

---

## Medium Findings

### 8. Debug Mode Exposes Internal State (render.go:97, server.go:446)

**Severity: MEDIUM**
**File:** `server.go:446`, `render.go` (throughout)

Adding `?debug` to any URL enables verbose HTML comments showing:
- Internal file paths (`<!-- resolve "html" from /srv/www/default/html.yaml -->`)
- YAML parse errors with full filesystem paths
- Format definition internals

This is accessible to any external user with no authentication.

**Recommendation:**
- Disable debug mode in production, or gate it behind an environment variable / config flag.
- At minimum, require a secret token: `?debug=<secret>`.

### 9. No CSRF Protection for Script-Backed POST Handlers

**Severity: MEDIUM**

Scripts that handle POST requests (via `$_POST` in PHP, or the `_POST_DATA` env var) have no CSRF protection. Any website can submit forms to a bserver-hosted page and the script will process the data. The `Referrer-Policy` header is set but `Origin` checking is not performed.

**Recommendation:**
- Consider adding SameSite cookie support or CSRF token middleware for POST requests that trigger script execution.

### 10. Race Condition in Cache Invalidation (cache.go:220-258)

**Severity: MEDIUM**
**File:** `cache.go:220-258`

The `handleFileEvent` method collects keys to invalidate under lock, then releases the lock, then re-acquires it per-key to remove entries. Between the unlock and re-lock, new entries could be added for the same keys, leading to stale cache entries surviving invalidation.

```go
rc.mu.Unlock()  // keys collected
// ... window where new Put() can add entries for same keys ...
for _, key := range keysToInvalidate {
    rc.mu.Lock()
    if entry, ok := rc.entries[key]; ok {
        rc.removeLocked(entry)  // removes newly-added entry
    }
    rc.mu.Unlock()
}
```

**Recommendation:**
- Perform all invalidation under a single lock hold, or use a generation counter to distinguish old vs. new entries.

### 11. Self-Signed Certificate Key Material on Disk (server.go:540-600)

**Severity: MEDIUM**
**File:** `server.go:590-592`

```go
_ = os.WriteFile(certFile, certPEM, 0600)
_ = os.WriteFile(keyFile, keyPEM, 0600)
```

Private keys are written to disk with `0600` permissions, which is correct, but:
- The `cert-cache` directory is created with `0700` but errors are ignored.
- After privilege drop to `nobody`, the key files may be readable by the `nobody` user, which is a shared UID on many systems.
- The serial number uses `time.Now().UnixNano()` which is predictable.

**Recommendation:**
- Use `crypto/rand` for serial number generation.
- Ensure the cert-cache directory is owned by root and only readable by the process before privilege drop.

### 12. Markdown Renderer Has Unsafe HTML Enabled (render.go:20-27)

**Severity: MEDIUM**
**File:** `render.go:20-27`

```go
var mdRenderer = goldmark.New(
    goldmark.WithRendererOptions(
        goldmarkhtml.WithUnsafe(),
    ),
)
```

The markdown renderer allows raw HTML passthrough. If any markdown content is user-supplied (e.g., via a CMS, file upload, or user-editable `.md` files), this enables **stored XSS**. Arbitrary `<script>`, `<iframe>`, `<object>` tags will pass through unescaped.

**Recommendation:**
- If markdown files are only author-controlled, document this assumption clearly.
- If user content can reach `.md` files, use a safe HTML sanitizer (e.g., `bluemonday`) after markdown rendering.

---

## Low Findings

### 13. loggingResponseWriter Doesn't Implement All http.ResponseWriter Interfaces

**Severity: LOW**
**File:** `server.go:1031-1039`

`loggingResponseWriter` wraps `http.ResponseWriter` but doesn't forward `http.Hijacker`, `http.Flusher`, or `http.Pusher` interfaces. This can break WebSocket upgrades and SSE streaming through the logging middleware. The rate limiter's `dropResponse` uses `Hijacker` which would fail if the inner writer was already wrapped.

**Recommendation:**
- Implement optional interface forwarding for `Hijacker`, `Flusher`, and `Pusher`.

### 14. Predictable Rate Limiter Behavior (ratelimit.go:159-187)

**Severity: LOW**
**File:** `ratelimit.go:159-187`

The `dropResponse` function uses `math/rand.Intn` (not crypto/rand) for randomization, making the response pattern predictable. The sleep in case 3 (1-3 seconds) ties up a goroutine. An attacker sending many requests could accumulate blocked goroutines in the sleep path.

**Recommendation:**
- Use `crypto/rand` for the randomization if unpredictability matters.
- Avoid sleeping in the handler; prefer immediate connection close.

### 15. No Content-Security-Policy Header (server.go:1052-1059)

**Severity: LOW**
**File:** `server.go:1052-1059`

Security headers include `X-Content-Type-Options`, `X-Frame-Options`, and `Referrer-Policy`, but there is no `Content-Security-Policy` header. A CSP would significantly reduce XSS impact.

**Recommendation:**
- Add a configurable CSP header, at minimum `default-src 'self'` with appropriate overrides for inline styles/scripts used by the templating system.

### 16. install-service.sh Downloads Go Over HTTP Without Checksum Verification

**Severity: LOW**
**File:** `install-service.sh:56-72`

The script downloads a Go tarball from `go.dev` and extracts it to `/usr/local/go` without verifying a SHA256 checksum. If the download were intercepted (e.g., on a compromised network), a malicious Go toolchain could be installed.

**Recommendation:**
- Add SHA256 checksum verification for the downloaded tarball.

---

## Positive Security Observations

The following security practices are already well-implemented:

1. **Privilege dropping** (`server.go:996-1028`): The server correctly drops to `nobody` after binding privileged ports, using the proper `setgroups → setgid → setuid` order.
2. **Security headers** (`server.go:1052-1059`): `X-Content-Type-Options`, `X-Frame-Options`, and `Referrer-Policy` are applied globally.
3. **Rate limiting** (`ratelimit.go`): Escalating penalties with exponential backoff for repeat offenders.
4. **File type allowlist** (`siteconfig.go`): Only whitelisted extensions are served.
5. **Render depth limits** (`render.go:67`): `maxRenderDepth = 50` prevents infinite recursion from circular YAML references.
6. **Cycle detection** (`render.go:89`): The `resolving` map prevents infinite loops.
7. **Parent directory traversal limits** (`render.go:71-72`): `DefaultMaxParentLevels = 1` prevents the YAML resolver from reading files far above docRoot.
8. **Script environment isolation** (`script.go:22-26`): Scripts don't inherit the full server environment.
9. **Server timeouts** (`server.go:808-812`): Read, write, and idle timeouts are configured on both HTTP and HTTPS servers.
10. **Let's Encrypt host policy** (`server.go:778-783`): Only known vhosts can trigger certificate issuance, preventing attackers from exhausting rate limits.
11. **HTML escaping** (`render.go`, `format.go`): User-supplied text content is consistently escaped via `html.EscapeString`.
12. **Script execution timeouts** (`script.go:235-237`): 30-second timeout prevents runaway scripts.

---

## Summary of Recommendations (Priority Order)

| Priority | Finding | Action |
|----------|---------|--------|
| P0 | No request body size limit | Add `http.MaxBytesReader` globally |
| P0 | POST body in env var | Pass via stdin instead; enforce size limits |
| P1 | Script execution has no sandboxing | Add config to disable; consider sandboxing |
| P1 | SSRF via proxy mode | Validate proxy targets against internal ranges |
| P1 | Path traversal in vhost resolution | Validate host has no path separators |
| P2 | Debug mode publicly accessible | Gate behind config flag or secret token |
| P2 | PHP-CGI header injection | Sanitize CGI response headers |
| P2 | Unsafe markdown HTML | Document assumption or add sanitizer |
| P3 | No CSP header | Add configurable Content-Security-Policy |
| P3 | No CSRF protection | Add SameSite / CSRF tokens for POST scripts |
| P3 | Cert serial from timestamp | Use crypto/rand for serial numbers |
