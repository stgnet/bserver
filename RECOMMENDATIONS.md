# Recommendations for bserver

A review of the bserver project covering documentation, code quality, testing,
security, and developer experience. Items marked ~~strikethrough~~ have been
completed.

---

## Completed

The following recommendations have been implemented:

- ~~Add "How It Works" overview to README~~
- ~~Specify Apache 2.0 license in README~~
- ~~Add CI/Go/License badges to README~~
- ~~Document environment variables alongside CLI flags~~
- ~~Add example HTML output to README~~
- ~~Expand CONTRIBUTING.md (architecture, dev setup, code style)~~
- ~~Fix environment variable names in getting-started.md~~
- ~~Replace deprecated `ioutil` with `os.ReadFile`/`os.WriteFile`~~
- ~~Remove commented-out imports in server.go~~
- ~~Remove dead test code in render_test.go~~
- ~~Extract `dropPrivileges()` function~~
- ~~Fix 404 response leaking filesystem paths~~
- ~~Add request logging middleware~~
- ~~Add graceful shutdown (SIGINT/SIGTERM)~~
- ~~Split render.go into render.go, format.go, script.go, style.go~~
- ~~Define `maxRenderDepth` constant (replace magic number 50)~~
- ~~Fix `os.Environ()` leak to scripts~~
- ~~Add `-version` flag~~
- ~~Add `-no-scripts` flag to disable script execution~~
- ~~Add security headers (X-Content-Type-Options, X-Frame-Options, Referrer-Policy)~~
- ~~Add server.go tests (HTTP handlers, middleware, virtual hosting)~~
- ~~Add orderedmap_test.go with dedicated unit tests~~
- ~~Add test helper functions (`defaultDocRoot`, `setupMinimalSite`)~~
- ~~Add error path tests (malformed YAML, circular refs, missing html.yaml)~~
- ~~Add benchmarks (`BenchmarkRenderYAMLPage`, `BenchmarkRenderMarkdownPage`)~~
- ~~Add `go vet` to CI workflow~~
- ~~Document hot reload and `?debug` in README~~
- ~~Add Makefile~~
- ~~Add .editorconfig~~
- ~~Error pages via YAML (error/error404 definitions with $error, $description, $message)~~

---

## Remaining

### 1. Caching System with YAML Configuration

Rendered YAML and Markdown pages are regenerated on every request, and static
files lack `Cache-Control` headers. A caching system with two layers would
improve performance:

1. **HTTP Cache-Control headers** — Set browser cache TTLs for static assets
   (CSS, JS, images) and rendered pages.
2. **In-memory output cache** — Cache rendered HTML keyed by file path + mtime,
   avoiding re-rendering unchanged pages. This also provides the foundation
   for caching generated assets like favicons (see below).

Cache settings should be defined in a YAML file (e.g., `_cache.yaml`) at the
project root, providing sensible defaults that sites can override at any
directory level. This follows bserver's cascading resolution pattern — a
site-level `_cache.yaml` overrides the root defaults. Example:

```yaml
# _cache.yaml — cache configuration defaults
static:
  css: 86400       # 1 day (seconds)
  js: 86400
  images: 604800   # 1 week
rendered:
  ttl: 0           # 0 = no in-memory caching (preserves hot-reload by default)
  max-entries: 1000
```

### 2. Auto-Generated Favicon

Browsers request `/favicon.ico` on every page load. Without special handling,
this generates a 404 for every page view, cluttering access logs. Rather than
just silencing the 404, bserver could auto-generate a favicon from clues in
the rendered page — e.g., the first letter of the `<title>`, the brand text
in the navbar, or a configured color from `style.yaml`. This keeps the
zero-boilerplate philosophy: a site gets a reasonable favicon without the user
having to create one. If a site provides its own `favicon.ico` file, it takes
precedence. Depends on the caching system (#1) to avoid regenerating the
icon on every request.

### 3. Embed YAML Definitions with go:embed

YAML definition files (html.yaml, head.yaml, body.yaml, etc.) live in the
project root alongside Go source files. This means the project root is both
the Go module root and a runtime content directory. Moving the built-in YAML
definitions into a dedicated directory and embedding them with `go:embed` would:
- Clean up the root directory
- Make the binary self-contained (no need to run from the source directory)
- Separate code from content

This is the most significant remaining architectural change.

### 4. Release Workflow

The CI only builds and tests. Adding a release workflow that builds binaries
for linux/amd64, linux/arm64, and darwin/amd64/arm64 when a Git tag is pushed
would make installation much easier for users who don't have a Go toolchain.

### 5. Rate Limiting

No rate limiting or request size limits beyond Go's defaults. For a
production-facing web server, adding basic rate limiting would improve
resilience against abuse.

### 6. Per-Virtual-Host Script Permissions

Currently, `-no-scripts` is a global toggle. In a shared hosting context,
per-virtual-host script permission controls (e.g., a `_config.yaml` file in
each site directory) would provide finer-grained security.

### 7. Code of Conduct

Most open-source projects include a code of conduct. Consider adding
`CODE_OF_CONDUCT.md`.

---

## Priority Ranking (remaining items)

1. **Caching system with YAML config** (performance, enables #2)
2. **Auto-generated favicon** (zero-boilerplate philosophy, depends on #1)
3. **Embed YAML definitions** (architecture)
4. **Release workflow** (distribution)
5. **Rate limiting** (production hardening)
6. **Per-virtual-host script permissions** (security, future)
7. **Code of conduct** (community)
