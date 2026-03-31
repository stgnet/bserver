# Tips & Recipes

Practical patterns for common web development tasks using bserver's YAML system.

## Redirects

To redirect an entire site (or page) to another URL, create an `index.yaml`
that uses the built-in `redirect` format to inject a meta refresh into the head:

```yaml
+head:
  - redirect: https://example.com/new-location
main:
  - p: "Redirecting..."
  - links:
      https://example.com/new-location: example.com
```

The `main:` content is a fallback for clients that don't follow meta refresh.

This is useful for retiring a virtual host and pointing it to a new location,
or for creating short-URL aliases.

## Client-Side JavaScript

For interactive features like countdowns, form validation, or dynamic content,
embed a `<script>` tag using `raw:` inside your page content:

```yaml
main:
  - raw: |
      <div id="output"></div>
      <script>
      document.getElementById('output').textContent = 'Hello from JavaScript!';
      </script>
```

Note: bserver's `javascript:` key runs server-side Node.js scripts. For
client-side browser JavaScript, always use `raw:` with a `<script>` tag.

## Raw HTML When You Need It

The `raw:` format passes content through without escaping, so you can
drop in any HTML. This is useful for things that don't have a YAML equivalent:

```yaml
main:
  - raw: |
      <video controls width="100%">
        <source src="/videos/demo.mp4" type="video/mp4">
      </video>
```

Compare with the normal behavior where text content is HTML-escaped:

```yaml
main:
  - p: "This <b>bold</b> text will be escaped and shown literally"
```

## Inline Format Definitions

Define custom elements directly in your page file using the `^name:` syntax.
This is useful for one-off components that don't need to be shared:

```yaml
main:
  - alert-box: Something important happened!
  - alert-box: Check your settings.

^alert-box:
  tag: div
  params:
    class: alert alert-warning
    role: alert
  content: $*
```

Each `alert-box:` in the content will render as a styled Bootstrap alert div.

## Embedding Iframes

For embedding external widgets, maps, or video players:

```yaml
main:
  - card:
      - h3: Live Dashboard
      - raw: <iframe src="https://example.com/dashboard" width="100%" height="400" frameborder="0"></iframe>
```

## Cards with Columns

Use `row:` and `col:` for side-by-side layouts inside a card:

```yaml
main:
  - card:
      row:
        - col:
            markdown: |
              ![](/images/photo.jpg)
        - col:
            markdown: |
              ### About This

              Description text goes here with **markdown** formatting.
```

## Adding Custom CSS

Use `+style:` to append CSS rules to the inherited styles without
replacing them. The format uses CSS selectors as keys and
`property: value` pairs underneath:

```yaml
+style:
  .highlight:
    background-color: yellow
  .sidebar:
    border-left: 3px solid #007bff
    padding-left: 1rem
```

This can go in a site-wide file (like `title.yaml`) to apply to all
pages, or directly in a page's YAML to apply only there.

**Important:** Using `style:` (without the `+` prefix) would *replace*
all inherited styles, breaking the base layout. Always use `+style:`
to add to the existing styles.

## Linking with Icons

Use `links:` with Font Awesome icon names (included by default) to create
icon-prefixed links. Each entry maps a URL to a list containing the icon
name and the link text:

```yaml
main:
  - links:
      /files/document.pdf:
        - fa-solid-download
        - Download PDF
      mailto:hello@example.com:
        - fa-solid-envelope
        - Contact Us
      /about:
        - fa-solid-circle-info
        - About Us
```

See the [Icons](/icons) page for all available icon definitions.

## Page Title and Meta Description

Override these per-site in a `title.yaml` and by redefining `meta:`:

```yaml
title: My Site Name

meta:
  viewport: width=device-width, initial-scale=1
  description: A brief description of this site for search engines
```

Or per-page by including the definition at the top of your page's YAML file.

## Hiding and Showing Content with IDs

When you need JavaScript to toggle visibility, define a format with an `id`
param and use `style` to set the initial state:

```yaml
main:
  - toggle-btn
  - secret-content:
      - p: This content is initially hidden.

^toggle-btn:
  tag: button
  params:
    class: btn btn-primary
    onclick: "document.getElementById('secret').style.display = document.getElementById('secret').style.display === 'none' ? '' : 'none'"
  content: Toggle Content

^secret-content:
  tag: div
  params:
    id: secret
    style: "display:none"
```

## Symlinks for Domain Aliases

Instead of duplicating content, use filesystem symlinks to serve the same
site under multiple domains:

```
cd www/
ln -s mysite.com www.mysite.com
```

Both `mysite.com` and `www.mysite.com` will serve identical content from
the same directory. This also works for shorthand aliases like
`ln -s mysite.com m.mysite.com`.
