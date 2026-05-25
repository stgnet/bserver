# Getting Started

## What is bserver?

bserver is a small web server written in Go that builds HTML pages from
**YAML and Markdown** definitions instead of templates. You describe a
page as a tree of named pieces, and bserver renders them into clean,
indented HTML5. The same engine handles virtual hosting, automatic
HTTPS, server-side scripts in four languages, reverse proxying, and
static file serving.

Most sites need only a handful of YAML files. The default `www/`
directory bundled with bserver — including the site you're reading right
now — is itself a complete bserver site you can clone, tweak, and serve.

## Installation

You need [Go](https://go.dev/dl/) 1.24 or later.

```sh
git clone https://github.com/stgnet/bserver.git
cd bserver
go build
./bserver
```

By default, bserver listens on port 80 (HTTP) and 443 (HTTPS) and serves
content from `./www/`. If port 80 is unavailable (no root, port in use)
it falls back to a port in the `8000-8099` range. Open the URL it
logs at startup to see the documentation site.

To install as a system service (systemd or launchd):

```sh
sudo ./install-service.sh
```

## Your First Page

Make a directory for your virtual host and drop an `index.yaml` into it:

```sh
mkdir www/example.com
```

```yaml
# www/example.com/index.yaml
main:
  - h1: "Hello World"
  - p: "Welcome to my site."
```

That's a complete page. Visit `http://example.com/` (or set up
`example.com` in `/etc/hosts` for local testing) and you'll get:

```html
<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <title>bserver</title>
    ...
  </head>
  <body>
    <header><nav>...</nav></header>
    <main>
      <h1>Hello World</h1>
      <p>Welcome to my site.</p>
    </main>
    <footer>...</footer>
  </body>
</html>
```

Notice you only defined `main:` — the rest (DOCTYPE, `<head>`, navbar,
footer, Bootstrap styles, automatic favicon) was inherited from the
shared YAML in `www/`. See [Content Definitions](/definitions) for how
that works.

## How Pages Are Built

bserver assembles every page by following a tree of named references
starting at `html`:

```
html.yaml          ← starting point: <html lang="en"> wrapping head + body
├── head.yaml      ← <head> with meta, title, headlink, style
└── body.yaml      ← <body> wrapping:
    ├── header.yaml    ← navbar
    ├── main           ← YOUR PAGE CONTENT (from your index.yaml or .md file)
    └── footer.yaml    ← footer text
```

Each name is resolved by looking for a `<name>.yaml` file, starting in
the request's directory and walking up to one level above the document
root. Your site's files override the inherited ones. The pipeline is
described in detail under [Content Definitions](/definitions) and
[Advanced Features](/advanced).

## Directory Layout

```
www/                        ← the document root (the -base flag)
├── _config.yaml            ← server-wide configuration (all keys optional)
├── html.yaml               ← shared base definitions ...
├── head.yaml
├── body.yaml
├── navbar.yaml
├── bootstrap5.yaml
├── ...
├── default/                ← fallback site (this documentation)
│   └── index.yaml
├── example.com/            ← virtual host
│   ├── index.yaml          ← home page
│   ├── about.md            ← markdown page → /about
│   ├── navlinks.yaml       ← override nav (optional)
│   ├── style.yaml          ← extra CSS (optional, use +style)
│   └── _config.yaml        ← per-vhost overrides (optional)
└── cert-cache/             ← TLS certificates (auto-created)
```

Anything in `www/` (the shared definitions) is available to every site.
Anything inside a vhost directory wins over the shared version because
page-level files are loaded first.

## Configuration

bserver is configured through `_config.yaml` in the `www/` directory.
**Every key is optional** and has a sensible default. See the bundled
`www/_config.yaml` for a documented template.

### Server-wide settings

| Key | Env | Default | Description |
|-----|-----|---------|-------------|
| `http` | `HTTP_ADDR` | `:80` | HTTP listen address |
| `https` | `HTTPS_ADDR` | `:443` | HTTPS listen address |
| `email` | `LE_EMAIL` | *(auto-detected)* | Let's Encrypt contact email |
| `cert-cache` | `CERT_CACHE` | `./cert-cache` | TLS cert cache directory |
| `php` | `PHP_CGI` | *(auto-detected)* | Path to `php-cgi` |
| `cache-size` | — | `1024` | Render cache size in MB (`0` disables) |
| `max-body-size` | — | `10` | Max request body in MB (`0` disables) |
| `js-heap-mb` | — | `128` | Per-script JS heap-growth cap in MB |
| `php-timeout` | — | `60` | Idle timeout for `php-cgi`, seconds |
| `php-stream-after` | — | `5` | Buffer `php-cgi` for this long before switching to chunked |
| `debug-token` | `DEBUG_TOKEN` | — | Token required for `?debug=<token>` |

### Per-vhost settings (override in `<host>/_config.yaml`)

| Key | Env | Default | Description |
|-----|-----|---------|-------------|
| `cache-age` | — | `900` | Render cache + Cache-Control for pages, seconds |
| `static-age` | — | `86400` | Max Cache-Control max-age cap for static files, seconds |
| `parent-levels` | — | `1` | How many directories above docRoot to search |
| `index` | `INDEX` | `index.yaml,index.md,index.php,index.html,index.htm` | Directory index priority |
| `types` | `TYPES` | *(common web types)* | Allowed file extensions |
| `allow-http` | — | `false` | Serve this vhost over plain HTTP (skip HTTPS redirect) |

### Command-line flags

| Flag | Description |
|------|-------------|
| `-base <dir>` | Web content root (overrides `BASE_DIR` env, defaults to `./www`) |
| `-version` | Print version and exit |

Precedence everywhere: **environment variable > `_config.yaml` value >
built-in default**.

## What's Next

If you're new, read these in order:

1. **[Content Definitions](/definitions)** — the four prefixes (`name`,
   `^name`, `+name`, `$name`) that govern every YAML key
2. **[Format Definitions](/formats)** — how to make reusable HTML
   components
3. **[Built-in Components](/components)** — the catalog of pre-made
   layout, navigation, and content blocks
4. **[Server-Side Scripts](/scripts)** — Python, JavaScript, PHP, and
   Shell scripts that produce HTML
5. **[Tips & Recipes](/tips)** — common patterns: redirects, custom CSS,
   icon links, embedded media

For operations:

- **[Server Features](/features)** — caching, security headers, rate
  limiting, TLS, debug mode, favicons
- **[Proxy Mode](/proxy)** — make a vhost a reverse proxy with one line
- **[Error Handling](/errors)** — custom 404/500 templates
- **[Advanced Features](/advanced)** — virtual hosting, style rendering,
  name resolution internals
