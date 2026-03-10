# bserver

[![CI](https://github.com/stgnet/bserver/actions/workflows/ci.yml/badge.svg)](https://github.com/stgnet/bserver/actions/workflows/ci.yml)
[![Go 1.24+](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev/dl/)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

A YAML-driven web server written in Go that generates complete HTML pages from structured YAML and Markdown definitions. Write YAML, get HTML — with virtual hosting, automatic HTTPS, and zero boilerplate.

**Website:** [bserver.info](https://bserver.info)

## How It Works

bserver uses a pipeline of YAML definitions to build complete HTML pages. You only define `main:` — everything else is inherited:

```
html.yaml          ← starting point (provides <html lang="en">)
├── head.yaml      ← <head> with meta, title, styles
└── body.yaml      ← <body> wrapping:
    ├── header.yaml    ← navbar
    ├── main           ← YOUR CONTENT (from index.yaml)
    └── footer.yaml    ← footer text
```

Names resolve by searching upward through directories — your site's `navlinks.yaml` overrides the default, but inherited definitions like `navbar.yaml` are shared across all sites. Files are read on every request, so changes take effect immediately with no restart.

## Features

- **YAML page generation** — Define pages as structured YAML; bserver renders clean, indented HTML5
- **Markdown support** — `.md` files render with full site chrome (navbar, header, footer, styles)
- **Format definitions** — Reusable HTML templates via the `^name` prefix, with variable substitution
- **Cascading name resolution** — Definitions resolve upward through directories, child overrides parent
- **Proxy mode** — Reverse-proxy a vhost to a backend server with a one-line `index.yaml`
- **Virtual hosting** — Serve multiple domains from subdirectories, with a `default/` fallback
- **Automatic HTTPS** — Let's Encrypt certificates with self-signed fallback for local development
- **Bootstrap 5** — Pre-configured CSS and Font Awesome out of the box
- **Server-side scripting** — Dynamic content via Python, JavaScript (Node.js), or PHP
- **Privilege dropping** — Binds ports 80/443 as root, then drops to `nobody`
- **Merge definitions** — Extend inherited definitions with the `+name` prefix
- **Style rendering** — YAML-defined CSS with selector keys and property maps
- **Debug mode** — Add `?debug` to any URL (e.g., `http://localhost/?debug`) for HTML comment tracing
- **Hot reload** — YAML/Markdown files are read per-request; no server restart needed
- **Access logging** — Every request is logged with method, path, status, and duration
- **Graceful shutdown** — Clean shutdown on SIGINT/SIGTERM for systemd compatibility

## Quick Start

Requires [Go](https://go.dev/dl/) 1.24 or later.

```sh
git clone https://github.com/stgnet/bserver.git
cd bserver
go build -o bserver
./bserver -http :8080 -https ""
```

Then visit `http://localhost:8080` to see the built-in documentation site.

A minimal page needs just an `index.yaml`:

```yaml
main:
  - h1: "Hello World"
  - p: "Welcome to my site."
```

This produces:

```html
<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <title>bserver</title>
    ...
  </head>
  <body>
    <header>...</header>
    <main>
      <h1>Hello World</h1>
      <p>Welcome to my site.</p>
    </main>
    <footer>...</footer>
  </body>
</html>
```

## Configuration

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-email` | | Let's Encrypt contact email |
| `-http` | `:80` | HTTP listen address |
| `-https` | `:443` | HTTPS listen address |
| `-cache` | `./cert-cache` | Certificate cache directory |
| `-php` | auto-detected | Path to php-cgi |
| `-index` | `index.yaml,index.md,index.php,index.html,index.htm` | Index file priority |
| `-parent-levels` | `1` | Max directory levels above docroot for YAML search |
| `-base` | `www` | Web content root directory |
| `-version` | | Print version and exit |

### Environment Variables

All flags can also be set via environment variables:

| Variable | Flag equivalent |
|----------|----------------|
| `LE_EMAIL` | `-email` |
| `HTTP_ADDR` | `-http` |
| `HTTPS_ADDR` | `-https` |
| `CERT_CACHE` | `-cache` |
| `PHP_CGI` | `-php` |
| `BASE_DIR` | `-base` |
| `INDEX` | `-index` |

CLI flags take precedence over environment variables.

## Directory Structure

Go source files live in the project root. Web content lives in `www/`, mirroring the `/var/www` convention:

```
bserver/
├── *.go                     # Go source code
├── www/                     # Web content root (-base flag)
│   ├── default/             # Fallback for unmapped hosts
│   │   ├── index.yaml       # Home page
│   │   ├── header.yaml      # Site header
│   │   ├── footer.yaml      # Site footer
│   │   └── style.yaml       # Site styles
│   ├── example.com/         # Virtual host
│   │   ├── index.yaml
│   │   └── about.md
│   ├── html.yaml            # Base document structure (inherited)
│   ├── bootstrap5.yaml      # Bootstrap 5 CDN (inherited)
│   ├── navbar.yaml          # Navigation component (inherited)
│   └── cert-cache/          # TLS certificates (auto-created)
└── ...
```

The shared YAML definitions in `www/` are readable and can be copied into any virtual host directory to customize behavior.

## Proxy Mode

Any virtual host can act as a reverse proxy instead of serving files. Create a vhost directory with an `index.yaml` containing an `http:` key:

```yaml
# www/app.example.com/index.yaml
http: '192.168.1.2:8080'
```

All requests to that domain are forwarded to the backend. The proxy configuration is cached and automatically reloaded when `index.yaml` changes. If the backend is unreachable, a `502 Bad Gateway` is returned.

## Installing as a Service

```sh
git clone https://github.com/stgnet/bserver.git
cd bserver
go build -o bserver
sudo ./install-service.sh
```

Installs and starts bserver as a system service using systemd (Linux) or launchd (macOS). The service starts automatically and is enabled on boot.

To update and restart after pulling new changes:

```sh
git pull
go build -o bserver
sudo ./install-service.sh restart
```

To uninstall:

```sh
sudo ./install-service.sh remove
```

## License

Licensed under the Apache License 2.0. See [LICENSE](LICENSE) for details.
