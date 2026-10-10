# Contributing to bserver

## Getting Started

bserver is a YAML-driven web server written in Go. The codebase is small and focused:

| File | Purpose |
|------|---------|
| `src/server.go` | HTTP server, virtual hosting, TLS, PHP CGI |
| `src/render.go` | Core rendering engine (YAML/Markdown to HTML) |
| `src/format.go` | Format definition parsing and variable substitution |
| `src/script.go` | Server-side script execution (Python, JS, PHP) |
| `src/style.go` | CSS rendering from YAML style definitions |
| `src/orderedmap.go` | Insertion-order-preserving map for YAML parsing |

YAML component templates (`html.yaml`, `navbar.yaml`, etc.) live in the `www/` directory. The `www/default/` directory is the built-in documentation site that also serves as a working example. Go source lives in `src/` and web content in `www/` (mirroring the `/var/www` convention), keeping the project root for docs and scripts. The binary is still built at the project root as `./bserver`.

## Building

```sh
go build -o bserver ./src
```

Or using the Makefile:

```sh
make build
```

## Running Locally

Since ports 80/443 require root, use alternative ports for development:

```sh
./bserver -http :8080 -https ""
```

Then visit `http://localhost:8080` to see the built-in documentation site.

## Testing

```sh
go test ./...
```

Run with verbose output:

```sh
go test -v ./...
```

Run benchmarks:

```sh
go test -bench=. -benchmem ./...
```

Please ensure all tests pass before submitting a pull request.

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Keep functions focused and files under ~500 lines where practical
- Use `strings.Builder` for HTML generation
- Error paths should produce HTML comments (`<!-- error: ... -->`) rather than panicking

## How Rendering Works

1. `renderYAMLPage()` creates a `renderContext` and loads the requested YAML file
2. **Pass 1 (resolve):** `resolveAll("html", 0)` walks the name tree, loading YAML files and applying `+merge` definitions
3. **Pass 2 (render):** `renderName()` recursively produces HTML output
4. Names resolve by searching upward from the request directory through parent directories
5. `^name` format definitions control how names render as HTML tags with variable substitution

## Submitting Changes

1. Fork the repository
2. Create a feature branch from `main`
3. Make your changes
4. Run `go vet ./...` and `go test ./...` to verify
5. Submit a pull request with a clear description of the change

## Reporting Issues

Open an issue on GitHub with steps to reproduce the problem and any relevant YAML/Markdown content.
