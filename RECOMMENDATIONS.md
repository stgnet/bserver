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
- ~~In-memory render cache with fsnotify file watching, LRU eviction, RAM detection~~
- ~~Cache-Control headers for rendered pages and static files~~
- ~~Auto-generated favicon from domain/title with `_favicon.yaml` override~~
- ~~Remove unused `_tags.yaml` support (format definitions cover this use case)~~
- ~~Separate code from content (move YAML definitions and virtual hosts into `www/` subdirectory)~~
- ~~Release workflow (cross-compile binaries on Git tag push via GitHub Actions)~~

---

## Remaining

### 1. Rate Limiting

No rate limiting or request size limits beyond Go's defaults. For a
production-facing web server, adding basic rate limiting would improve
resilience against abuse.

### 2. Per-Virtual-Host Script Permissions

Currently, `-no-scripts` is a global toggle. In a shared hosting context,
per-virtual-host script permission controls (e.g., a `_config.yaml` file in
each site directory) would provide finer-grained security.

### 3. Code of Conduct

Most open-source projects include a code of conduct. Consider adding
`CODE_OF_CONDUCT.md`.

---

## Priority Ranking (remaining items)

1. **Rate limiting** (production hardening)
2. **Per-virtual-host script permissions** (security, future)
3. **Code of conduct** (community)
