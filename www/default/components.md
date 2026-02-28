# Built-in Components

bserver includes a set of pre-defined YAML files that provide common HTML
patterns. These live in the content root directory (`www/`) and are inherited
by all sites through the name resolution system.

## Page Structure

### html.yaml

The root of every page. Defines the HTML element with `lang="en"` and
includes head and body:

```yaml
html:
  - head
  - body

^html:
  tag: html
  params:
    lang: en
```

### head.yaml

Sets up the `<head>` section with meta tags, stylesheets, and styles:

```yaml
head:
  - title
  - meta
  - headlink
  - style

meta:
  viewport: width=device-width, initial-scale=1
  description: This should be replaced

^meta:
  tag: meta
  params:
    name: '$key'
    content: '$value'

^headlink:
  tag: link
  params: '$*'
```

The `^meta` format iterates over the `meta:` map, producing one `<meta>` tag
per entry. The `^headlink` format uses wildcard params (`$*`), so each
headlink entry's keys become HTML attributes directly.

Override `title:` and `meta:` in your site to customize the page title and
meta description.

### body.yaml

Defines the page body structure:

```yaml
body:
  - header
  - main
  - footer
```

This is why your pages define `main:` — it fills the middle section between
the header and footer.

### main.yaml

Wraps the main content in a Bootstrap container:

```yaml
^main:
  tag: div
  params:
    class: container mt-4
```

Note: `main` is also a known HTML tag, but the `^main` format overrides it
to render as a `<div>` with Bootstrap classes instead of a plain `<main>` tag.

## Bootstrap Integration

### bootstrap5.yaml

Loads Bootstrap 5 CSS from the jsDelivr CDN:

```yaml
bootstrap5:

+headlink:
  - rel: stylesheet
    href: https://cdn.jsdelivr.net/npm/bootstrap@5.3.3/dist/css/bootstrap.min.css
    integrity: sha384-QWTKZyjpPEjISv5WaRU9OFeRpok6YctnYmDr5pNlyT2bRjXh0JMhjY6hW+ALEwIH
    crossorigin: anonymous
```

The `bootstrap5:` key is intentionally empty — it exists only to be referenced
so the file gets loaded. The real work is in `+headlink:`, which merges the
Bootstrap CSS link into the head section.

The navbar component references `bootstrap5:` to ensure it's loaded.

### fontawesome.yaml

Similarly loads Font Awesome icons:

```yaml
fontawesome:

+headlink:
  - rel: stylesheet
    href: https://cdnjs.cloudflare.com/ajax/libs/font-awesome/7.0.1/css/all.min.css
```

Reference `fontawesome` in your content to include icon support.

## Navigation

### navbar.yaml

A complete Bootstrap 5 responsive navbar with collapsible menu. The structure
is built from several interconnected format definitions:

```yaml
navbar:
  bootstrap5:
  navbar-cf:
    navbar-toggler: navbar-toggler-icon
    navbar-collapse:
      navbar-nav:
        - navlinks
      navbar-nav-right:
        - navlinksright
```

The key formats:

- `^navbar` → `<nav class="navbar navbar-expand-lg navbar-dark bg-primary">`
- `^navbar-cf` → `<div class="container-fluid">`
- `^navbar-toggler` → hamburger button for mobile
- `^navbar-collapse` → collapsible menu wrapper
- `^navbar-nav` → `<ul class="navbar-nav">` (left-aligned links)
- `^navbar-nav-right` → `<ul class="navbar-nav ms-auto">` (right-aligned links)

### navlinks (script-based)

The `^navlinks` format uses Python scripting to generate navigation links
with active-page highlighting:

```yaml
^navlinks:
  script: python
  code: |
    import os, html as _html
    page = os.environ.get('REQUEST_URI', '/')
    link = record.get('key', '')
    text = record.get('value', '')
    active = ' active bg-primary bg-opacity-10' if link == page else ''
    print(f'<li class="nav-item"><a class="nav-link{active}" href="{_html.escape(link)}">{text}</a></li>')
```

This reads the current page URL from the `REQUEST_URI` environment variable
and adds the `active` class to the matching link. Define your links in
`navlinks.yaml`:

```yaml
navlinks:
  "/": Home
  "/about": About
  "/contact": Contact
```

### navlinksright (with dropdown support)

The `^navlinksright` format renders right-side navigation items and supports
Bootstrap 5 dropdown menus. When a YAML value is a nested map, it renders
as a dropdown; simple key-value pairs render as regular links:

```yaml
navlinksright:
  Info:
    https://github.com/example/repo: Repo
    "mailto:user@example.com": Author
  "/settings": Settings
```

The nested map under `Info` produces a Bootstrap 5 dropdown menu with the
toggle label "Info" and the child entries as dropdown items. The flat
`/settings` entry renders as a regular nav link.

## Content Elements

### link.yaml

A single link with named variable substitution:

```yaml
^link:
  tag: a
  params:
    href: '$url'
  content: '$contents'
```

Usage:

```yaml
- link:
    url: /about
    contents: About Us
```

Renders: `<a href="/about">About Us</a>`

With the container pattern, siblings provide content:

```yaml
- link: /about
  text: About Us
```

### links.yaml

Multiple links from a map, using `$key`/`$value` iteration:

```yaml
^links:
  tag: a
  params:
    href: '$key'
  contents: '$value'
```

Usage:

```yaml
links:
  /about: About Us
  /contact: Contact
```

Renders:

```html
<a href="/about">About Us</a>
<a href="/contact">Contact</a>
```

### image.yaml

Image tag with pass-through source:

```yaml
^image:
  tag: img
  params:
    src: '$*'
```

Usage: `image: photos/hero.jpg` renders `<img src="photos/hero.jpg">`

### ulist.yaml

Unordered list that wraps each item in `<li>`:

```yaml
^ulist:
  tag: ul
  contents:
    li: '$*'
```

Usage:

```yaml
ulist:
  - First item
  - Second item
  - Third item
```

Renders:

```html
<ul>
  <li>First item</li>
  <li>Second item</li>
  <li>Third item</li>
</ul>
```

The `contents:` (plural) is key here — it wraps each list item individually.

### ulinks.yaml

Unordered list of links, combining `ulist` and `link` patterns:

```yaml
^ulinks:
  tag: ul
  contents:
    li:
      link: '$*'
```

## Layout Components

### container.yaml

Bootstrap container:

```yaml
^container:
  tag: div
  params:
    class: container
```

### row.yaml

Bootstrap grid row:

```yaml
^row:
  tag: div
  params:
    class: row
```

### col.yaml

Bootstrap grid column:

```yaml
^col:
  tag: div
  params:
    class: col
```

Usage:

```yaml
main:
  container:
    row:
      - col: "Column 1 content"
      - col: "Column 2 content"
```

### card.yaml

Bootstrap card:

```yaml
^card:
  tag: div
  params:
    class: card
```

### section.yaml

HTML section element:

```yaml
^section:
  tag: section
```

### muted.yaml

Muted text with small styling:

```yaml
^muted:
  tag: div
  params:
    class: text-muted small
  content: '$*'
```

Used in the default footer to display subtle text.

## Creating Your Own Components

To create a custom component, add a `.yaml` file in your site directory or the
content root:

```yaml
# alert.yaml
^alert:
  tag: div
  params:
    class: alert alert-warning
  content: '$*'
```

Then use it in your pages:

```yaml
main:
  - alert: "Warning: This is important!"
```

Components in your site directory override same-named components in parent
directories.

## Next Steps

- [Format Definitions](/formats) - Deep dive into the `^` syntax
- [Server-Side Scripts](/scripts) - Dynamic components with code
- [Advanced Features](/advanced) - Virtual hosting, custom tags, and more
