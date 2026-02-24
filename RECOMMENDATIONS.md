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

---

## Remaining

### 1. Document or Remove _tags.yaml Support

The `_tags.yaml` feature (registering custom HTML tag names per directory) is
undocumented. Either add documentation for it in the default site docs, or
remove support if it's not needed — custom tags could be handled by format
definitions instead.

### 2. Embed YAML Definitions with go:embed

YAML definition files (html.yaml, head.yaml, body.yaml, etc.) live in the
project root alongside Go source files. This means the project root is both
the Go module root and a runtime content directory. Moving the built-in YAML
definitions into a dedicated directory and embedding them with `go:embed` would:
- Clean up the root directory
- Make the binary self-contained (no need to run from the source directory)
- Separate code from content

This is the most significant remaining architectural change.

### 3. Release Workflow

The CI only builds and tests. Adding a release workflow that builds binaries
for linux/amd64, linux/arm64, and darwin/amd64/arm64 when a Git tag is pushed
would make installation much easier for users who don't have a Go toolchain.

### 4. Rate Limiting

No rate limiting or request size limits beyond Go's defaults. For a
production-facing web server, adding basic rate limiting would improve
resilience against abuse.

### 5. Per-Virtual-Host Script Permissions

Currently, `-no-scripts` is a global toggle. In a shared hosting context,
per-virtual-host script permission controls (e.g., a `_config.yaml` file in
each site directory) would provide finer-grained security.

### 6. Code of Conduct

Most open-source projects include a code of conduct. Consider adding
`CODE_OF_CONDUCT.md`.

---

## Priority Ranking (remaining items)

1. **Document or remove _tags.yaml** (documentation debt)
2. **Embed YAML definitions** (architecture)
3. **Release workflow** (distribution)
4. **Rate limiting** (production hardening)
5. **Per-virtual-host script permissions** (security, future)
6. **Code of conduct** (community)
