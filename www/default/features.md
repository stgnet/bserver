# Server Features

bserver includes several production-oriented features that work automatically
without configuration.

## Virtual Host Resolution

bserver uses the HTTP `Host` header to resolve requests to virtual host
directories under the `www/` base directory. Each directory name corresponds
to a domain.

### Resolution Order

1. **Direct match** — if `www/<hostname>` exists as a directory (or symlink),
   the request is served from there.
2. **Known vhost fallback** — if the hostname is one subdomain deeper than a
   known vhost directory (e.g., `www.example.com` when `www/example.com`
   exists), the request is served from `www/default`.
3. **Unknown domain rejection** — if the hostname doesn't match any vhost
   directory and isn't one subdomain deeper than one, the server responds
   with `421 Misdirected Request`. The request is not served.

This means `www.example.com` and `api.example.com` automatically work when
you create `www/example.com`, but deeply nested bogus domains like
`update.update.update.m.example.com` are rejected immediately.

For domains that need more than one level of subdomains, create a symlink:
```
cd www && ln -s example.com deep.sub.example.com
```

### The `default` Directory

The `www/default` directory serves as a fallback for known vhosts that don't
have their own directory. For example, if `www/example.com` exists and
someone visits `www.example.com`, the request is served from `www/default`
(since `www/www.example.com` doesn't exist, but `www.example.com` is one
level deeper than the known `example.com` vhost).

If the `default` directory doesn't exist, requests that would fall back to
it receive a `404 Not Found`.

## Path-Embedded GET Arguments

Any page script can receive GET arguments as path segments instead of a
query string. These two URLs are equivalent:

```
https://example.com/confirm?token=abc123
https://example.com/confirm/token/abc123
```

This works automatically for every `.yaml`, `.md`, and `.php` page — no
configuration required. It exists because email security scanners and link
rewriters sometimes mangle or refuse to forward `?name=value` query
strings; the path form survives them intact.

### How It Works

When a URL doesn't resolve to any file or directory, bserver looks for the
longest leading portion of the path that resolves to a page script —
exactly as if that portion had been requested directly (`confirm` finds
`confirm.yaml`, `confirm.md`, or `confirm.php`, a directory index, or the
`dir/dir.yaml` naming convention). The remaining segments are taken
pairwise as `name/value` arguments and passed to the script just as
`?name=value` would be:

```
/report/year/2026/month/7      →  /report?year=2026&month=7
/subdir/confirm/token/abc123   →  /subdir/confirm?token=abc123
/search/q                      →  /search?q        (odd trailing segment)
```

A real query string can be combined with path arguments; the path pairs
are appended after it. The arguments are passed blindly — if the script
ignores them (or there is no script in the YAML at all), the page still
renders normally; extra arguments are never an error.

### Notes

- Only page-generating scripts (`.yaml`, `.md`, `.php`) are matched.
  Static files like `.html` or images never receive path arguments, so
  argument URLs can't alias static content.
- The script portion of the URL is still subject to the blocked-paths
  rules below; argument *values* that merely look like blocked names
  (e.g. a token starting with `_`) are fine.
- Values are URL-decoded path segments, so a value cannot contain a
  literal `/`. Use a regular query string for values that need one.
- Requests with arguments are treated as dynamic and bypass the render
  cache, same as any request with a query string.

## Render Cache

bserver caches rendered YAML and markdown pages in memory. When the same page
is requested again, the cached HTML is served directly without re-rendering.
This significantly reduces CPU usage for sites with many visitors.

### How It Works

- Only **rendered output** is cached (YAML and markdown pages). Static files
  served directly from disk (images, CSS, JavaScript) are not cached in memory.
- Each cache entry records the list of source files that were loaded during
  rendering (the page itself, html.yaml, navbar.yaml, style.yaml, etc.).
- When any source file changes on disk, all cache entries that depend on it
  are automatically invalidated via filesystem notifications (fsnotify/inotify).
- New files created in watched directories also trigger invalidation, since a
  new file might change YAML name resolution order.
- Debug mode (`?debug`) bypasses the cache entirely.

### Cache Eviction

Entries are evicted in three ways:

1. **File change** — fsnotify detects a source file was modified, created,
   renamed, or deleted.
2. **Age expiry** — entries older than the configured max age are discarded
   on the next access (default: 15 minutes).
3. **Size pressure** — when total cache size exceeds the limit, the least
   recently used entries are evicted first (LRU).

### RAM Detection

At startup, bserver checks available system memory on Linux by reading
`/proc/meminfo`. If available RAM is limited, the cache size is automatically
reduced:

- **No swap**: cache limited to 25% of available RAM
- **With swap**: cache limited to 50% of available RAM

A warning is logged when the effective cache size is lower than the configured
maximum. On non-Linux platforms, the configured maximum is used as-is.

### Configuration

These settings go in `_config.yaml` (in the www directory):

| Setting | Default | Description |
|---------|---------|-------------|
| `cache-size` | `1024` | Maximum cache size in MB (0 to disable) |
| `cache-age` | `900` | Maximum entry age in seconds (15 minutes; 0 to disable) |
| `static-age` | `86400` | Maximum Cache-Control age for static files in seconds (24 hours) |
| `max-body-size` | `10` | Maximum request body size in MB (0 to disable) |

Set `cache-size: 0` to disable caching entirely (server-wide).

### Disabling Caching Per Vhost

`cache-age` is a per-site setting, so a single virtual host can opt out of
caching without affecting the rest of the server. Set `cache-age: 0` in that
vhost's `_config.yaml` (e.g. `www/example.com/_config.yaml`):

```yaml
cache-age: 0
```

Pages for that vhost are then rendered fresh on every request — nothing is
stored in or served from the render cache — and responses carry
`Cache-Control: no-store` so browsers and proxies don't cache them either.
Useful for sites whose pages must always reflect live data.

Set `max-body-size: 0` to allow unlimited request bodies (not recommended).
The request body is always piped to scripts on stdin, so the practical limit
is set by `max-body-size` rather than by OS environment-variable limits.

## Cache-Control Headers

bserver sets `Cache-Control` headers on all responses to help browsers and
proxies cache content efficiently.

### Rendered Pages

YAML and markdown pages receive a `Cache-Control: public, max-age=N` header
where N matches the `cache-age` setting (default 900 seconds / 15 minutes).
This tells browsers to reuse the page without re-requesting it for that
duration.

### Static Files

For static files (images, CSS, JavaScript, fonts, etc.), bserver uses a
heuristic based on the file's last modification time:

- **max-age = half the file's age**, capped at `static-age` (default 24 hours)
- **Minimum 60 seconds** for very recently modified files

For example, a CSS file last modified 2 hours ago gets `max-age=3600` (1 hour).
A logo image unchanged for 30 days gets `max-age=86400` (24 hours, the cap).

This approach means frequently-updated files are re-checked sooner, while
stable files are cached longer.

## TLS Certificate Management

bserver automatically manages TLS certificates for HTTPS. To protect against
bogus domains exhausting Let's Encrypt rate limits, certificate requests are
restricted to known virtual hosts.

### Which Domains Get Let's Encrypt Certificates

A domain qualifies for a Let's Encrypt certificate only if it passes the
same known-vhost check used for request routing:

1. **Direct match** — a directory exists at `www/<domain>` (e.g., `www/example.com`)
2. **One subdomain deeper** — the parent domain has a directory (e.g.,
   `www.example.com` works when `www/example.com` exists)

Deeply nested bogus domains like `a.b.c.d.example.com` are rejected without
contacting Let's Encrypt.

### Domains Without a Virtual Host

Requests to unknown domains are rejected at two levels:

1. **TLS layer** — a self-signed certificate is returned (no Let's Encrypt
   request is made), preventing bogus domains from exhausting LE rate limits.
2. **HTTP layer** — the server responds with `421 Misdirected Request`
   without serving any content. This 421 counts as an error for the rate
   limiter, so persistent scanners are blocked after 10 attempts.

### Private and Non-Public Domains

IP addresses and domains with non-public suffixes (`.local`, `.test`,
`.internal`, etc.) always get self-signed certificates without contacting
Let's Encrypt.

## Blocked Paths

bserver refuses to serve certain paths regardless of the allowed `types`,
because they commonly leak source code or secrets. Two categories are blocked
by default:

- **Hidden files and directories** — any path segment beginning with a dot,
  such as `/.git/`, `/.env`, `/.htaccess`, or `/.svn/`. This matters because
  extensionless files (e.g. `.git/index`, `.git/HEAD`) would otherwise bypass
  the `types` allow-list and expose a fetchable copy of the repository.
- **`vendor` directories** — dependency trees at any depth (`/vendor/...`,
  `/app/vendor/...`), which should never be web-served.

The `.well-known` directory is always exempt so ACME challenges and
`security.txt` keep working. A blocked request returns a plain `404` and does
not confirm whether the file exists.

### Customizing

Two `_config.yaml` keys (server-wide in `www/_config.yaml`, or per-vhost) let
you adjust the defaults. They also accept the `BLOCK_PATHS` / `ALLOW_PATHS`
environment variables (comma-separated).

| Setting | Description |
|---------|-------------|
| `block-paths` | Additional paths to deny, beyond the built-in defaults |
| `allow-paths` | Paths to expose, overriding the defaults and `block-paths` |

Each entry is matched against the request path one of two ways:

- A **bare name** (no slash, e.g. `vendor`) matches that directory at **any
  depth**. Matching is segment-aware, so `vendor` never matches `/vendored`.
- A **rooted prefix** (a leading slash or multiple segments, e.g. `/vendor` or
  `vendor/public`) matches only from the document root.

`allow-paths` always wins over the defaults and `block-paths`, so you can
expose a single subtree while keeping the rest blocked:

```yaml
# www/example.com/_config.yaml
allow-paths:
  - /vendor/public      # serve this subtree...
block-paths:
  - private             # ...block any "private" dir, and
  - /internal/secrets   # ...this exact rooted path
```

## Vhost Authentication

A virtual host can be placed behind a passwordless login by creating an
`_auth.yaml` in its document root — one file that controls everything about the
gate. Like `_config.yaml`, the underscore prefix means it can never be served
as web content. When `email:` is set, the host stays reachable from any IP, but
every path outside the always-public set (the splash page `/`, `/favicon.ico`,
`/robots.txt`, anything under `/.well-known/`, the `/auth/*` login endpoints,
and any prefixes you add via `public:`) requires a valid session cookie.

The gate can also cover just part of a site: place an `_auth.yaml` in a
direct subdirectory instead of the docroot (e.g. `log/_auth.yaml`) and only
that subtree requires sign-in — the rest of the vhost stays open. Several
subdirectories can each carry their own `_auth.yaml`; every file is an
independent realm with its own owner, approved users, signing secret and
session cookie, so signing in to one grants nothing in another. A docroot
file and subdirectory files can coexist — the most specific one governs each
path.

An `_auth.yaml` that exists but cannot be operated fails closed: a file
missing its `email:` or `secret-file:`, or one nested deeper than a direct
subdirectory, causes every request into its subtree to be refused (with a
log message saying why) rather than served open.

A visitor signs in by requesting a one-time 6-digit code, which is delivered to
an approved address, and entering it. On success bserver sets `bs_auth` — an
HMAC-signed cookie carrying the address and an expiry — so the session is
verified statelessly on every request and survives restarts. The cookie lasts
`ttl-days` (default 7) before a fresh code is needed. `/auth/logout` clears it.
Codes expire after 10 minutes, are single-use, are burned after 5 wrong
guesses, and are rate-limited (per address and globally).

| Setting | Description |
|---------|-------------|
| `email` | Owner address. **Presence enables the gate.** Always allowed to sign in, so a broken users list can never lock the site out. |
| `secret-file` | Path (keep it **outside** the webroot) to the cookie-signing key. Auto-created `0600` with 32 random bytes if missing, and persisted so cookies survive restarts. **Required** — the gate stays off if it is missing or unusable. |
| `users` | Inline list of additional approved addresses. |
| `users-file` | File of additional approved addresses (one per line, `#` comments). Your app can maintain it; removing an address revokes on the next request. |
| `allow` | Shell script deciding whether an address may sign in — e.g. a database lookup. Runs with `AUTH_EMAIL` in the environment; exit `0` allows. Verdicts are cached for a minute. |
| `send` | Shell script that delivers the code, replacing the built-in mailer — e.g. local sendmail, an SMS gateway, an API call. Runs with `AUTH_EMAIL`, `AUTH_CODE`, `AUTH_FROM`. |
| `smtp` | SMTP relay `host:port` for the built-in mailer (default `localhost:25`). Plain SMTP to a trusted relay — no auth/STARTTLS is attempted. |
| `from` | `From`/envelope sender for the code email (default = `email`). |
| `mail-subject`, `mail-body` | Text of the code email; `$code` in the body is replaced with the code. |
| `public` | Extra always-public path prefixes, beyond the built-in set. |
| `ttl-days` | Session cookie lifetime in days (default `7`). |
| `login` | Inline page definitions for the sign-in dialog (see below). |

```yaml
# www/example.com/_auth.yaml
email: owner@example.com
secret-file: /var/lib/bserver-secrets/example-auth   # not under the webroot
users:
  - friend@example.com
allow: ./check-user.sh          # optional: ask a script/database instead
send: |                         # optional: deliver the code yourself
  printf 'Subject: Your code\n\nCode: %s\n' "$AUTH_CODE" | sendmail "$AUTH_EMAIL"
```

(The original `auth-email`, `auth-smtp`, `auth-from`, `auth-secret-file`,
`auth-public`, `auth-ttl-days`, and `auth-users-file` keys in `_config.yaml`
still work; `_auth.yaml` values override them.)

When more than one address can sign in (any of `users`, `users-file`, or
`allow` is configured), the login form asks which address is signing in and
refuses to send codes to unapproved ones. The verified address reaches PHP
apps as `$_SERVER['REMOTE_USER']`, and `proxy-path-users-file` in
`_config.yaml` can restrict a proxied path to specific signed-in users.

### The sign-in dialog is a page, not server code

`/auth/login` renders the `auth-login` definition through the normal YAML
pipeline — no HTML for it lives in the server. The default is
`www/auth-login.yaml`; a site overrides it with its own `auth-login.yaml`, or
inline under `login:` in its `_auth.yaml`. The [login demo](/login) on this
site renders the default dialog with sample values so you can see what you are
restyling. The template's header comments document the `$auth...` values the
server seeds (redirect target, address, notices, focus).

The gate runs before proxy handling and before the render cache is consulted, so
a path-based reverse proxy (e.g. a `/terminal/` web shell) is covered too, and a
cached page is never served to an unauthenticated client. The `bs_auth` cookie
is `HttpOnly`, `Secure`, and `SameSite=Lax`, so the vhost must be served over
HTTPS (the default) for logins to stick.

## Security Headers

Every response includes these security headers automatically:

| Header | Value | Purpose |
|--------|-------|---------|
| `X-Content-Type-Options` | `nosniff` | Prevents browsers from MIME-sniffing |
| `X-Frame-Options` | `SAMEORIGIN` | Blocks framing by other sites (clickjacking protection) |
| `Referrer-Policy` | `strict-origin-when-cross-origin` | Limits referrer information sent to other origins |

These are applied as middleware, so they cover all responses including static
files, rendered pages, error pages, and PHP output.

## Request Logging

Every HTTP request is logged with the client IP address, hostname, HTTP
method, path, response status code, and duration:

```
203.0.113.42 example.com GET / 200 12ms
203.0.113.42 example.com GET /about 200 3ms
198.51.100.7 example.com GET /missing 404 1ms
```

The IP address is extracted from the TCP connection source (`RemoteAddr`).
This makes it easy to identify repeated requests from the same source,
spot scanning patterns, and correlate with rate limiting events.

Cached responses are typically much faster than first renders, making it easy
to spot cache misses in the logs.

## Rate Limiting

bserver automatically rate-limits IP addresses that make too many consecutive
failed requests (status 400 or higher). This protects against scanning,
fishing, and brute-force attacks without affecting normal traffic.

### How It Works

1. Every response is tracked per client IP address.
2. Each error response (4xx or 5xx) increments a consecutive error counter
   for that IP.
3. Any successful response (2xx or 3xx) resets the counter to zero.
4. When an IP accumulates **10 consecutive errors**, it is blocked.

This means legitimate users who occasionally hit a 404 are unaffected — a
single successful page view resets the counter entirely.

### Blocked Requests

When a blocked IP sends a request, the server skips all normal request
processing (no routing, no rendering, no file I/O) and responds with a
minimal drop response using one of several randomized strategies:

- Close the connection immediately
- Return a bare `429 Too Many Requests`
- Return a bare `503 Service Unavailable`
- Delay briefly then close the connection

The randomized responses are designed to confuse automated scanners and
make it difficult for attackers to distinguish between a block and a
genuine server issue. Only the first blocked request is logged (with
"dropped" in place of the status code) to avoid flooding the log.

### Escalating Penalties

Each time an IP is blocked, the penalty duration doubles:

| Offense | Block Duration |
|---------|---------------|
| 1st     | 10 minutes    |
| 2nd     | 20 minutes    |
| 3rd     | 40 minutes    |
| 4th     | 80 minutes    |
| ...     | ...           |
| 9th+    | ~42 hours (cap) |

The penalty level is preserved across blocks, so a persistent attacker
faces increasingly long timeouts. The penalty history is cleared when
the IP has been idle for at least 1 hour after its block expires.

### Example Log Output

A scanning attack against a known vhost (error paths on a valid domain):

```
198.51.100.7 example.com POST /webhook/upload 404 106ms
198.51.100.7 example.com POST /webhook/files 404 109ms
...
198.51.100.7 rate-limited after 10 consecutive errors (penalty: 10m0s)
198.51.100.7 example.com POST /webhook/batch dropped
```

A scanning attack using bogus domains (rejected at the vhost level):

```
198.51.100.7 bogus.update.m.example.com GET / 421 0s
198.51.100.7 bogus.update.m.example.com GET /admin 421 0s
...
198.51.100.7 rate-limited after 10 consecutive errors (penalty: 10m0s)
198.51.100.7 bogus.update.m.example.com GET / dropped
```

Only the first dropped request is logged; subsequent drops from the same
IP during the same penalty period are silently discarded.

## HTTP to HTTPS Redirect

When HTTPS is active (port 443 is available), all HTTP requests are
automatically redirected to HTTPS with a `308 Permanent Redirect` status.
The only exception is ACME HTTP-01 challenge requests from Let's Encrypt,
which are handled on port 80 to complete certificate issuance.

When HTTPS is not available (port 443 cannot be bound), HTTP serves
requests directly with the full middleware chain (logging, security
headers, rate limiting).

## Privilege Dropping

After binding to privileged ports (80 and 443), bserver drops privileges
to the `nobody` user. This limits the impact of any potential security
vulnerability — even if the server process is compromised, it runs with
minimal filesystem and system permissions.

Privilege dropping is automatic and logged at startup:
```
Dropped privileges to nobody (UID=65534 GID=65534)
```

If the `nobody` user doesn't exist or privilege dropping fails for any
reason, the server continues as the current user and logs a warning.

## Port Fallback

If port 80 is unavailable (e.g., another process is using it, or the
server is running without root privileges), bserver automatically tries
alternative ports 8000 through 8099 and uses the first available one.

This makes it easy to run bserver in development without `sudo`:
```
Warning: cannot listen on :80 (trying alternative ports)
Using alternative HTTP port: :8000
```

If port 443 is unavailable, HTTPS is disabled and the server runs
HTTP-only. A warning is logged but the server continues normally.

## Graceful Shutdown

bserver handles `SIGINT` (Ctrl+C) and `SIGTERM` signals gracefully:

1. Stops accepting new connections
2. Waits up to 10 seconds for in-flight requests to complete
3. Closes the render cache and file watchers
4. Exits cleanly

This means deployments using `systemctl restart` or container orchestrators
won't drop active requests.

## Allowed File Types

bserver only serves files whose extension appears in the allowed-types
list. Requests for unlisted extensions return `404` even if the file
exists. This stops accidental exposure of configuration, secrets, or
backup files (`.env`, `.json`, `.sql`, `.log`, etc.).

The default list is permissive enough for typical web content (HTML, CSS,
JS, images, fonts, audio, video, PDF, etc.). To customize, set `types:`
in `_config.yaml` or the `TYPES` environment variable:

```yaml
# www/_config.yaml or www/example.com/_config.yaml
types:
  - yaml
  - md
  - png
  - svg
  - css
  - js
```

The `types` setting can be overridden per-vhost.

## Per-vhost HTTP

`_config.yaml` accepts `allow-http: true` on a per-vhost basis. When set,
the vhost is served over plain HTTP instead of being redirected to HTTPS.
This is intended for constrained clients (IoT devices, embedded systems)
that cannot do TLS. A warning is logged because session cookies and other
secrets may transit in cleartext.

```yaml
# www/iot.example.com/_config.yaml
allow-http: true
```

## Favicons

Every vhost gets a `/favicon.ico` even without an `favicon.ico` file. By
default, bserver generates one on the fly using the first three letters
of the domain name, white on black. Drop a real `favicon.ico` into the
vhost's document root to use that instead.

To customize the generated icon, add a `_favicon.yaml` to the vhost root:

```yaml
# Text mode
text: ABC
color: white
background: navy
```

```yaml
# Image mode — scales an image to a square ICO
image: logo.png
fit: contain   # contain | crop | stretch
```

The generated icons are cached in memory and regenerated when
`_favicon.yaml` (or the source image) changes on disk.

## Debug Mode

Append `?debug` to any URL to emit HTML comments throughout the rendered
output that trace name resolution, format selection, and rendering depth.

For production, set `debug-token` in `_config.yaml` (or the `DEBUG_TOKEN`
environment variable) to require a secret: `?debug=<token>`. With a token
configured, the bare `?debug` no longer works — a constant-time compare
gates access. With no token configured, `?debug` is open (development
default).

## JS Heap Cap

Each embedded-JavaScript invocation has a soft heap-growth cap to protect
against runaway scripts:

```yaml
# www/_config.yaml
js-heap-mb: 128   # 0 disables the check
```

The cap is sampled every 100 ms; a single huge allocation between probes
can still escape, so a cgroup or other deployment-level memory limit is a
useful belt-and-braces backstop on production hosts.

## Memory Monitor

bserver includes a built-in memory monitor that logs heap, goroutine, and
cache statistics on an interval and writes a pprof heap dump when growth
exceeds a threshold. The relevant `_config.yaml` keys (all optional, all
have sensible defaults):

- `mem-log-interval` — seconds between status log entries
- `mem-heap-threshold-mb` — heap size that triggers a warning
- `mem-goroutine-threshold` — goroutine count that triggers a warning
- `mem-growth-mb-per-5min` — growth rate that triggers a pprof dump
- `mem-dump-dir` — where to write pprof dumps (empty = disabled)
- `mem-dump-cooldown-min` — minimum minutes between dumps
- `mem-dump-max-files` — keep at most this many pprof files
- `pprof-addr` — optional debug pprof listen address (e.g. `127.0.0.1:6060`)

These are diagnostics, not safety limits. The JS heap cap above and your
OS/cgroup limits are what actually stop runaway memory use.

## Version Flag

Use `-version` to print the build version and exit:

```
$ bserver -version
bserver dev
```

Override the version at build time with:

```
go build -ldflags "-X main.Version=1.0.0" -o bserver ./src
```
