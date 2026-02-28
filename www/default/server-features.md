# Server Features

bserver includes several production-oriented features that work automatically
without configuration.

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
| `cache-age` | `900` | Maximum entry age in seconds (15 minutes) |
| `static-age` | `86400` | Maximum Cache-Control age for static files in seconds (24 hours) |

Set `cache-size: 0` to disable caching entirely.

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

A domain qualifies for a Let's Encrypt certificate only if:

1. **Direct match** — a directory exists at `www/<domain>` (e.g., `www/example.com`)
2. **One subdomain deeper** — the parent domain has a directory (e.g.,
   `www.example.com` works when `www/example.com` exists)

This means `www.example.com` and `api.example.com` automatically work when
you create `www/example.com`. But deeply nested bogus domains like
`a.b.c.d.example.com` are rejected without contacting Let's Encrypt.

For domains that need more than one level of subdomains, create a symlink:
```
cd www && ln -s example.com deep.sub.example.com
```

### Domains Without a Virtual Host

Requests to domains that don't match any virtual host directory get a
self-signed certificate (no Let's Encrypt request is made). The request
still reaches the server and is served from the `default` virtual host,
so the rate limiter can track and block scanning attempts.

### Private and Non-Public Domains

IP addresses and domains with non-public suffixes (`.local`, `.test`,
`.internal`, etc.) always get self-signed certificates without contacting
Let's Encrypt.

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

Every HTTP request is logged with the client IP address, hostname, method,
path, response status code, and duration:

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
genuine server issue. Blocked requests are logged with "dropped" in
place of the status code.

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

A typical scanning attack in the logs:

```
198.51.100.7 bogus.example.com POST /webhook/upload 404 106ms
198.51.100.7 bogus.example.com POST /webhook/files 404 109ms
...
198.51.100.7 rate-limited after 10 consecutive errors (penalty: 10m0s)
198.51.100.7 bogus.example.com POST /webhook/batch dropped
198.51.100.7 bogus.example.com POST /webhook/import dropped
```

## Graceful Shutdown

bserver handles `SIGINT` (Ctrl+C) and `SIGTERM` signals gracefully:

1. Stops accepting new connections
2. Waits up to 10 seconds for in-flight requests to complete
3. Closes the render cache and file watchers
4. Exits cleanly

This means deployments using `systemctl restart` or container orchestrators
won't drop active requests.

## Version Flag

Use `-version` to print the build version and exit:

```
$ bserver -version
bserver dev
```

Override the version at build time with:

```
go build -ldflags "-X main.Version=1.0.0"
```

