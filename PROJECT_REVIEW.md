# Project Review (2026-03)

This review focuses on practical improvements across security, features, testing,
and documentation, based on the current codebase and CI setup.

## Top Priorities

1. **Add request body size limits for PHP and script execution paths**
   - `handlePHP` copies the full request body into memory with `io.Copy` before invoking `php-cgi`.
   - script execution similarly reads full request bodies via `io.ReadAll` in `buildScriptEnv`.
   - Recommendation: enforce a configurable max payload using `http.MaxBytesReader` and reject oversized requests with `413`.

2. **Strengthen default transport/security headers**
   - The server currently sets `X-Content-Type-Options`, `X-Frame-Options`, and `Referrer-Policy`.
   - Recommendation: add opt-in defaults for `Content-Security-Policy`, `Strict-Transport-Security` (HTTPS only), and `Permissions-Policy`.

3. **Harden TLS defaults**
   - TLS config does not explicitly set `MinVersion`.
   - Recommendation: set `MinVersion: tls.VersionTLS12` (or TLS 1.3-only mode behind a compatibility flag), and document ciphers/protocol expectations.

4. **Proxy-aware client IP handling for rate limiting/logging**
   - Rate limiting and logging rely on `RemoteAddr` only.
   - Recommendation: add a trusted-proxy list and parse `X-Forwarded-For`/`X-Real-IP` only when the source is trusted, to avoid spoofing while supporting real deployments.

5. **Expand CI into a security + quality pipeline**
   - Current CI runs vet/build/test only.
   - Recommendation: include `gofmt -w` checks, `staticcheck`, `govulncheck`, and race tests (`go test -race ./...`) on Linux.

## Feature Opportunities

- **Health and readiness endpoints**
  - Add `/healthz` and `/readyz` for orchestration and uptime checks.

- **Observability endpoints**
  - Add optional Prometheus metrics and structured logging mode (JSON) for production ingestion.

- **Per-vhost operational controls**
  - Add request timeout and body limit overrides in `_config.yaml` with safe global caps.

- **Staging mode for config validation**
  - Add a startup `--check-config` command that validates YAML inheritance and format references without starting listeners.

## Documentation Improvements

- Add a short **threat model** section to README (trusted editor model, script execution implications, reverse proxy assumptions).
- Add **reverse proxy deployment guidance** (Caddy/Nginx/Cloudflare), including which headers are trusted.
- Add **resource limit guidance** (cache size tuning, script timeout behavior, body limits, and expected memory impact).
- Add a troubleshooting matrix for common operational failures (cert issuance, php-cgi missing, bad vhost routing).

## Testing Improvements

- Add fuzz tests for YAML parsing/rendering boundaries and recursion depth behavior.
- Add integration tests for oversized body handling and 413 responses once limits are implemented.
- Add tests for trusted proxy parsing and anti-spoofing behavior.
- Add benchmark coverage for cache-hit vs cache-miss paths under concurrency.

## Suggested Implementation Order

1. Body limits + tests.
2. TLS/security header hardening + docs updates.
3. Proxy-aware client IP support + tests.
4. CI quality/security jobs.
5. Observability and health endpoints.
