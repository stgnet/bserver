# Getting Started with bserver

## What is bserver?

bserver is a simple web server written in Go that generates HTML pages from YAML
definitions. Instead of writing HTML directly, you define your page structure
using YAML files, and bserver renders them into complete HTML pages with proper
DOCTYPE, head, body, and all the trimmings.

## Installation

Build from source:

```
go build
./bserver
```

By default, bserver listens on port 80 (HTTP) and serves content from the
`www/` subdirectory of the current working directory. Subdirectories matching
domain names are used as virtual host roots. Use the `-base` flag or
`BASE_DIR` environment variable to override the content directory.

## How It Works

bserver serves virtual hosts from subdirectories under its content root. For
example, if the content root is `/var/www/`, it will serve `example.com` from
`/var/www/example.com/`.

The `default/` directory is used as a fallback for any host that doesn't have
its own directory. This documentation site itself is the default site.

## Directory Structure

A typical bserver site looks like this:

```
mysite.com/
├── index.yaml          # Homepage content
├── about.yaml          # About page
├── navlinks.yaml       # Navigation links
├── header.yaml         # Header section (usually loads navbar)
├── footer.yaml         # Footer section
└── style.yaml          # Custom CSS styles (optional)
```

The content root directory (`www/`) contains shared definitions (html.yaml,
head.yaml, body.yaml, navbar.yaml, etc.) that are inherited by all sites.
You can copy any of these into your site directory to customize them.

## Your First Page

The simplest page needs only an `index.yaml` file with a `main:` definition:

```yaml
main:
  - h1: "Hello World"
  - p: "This is my first bserver page."
```

This renders inside the full HTML structure. bserver automatically provides:

- `<!DOCTYPE html>` declaration
- `<html lang="en">` with proper attributes
- `<head>` with meta tags and stylesheets
- `<body>` with header, your main content, and footer

## The Rendering Pipeline

1. bserver loads `html.yaml` as the starting point
2. `html.yaml` references `head` and `body`
3. `body.yaml` references `header`, `main`, and `footer`
4. Each name is resolved by searching for `name.yaml` files
5. Your `index.yaml` defines `main:` to set the page content

The pipeline looks like:

```
html.yaml
├── head.yaml → title, meta, headlink, style
└── body.yaml
    ├── header.yaml → navbar
    ├── main ← YOUR CONTENT (from index.yaml)
    └── footer.yaml → muted text
```

## Name Resolution

When bserver encounters a name reference like `footer`, it searches for
`footer.yaml`:

1. First in the current request directory (e.g., `mysite.com/`)
2. Then upward through parent directories
3. Up to the document root and one level above

This cascading search allows shared definitions (like the navbar) to live in
the content root directory while site-specific content lives in the site
directory. Your site's `navlinks.yaml` overrides the default navigation, but
the navbar structure itself is inherited.

## Creating Multiple Pages

Each `.yaml` file that defines `main:` becomes a page. For example:

**about.yaml:**
```yaml
main:
  - h1: "About Us"
  - p: "Welcome to our company."
```

This page is accessible at `/about` (bserver strips the `.yaml` extension).

You can also use Markdown files - any `.md` file is automatically rendered
with the same site structure (navbar, footer, etc.). See the
[Definitions](/definitions) page for more on how content is defined.

## Setting Up Navigation

Create a `navlinks.yaml` file in your site directory:

```yaml
navlinks:
  "/": Home
  "/about": About
  "/contact": Contact
```

Each key is the URL path, and the value is the display text. The navbar
automatically highlights the current page using server-side Python scripting.

## Adding Styles

Create a `style.yaml` file for custom CSS:

```yaml
style:
  body:
    font-family: sans-serif
  .custom-header:
    background-color: "#2c3e50"
    color: white
```

Or include Bootstrap 5 (already included in the default navbar) for a full
CSS framework.

## Configuration

bserver is configured through `_config.yaml` in the www directory. All settings
have sensible defaults — the file is optional. See `www/_config.yaml` for a
documented template with all available settings.

Per-site overrides: place a `_config.yaml` in a virtual host directory
(e.g., `www/example.com/_config.yaml`) to override `cache-age`, `static-age`,
`parent-levels`, `index`, and `max-body-bytes` for that site.

Environment variables override `_config.yaml` values:

| Variable | Config key | Default | Description |
|----------|------------|---------|-------------|
| `HTTP_ADDR` | `http` | `:80` | HTTP listen address |
| `HTTPS_ADDR` | `https` | `:443` | HTTPS listen address |
| `LE_EMAIL` | `email` | (empty) | Let's Encrypt contact email |
| `CERT_CACHE` | `cert-cache` | `./cert-cache` | Certificate cache directory |
| `PHP_CGI` | `php` | (auto-detected) | Path to php-cgi executable |
| `INDEX` | `index` | `index.yaml,index.md,...` | Index file search order |
| `BASE_DIR` | — | (empty) | Web content root directory |
| `MAX_BODY_BYTES` | `max-body-bytes` | `1048576` | Maximum request body size in bytes for dynamic handlers |


`max-body-bytes` defaults to 1 MiB and applies to dynamic request bodies used by YAML/Markdown script rendering and PHP CGI handling.

## Command-Line Flags

| Flag | Description |
|------|-------------|
| `-base` | Web content root directory (default: `www` subdirectory of cwd) |
| `-version` | Print version and exit |

See [Server Features](/server-features) for details on caching, security
headers, and other production features.

## Next Steps

- [Content Definitions](/definitions) - Learn how YAML keys become HTML
- [Format Definitions](/formats) - Create reusable HTML components
- [Built-in Components](/components) - Explore what's included
- [Server Features](/server-features) - Caching, security headers, and more
