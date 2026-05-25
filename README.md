# bserver

[![CI](https://github.com/stgnet/bserver/actions/workflows/ci.yml/badge.svg)](https://github.com/stgnet/bserver/actions/workflows/ci.yml)
[![Go 1.24+](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev/dl/)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

A small web server written in Go that builds HTML pages from **YAML and
Markdown** definitions — no template language, no build step. Virtual
hosting, automatic HTTPS, server-side scripts (Python, JavaScript, PHP,
Shell), reverse proxying, and rate limiting are all built in.

**Website / live docs:** [bserver.info](https://bserver.info)

## Quick Start

You need [Go 1.24+](https://go.dev/dl/).

```sh
git clone https://github.com/stgnet/bserver.git
cd bserver
go build
./bserver
```

Open the URL it logs at startup (port 80 if available, otherwise
`8000-8099`). You'll land on the bundled documentation site — which is
itself a bserver site living in `www/default/`.

### Your First Page

```sh
mkdir www/example.com
```

```yaml
# www/example.com/index.yaml
main:
  - h1: "Hello World"
  - p: "Welcome to my site."
```

That's it. Visit the site and bserver wraps your `main:` content in a
full HTML document with navigation, footer, Bootstrap 5 styling, and an
auto-generated favicon. Everything outside of `main:` was inherited from
the shared YAML in `www/`; override any of it by dropping a same-named
file into your vhost directory.

### Install as a System Service

```sh
sudo ./install-service.sh             # install + start (systemd or launchd)
sudo ./install-service.sh restart     # after `git pull && go build`
sudo ./install-service.sh remove      # uninstall
```

## How It Works (One Page)

Every page is assembled by following a tree of named references starting
at `html`:

```
html.yaml          ← <html lang="en"> wrapping head + body
├── head.yaml      ← <head> with meta, title, styles
└── body.yaml      ← <body> wrapping:
    ├── header.yaml    ← navbar
    ├── main           ← YOUR CONTENT (from index.yaml or .md)
    └── footer.yaml    ← footer
```

When bserver sees a name like `header`, it looks for `header.yaml`
starting in the request's directory and walking up — so site-specific
files win, shared definitions in `www/` are inherited. YAML files are
re-read on every request (with a render cache invalidated by fsnotify),
so there's no restart cycle.

Four prefixes govern every YAML key:

```yaml
main:           # plain key — content
  - h1: "Hi"

^card:          # ^ — format definition (how a name renders as HTML)
  tag: div
  params: { class: card }

+headlink:      # + — merge into an existing definition
  - { rel: stylesheet, href: /extra.css }

$navlinks:      # $ — data source (script that produces the value)
  script: javascript
  code: |
    print(JSON.stringify([{key: "/", value: "Home"}]));
```

Everything else — components, layouts, the navbar, error pages, scripts,
proxy mode — composes from these four pieces.

## Feature Highlights

- **YAML / Markdown page generation** with clean indented HTML5 output
- **Cascading name resolution** — child overrides parent, no inheritance
  ceremony
- **Format definitions** (`^name`) — reusable HTML templates with
  variable substitution
- **Data sources** (`$name`) — script-backed content with JSON output
- **Server-side scripts** — Python, embedded JavaScript (goja), PHP, and
  Shell, plus full PHP-CGI for `.php` files
- **Virtual hosting** — one directory per domain, `default/` fallback,
  symlink aliases
- **Automatic HTTPS** — Let's Encrypt for public domains, self-signed
  fallback for IPs and `.local` / `.test` / `.internal`
- **Reverse proxy mode** — one-line `index.yaml` turns a vhost into a
  proxy (with SSRF guards and optional API-key gating)
- **Render cache** with fsnotify invalidation and RAM-aware sizing
- **Security headers, rate limiting, privilege dropping**, port-80
  fallback
- **Auto-generated favicons** with optional `_favicon.yaml` customization
- **Debug mode** — `?debug` emits HTML-comment traces of name resolution

## Documentation

Once the server is running, the full documentation site is available at
`/`:

- [Getting Started](getting-started) — installation, first page,
  configuration
- [Content Definitions](definitions) — the four prefixes
- [Format Definitions](formats) — the `^` system
- [Data Sources](data-sources) — the `$` system
- [Built-in Components](components) — pre-defined layout & navigation
  pieces
- [Server-Side Scripts](scripts) — Python, JavaScript, PHP, Shell
- [Server Features](features) — caching, security, rate limiting, TLS,
  favicons
- [Proxy Mode](proxy) — reverse proxying with one line
- [Error Handling](errors) — custom 404/500 templates
- [Advanced Features](advanced) — virtual hosting internals, name
  resolution, debug mode
- [Tips & Recipes](tips) — common patterns

## License

Apache 2.0 — see [LICENSE](LICENSE).
