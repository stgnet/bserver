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

| Flag | Default | Description |
|------|---------|-------------|
| `-cache-size` | `1024` | Maximum cache size in MB (0 to disable) |
| `-cache-age` | `900` | Maximum entry age in seconds (15 minutes) |
| `-static-age` | `86400` | Maximum Cache-Control age for static files in seconds (24 hours) |

Set `-cache-size=0` to disable caching entirely.

## Cache-Control Headers

bserver sets `Cache-Control` headers on all responses to help browsers and
proxies cache content efficiently.

### Rendered Pages

YAML and markdown pages receive a `Cache-Control: public, max-age=N` header
where N matches the `-cache-age` setting (default 300 seconds / 5 minutes).
This tells browsers to reuse the page without re-requesting it for that
duration.

### Static Files

For static files (images, CSS, JavaScript, fonts, etc.), bserver uses a
heuristic based on the file's last modification time:

- **max-age = half the file's age**, capped at `-static-age` (default 24 hours)
- **Minimum 60 seconds** for very recently modified files

For example, a CSS file last modified 2 hours ago gets `max-age=3600` (1 hour).
A logo image unchanged for 30 days gets `max-age=86400` (24 hours, the cap).

This approach means frequently-updated files are re-checked sooner, while
stable files are cached longer.

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

Every HTTP request is logged with the method, path, response status code, and
duration:

```
GET / 200 12ms
GET /about 200 3ms
GET /missing 404 1ms
```

Cached responses are typically much faster than first renders, making it easy
to spot cache misses in the logs.

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

