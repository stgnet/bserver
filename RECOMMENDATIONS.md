# First-Time User Review: Recommendations for bserver

A review of the bserver project from the perspective of a first-time open source
contributor/user, covering documentation, code quality, testing, security, and
developer experience.

---

## Summary

bserver is a well-conceived YAML-driven web server with a clear vision. The core
rendering engine is functional and the built-in documentation site demonstrates
the system well. However, several areas would benefit from improvement to make
the project more accessible, maintainable, and production-ready.

---

## 1. Documentation

### README.md

**Strengths:**
- Clear one-line description of what the project does
- Feature list is comprehensive
- Quick Start section gets users running fast
- Configuration table is well-formatted

**Recommendations:**

- **Add a "How It Works" overview** directly in the README. The rendering
  pipeline (html.yaml → head + body → header + main + footer) is the key
  mental model, but it's only in the getting-started.md doc site. A brief
  paragraph or ASCII diagram in the README would help GitHub visitors
  immediately understand the architecture.

- **Specify the license type** in the README. The file says "See LICENSE for
  details" but doesn't name it as Apache 2.0. A one-liner like "Licensed
  under the Apache License 2.0" helps users without opening the file.

- **Add a badge section** at the top with CI status, Go version, and license
  badges. This is standard for Go open-source projects and provides
  at-a-glance project health.

- **Document environment variables** alongside CLI flags. The README shows
  `-email`, `-http`, etc. but doesn't mention the corresponding `LE_EMAIL`,
  `HTTP_ADDR`, `HTTPS_ADDR`, `CERT_CACHE` environment variables that the
  code supports as defaults. The getting-started.md lists some env vars but
  with incorrect names (`HTTP` instead of `HTTP_ADDR`, `HTTPS` instead of
  `HTTPS_ADDR`).

- **Add example output.** Show the HTML that the "Quick Example" YAML
  produces. This helps users understand the transformation immediately.

### CONTRIBUTING.md

- **Very minimal.** Consider adding: coding style expectations, how to run
  the server locally for development (non-root ports), and how the YAML
  definition system works at a high level so contributors can orient
  themselves in the codebase.

- **No code of conduct.** Most open-source projects include one.

### Inline Documentation (getting-started.md)

- **Environment variable names are wrong** in the table at the bottom. The
  code uses `HTTP_ADDR`, `HTTPS_ADDR`, `CERT_CACHE`, and `INDEX`, but the
  doc lists `HTTP` and `HTTPS` as the variable names.

---

## 2. Code Quality

### server.go

- **Deprecated `ioutil` usage** (lines 17, 319-320, 359-360): `ioutil.ReadFile`
  and `ioutil.WriteFile` have been deprecated since Go 1.16. Replace with
  `os.ReadFile` and `os.WriteFile`.

- **Commented-out imports** (lines 12-14): Three commented-out import lines
  remain in the file. These should be removed.

- **Deeply nested privilege-dropping code** (lines 514-537): The
  `setuid`/`setgid` logic is nested 4+ levels deep with cascading
  `if`/`else` blocks. Extracting this to a dedicated `dropPrivileges()`
  function would improve readability.

- **Error details leaked to clients** (line 70): The 404 response includes
  filesystem paths (`host "foo": /var/www/foo does not exist; default:
  /var/www/default does not exist`). In production, this exposes server
  internals. Consider returning a generic 404 and logging the details
  server-side only.

- **No request logging.** There is no access logging or request logging of
  any kind. A basic access log (method, path, status, duration) would be
  valuable for both development and production use.

- **No graceful shutdown signal handling.** The server exits on the first
  error from either listener but doesn't handle SIGTERM/SIGINT for clean
  shutdown, which is important for the systemd service use case.

### render.go

- **1800+ line file.** This single file contains the entire rendering engine
  — YAML parsing, template resolution, HTML generation, script execution,
  and CSS rendering. Consider splitting into logical units:
  - `render.go` — core rendering (renderContext, renderName, renderContent)
  - `format.go` — format definition parsing and variable substitution
  - `script.go` — script execution (renderScript, wrappers)
  - `style.go` — CSS rendering

- **Magic number depth limit of 50** appears in 5+ places. Define this as a
  named constant (e.g., `maxRenderDepth = 50`) for clarity and single-point
  modification.

- **`os.Environ()` passed to scripts** (line 1546): The `buildScriptEnv`
  function starts with the full server process environment (`os.Environ()`),
  which means scripts inherit everything the server process has access to.
  Consider passing a minimal, controlled environment instead.

### orderedmap.go

- **Clean implementation.** The `OrderedMap` type is well-documented and
  tested implicitly through the rendering tests. One minor note: `Delete()`
  is O(n) for the key slice scan, which is fine for YAML document sizes but
  worth noting.

---

## 3. Testing

### Current State

- **16 tests**, all passing. Good baseline.
- Tests cover: homepage content, navbar, footer, markdown pages, name
  reference validation, inline links, content wrapping (plural/singular),
  and `<pre>` tag handling.

### Recommendations

- **No tests for server.go at all.** The HTTP handler, virtual host routing,
  PHP CGI handling, path resolution, and TLS certificate generation have
  zero test coverage. These are critical code paths.

- **No tests for orderedmap.go.** The `OrderedMap` type (insertion order, Get,
  Set, Delete, Range, MarshalJSON, YAML parsing) should have dedicated unit
  tests.

- **Tests have dead/confusing code** in `TestHomepageContent` (lines 40-44):
  ```go
  if !strings.Contains(output, "Undefined name") {
      // This is intentionally inverted - we want NO undefined names
  } else {
      // But if there are, that's a real error
  }
  ```
  This `if/else` block does nothing — both branches are empty. The actual
  check is on line 46. This dead code should be removed.

- **Repetitive test setup.** Every test repeats the same
  `os.Getwd()`/`filepath.Join` boilerplate. A `testHelper` function or
  `TestMain` setup would reduce duplication.

- **No test for error paths.** What happens with malformed YAML? Circular
  references? Missing html.yaml? Scripts that fail? These edge cases should
  be tested.

- **No benchmarks.** For a web server, knowing the rendering performance
  characteristics would be valuable. `BenchmarkRenderYAMLPage` and
  `BenchmarkRenderMarkdownPage` would help track regressions.

- **CI doesn't run `go vet` or any linter.** Adding `go vet ./...` and
  optionally `staticcheck` or `golangci-lint` to the CI workflow would catch
  issues early.

---

## 4. Security

- **Script execution from YAML content** is the most significant concern.
  Any YAML file can define `script: python` with arbitrary code that runs on
  the server with the server's (post-privilege-drop) permissions. If bserver
  is used in a shared hosting context, one virtual host's YAML could execute
  code that affects others. Consider:
  - A flag to disable script execution entirely
  - Per-virtual-host script permission controls
  - Documentation of this risk prominently in the README

- **Full environment variable leak to scripts** (as noted above). Scripts get
  the full server environment, which may contain sensitive values.

- **`buildScriptEnv` passes `os.Environ()`** — this means if the server has
  database passwords, API keys, etc. in its environment, every script
  (Python, JS, PHP) inherits them.

- **No Content-Security-Policy headers.** The server returns HTML pages
  without any security headers (CSP, X-Frame-Options, X-Content-Type-Options,
  etc.). Consider adding at least basic security headers.

- **No rate limiting or request size limits** beyond Go's defaults. For a
  production web server, this matters.

---

## 5. Features / Usability

- **No `--version` flag.** Users and operators can't easily determine which
  version of bserver is running. Add a `-version` flag that prints the
  version and exits.

- **No hot reload.** Changing a YAML file requires no server restart (files
  are read per-request), which is great. However, there's no way to know
  this from the documentation. It should be called out as a feature.

- **`?debug` is undocumented in the README.** The feature list mentions
  "Debug mode" but doesn't explain the query parameter usage. Add an example:
  `http://localhost/?debug` produces HTML comment tracing.

- **No favicon handling.** Browsers request `/favicon.ico` on every page
  load. Without special handling, this generates a 404 for every page view.

- **Error pages are plain text.** 404 and 500 errors are returned as plain
  text via `http.Error()`. Consider rendering error pages through the YAML
  system (e.g., `404.yaml`) for a consistent user experience.

- **No `Cache-Control` headers on static files.** Static assets served via
  `http.ServeFile` get Go's default caching behavior. Adding appropriate
  cache headers for CSS/JS/images would improve performance.

---

## 6. Project Structure

- **YAML definition files are in the project root** alongside Go source
  files. This means the project root is both the Go module root and a
  runtime content directory. Consider moving the built-in YAML definitions
  (html.yaml, head.yaml, body.yaml, etc.) into a dedicated directory
  (e.g., `builtins/` or `templates/`) and embedding them with `go:embed`.
  This would:
  - Clean up the root directory
  - Make the binary self-contained (no need to run from the source directory)
  - Separate code from content

- **No `Makefile` or build script.** While `go build` is simple enough, a
  Makefile with targets for `build`, `test`, `lint`, `install`, and `clean`
  is conventional for Go projects and helps contributors.

- **No `.editorconfig` file.** Adding one ensures consistent formatting
  (tabs vs spaces, line endings) across contributors' editors.

- **No release artifacts.** The CI only builds and tests. Consider adding a
  release workflow that builds binaries for linux/amd64, linux/arm64, and
  darwin/amd64/arm64 when a Git tag is pushed. This makes installation much
  easier for users who don't have a Go toolchain.

---

## 7. Minor Code Fixes (Concrete)

These are specific issues that can be fixed immediately:

1. **`ioutil` deprecation in server.go** — Replace `io/ioutil` with `os`
   equivalents.

2. **Remove commented-out imports** in server.go (lines 12-14).

3. **Remove dead test code** in render_test.go (lines 40-44).

4. **Fix environment variable names** in getting-started.md documentation
   table.

---

## Priority Ranking

If tackling these incrementally, the suggested order is:

1. **Fix env var documentation** (quick win, user-facing)
2. **Fix deprecated `ioutil` usage** (quick win, code quality)
3. **Add server.go and orderedmap.go tests** (reliability)
4. **Add `-version` flag** (quick win, operations)
5. **Split render.go** (maintainability)
6. **Add security headers** (production readiness)
7. **Add script execution controls** (security)
8. **Embed YAML definitions** (distribution)
9. **Add release workflow** (distribution)
10. **Add request logging** (operations)
