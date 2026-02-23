# bserver

A YAML-driven web server written in Go that generates complete HTML pages from structured YAML and Markdown definitions. Write YAML, get HTML — with virtual hosting, automatic HTTPS, and zero boilerplate.

**Website:** [bserver.info](https://bserver.info)

## Features

- **YAML page generation** — Define pages as structured YAML; bserver renders clean, indented HTML5
- **Markdown support** — `.md` files render with full site chrome (navbar, header, footer, styles)
- **Format definitions** — Reusable HTML templates via the `^name` prefix, with variable substitution
- **Cascading name resolution** — Definitions resolve upward through directories, child overrides parent
- **Virtual hosting** — Serve multiple domains from subdirectories, with a `default/` fallback
- **Automatic HTTPS** — Let's Encrypt certificates with self-signed fallback for local development
- **Bootstrap 5** — Pre-configured CSS and Font Awesome out of the box
- **Server-side scripting** — Dynamic content via Python, JavaScript (Node.js), or PHP
- **Privilege dropping** — Binds ports 80/443 as root, then drops to `nobody`
- **Merge definitions** — Extend inherited definitions with the `+name` prefix
- **Style rendering** — YAML-defined CSS with selector keys and property maps
- **Debug mode** — Add `?debug` to any request for HTML comment tracing

## Quick Start

```sh
go build -o bserver
./bserver
```

This serves the built-in documentation site from the `default/` directory on ports 80 and 443.

A minimal page needs just an `index.yaml`:

```yaml
main:
  - h1: "Hello World"
  - p: "Welcome to my site."
```

This produces a complete HTML page with doctype, head, body, navbar, and footer.

## Configuration

| Flag | Default | Description |
|------|---------|-------------|
| `-email` | | Let's Encrypt contact email |
| `-http` | `:80` | HTTP listen address |
| `-https` | `:443` | HTTPS listen address |
| `-cache` | `./cert-cache` | Certificate cache directory |
| `-php` | auto-detected | Path to php-cgi |
| `-index` | `index.yaml,index.md,index.php,index.html,index.htm` | Index file priority |
| `-parent-levels` | `1` | Max directory levels above docroot for YAML search |

## Directory Structure

```
/var/www/                    # Working directory
├── default/                 # Fallback for unmapped hosts
│   ├── index.yaml           # Home page
│   ├── header.yaml          # Site header
│   ├── footer.yaml          # Site footer
│   └── style.yaml           # Site styles
├── example.com/             # Virtual host
│   ├── index.yaml
│   └── about.md
├── html.yaml                # Base document structure (inherited)
├── bootstrap5.yaml          # Bootstrap 5 CDN (inherited)
├── navbar.yaml              # Navigation component (inherited)
└── cert-cache/              # TLS certificates (auto-created)
```

## Installing as a Service

```sh
go build -o bserver
sudo ./install-service.sh
```

Installs and starts bserver as a system service using systemd (Linux) or launchd (macOS).

```sh
sudo ./install-service.sh restart   # restart after rebuilding
sudo ./install-service.sh remove    # uninstall the service
```

## License

See [LICENSE](LICENSE) for details.
