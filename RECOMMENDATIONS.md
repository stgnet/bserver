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

---

## Remaining

### 1. Error Pages via YAML

404 and 500 errors are returned as plain text via `http.Error()`. Consider
rendering error pages through the YAML system (e.g., `404.yaml`) for a
consistent user experience that matches the site's design.

### 2. Cache-Control Headers on Static Files

Static assets served via `http.ServeFile` get Go's default caching behavior.
Adding appropriate `Cache-Control` headers for CSS, JS, and image files would
improve performance for production sites.

### 3. Rendered Output Cache

Rendered YAML and Markdown pages are regenerated on every request. Adding an
in-memory cache (keyed by file path + mtime) would avoid re-rendering unchanged
pages. This also provides the foundation for caching generated assets like
favicons (see below).

### 4. Auto-Generated Favicon

Browsers request `/favicon.ico` on every page load. Without special handling,
this generates a 404 for every page view, cluttering access logs. Rather than
just silencing the 404, bserver could auto-generate a favicon from clues in
the rendered page — e.g., the first letter of the `<title>`, the brand text
in the navbar, or a configured color from `style.yaml`. This keeps the
zero-boilerplate philosophy: a site gets a reasonable favicon without the user
having to create one. If a site provides its own `favicon.ico` file, it takes
precedence. Depends on the rendered output cache (#3) to avoid regenerating
the icon on every request.

### 5. Embed YAML Definitions with go:embed

YAML definition files (html.yaml, head.yaml, body.yaml, etc.) live in the
project root alongside Go source files. This means the project root is both
the Go module root and a runtime content directory. Moving the built-in YAML
definitions into a dedicated directory and embedding them with `go:embed` would:
- Clean up the root directory
- Make the binary self-contained (no need to run from the source directory)
- Separate code from content

This is the most significant remaining architectural change.

### 6. Release Workflow

The CI only builds and tests. Adding a release workflow that builds binaries
for linux/amd64, linux/arm64, and darwin/amd64/arm64 when a Git tag is pushed
would make installation much easier for users who don't have a Go toolchain.

### 7. Rate Limiting

No rate limiting or request size limits beyond Go's defaults. For a
production-facing web server, adding basic rate limiting would improve
resilience against abuse.

### 8. Per-Virtual-Host Script Permissions

Currently, `-no-scripts` is a global toggle. In a shared hosting context,
per-virtual-host script permission controls (e.g., a `_config.yaml` file in
each site directory) would provide finer-grained security.

### 9. Code of Conduct

Most open-source projects include a code of conduct. Consider adding
`CODE_OF_CONDUCT.md`.

---

## Priority Ranking (remaining items)

1. **Error pages via YAML** (user experience)
2. **Cache-Control headers** (quick win, performance)
3. **Rendered output cache** (performance, enables #4)
4. **Auto-generated favicon** (zero-boilerplate philosophy, depends on #3)
5. **Embed YAML definitions** (architecture)
6. **Release workflow** (distribution)
7. **Rate limiting** (production hardening)
8. **Per-virtual-host script permissions** (security, future)
9. **Code of conduct** (community)
