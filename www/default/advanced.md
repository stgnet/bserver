# Advanced Features

## Virtual Hosting

bserver serves different content based on the request hostname. Each domain
gets its own document root directory:

```
/var/www/
├── example.com/
│   ├── index.yaml
│   ├── navlinks.yaml
│   └── style.yaml
├── blog.example.com/
│   ├── index.yaml
│   └── navlinks.yaml
└── default/
    └── index.yaml
```

When a request comes in for `example.com`, bserver looks for content in
`/var/www/example.com/`. The `default/` directory serves as the fallback
for any host that doesn't have a dedicated directory.

## Let's Encrypt (HTTPS)

bserver supports automatic HTTPS with Let's Encrypt certificates via the
`golang.org/x/crypto/acme/autocert` package. When HTTPS is configured,
certificates are automatically obtained and renewed for the domains being
served.

## Known HTML Tags

bserver recognizes standard HTML5 tags. When a YAML key matches a known tag
name, it is rendered directly as that HTML element rather than being treated
as a name to resolve. For custom elements or non-standard tags, use a format
definition instead:

```yaml
^my-component:
  tag: my-component

main:
  - my-component: "Content inside custom element"
```

Renders: `<my-component>Content inside custom element</my-component>`

### Recognized Tags

**Document:** `html`, `head`, `body`, `title`, `meta`, `link`, `style`,
`script`

**Content:** `div`, `span`, `p`, `br`, `hr`, `h1`-`h6`, `a`, `img`,
`pre`, `code`, `blockquote`

**Text:** `strong`, `em`, `b`, `i`, `u`, `small`

**Lists:** `ul`, `ol`, `li`

**Tables:** `table`, `tr`, `td`, `th`, `thead`, `tbody`

**Forms:** `form`, `input`, `button`, `textarea`, `select`, `option`,
`label`, `fieldset`, `legend`

**Semantic:** `header`, `footer`, `nav`, `main`, `section`, `article`,
`aside`, `details`, `summary`

**Media:** `video`, `audio`, `source`, `canvas`

**Other:** `embed`, `area`, `base`, `col`, `track`, `wbr`

### Void Elements

These tags are self-closing and never produce a closing tag:

`meta`, `link`, `br`, `hr`, `img`, `input`, `source`, `area`, `base`,
`col`, `embed`, `track`, `wbr`

## Style Rendering

The `style:` name is special — its content is rendered as CSS rather than
HTML elements:

```yaml
style:
  body:
    font-family: sans-serif
    margin: 0
    padding: 0
  .header:
    background-color: "#2c3e50"
    color: white
    padding: 1rem
  .content p:
    line-height: 1.6
    max-width: 800px
```

Renders:

```html
<style>
body {
  font-family: sans-serif;
  margin: 0;
  padding: 0;
}
.header {
  background-color: #2c3e50;
  color: white;
  padding: 1rem;
}
.content p {
  line-height: 1.6;
  max-width: 800px;
}
</style>
```

Each top-level key is a CSS selector, and its map entries become CSS
property-value pairs.

### Merging Styles

Component files can add styles using the `+style` merge prefix:

```yaml
+style:
  .my-component:
    border: 1px solid "#ccc"
    border-radius: 4px
```

This adds your CSS rules to whatever styles already exist.

## Directory Resolution

When a request comes in for a directory path like `/service/`, bserver
looks for content in this order:

1. **Index file**: `service/index.yaml` (or `index.md`, `index.php`, etc.)
2. **Name-based fallback**: `service/service.yaml` (directory name matches
   file name)

This allows clean URL patterns:

```
mysite.com/
├── service/
│   └── service.yaml    # Served at /service/
├── products/
│   └── products.yaml   # Served at /products/
└── index.yaml          # Served at /
```

The request URI is computed by stripping the file extension and handling
the directory-name matching: `service/service.yaml` becomes `/service`.

## Markdown Pages

Any `.md` file in the document root is automatically rendered as a full HTML
page. The markdown content is:

1. Converted to HTML using the Goldmark library (with unsafe HTML enabled)
2. Injected as the `main` content definition
3. Wrapped in the full site structure (html, head, body, navbar, footer)

This means markdown files automatically get:

- DOCTYPE and proper HTML structure
- Meta tags and stylesheets
- Navigation bar
- Footer
- Any styles defined in the site

Inline HTML in markdown is preserved (not escaped), so you can mix markdown
with raw HTML tags, images, iframes, etc.

## Name Resolution Order

When bserver encounters a name reference, it searches for a definition:

1. **Already loaded**: Check definitions already in memory (from previously
   processed YAML files)
2. **Current directory**: Look for `name.yaml` in the request directory
3. **Parent directories**: Walk upward through parent directories
4. **Ceiling**: Stop at `maxParentLevels` above the document root (default: 1
   level above)

For example, with document root `/var/www/mysite.com/` and a request for
`/service/`:

```
Search order:
1. /var/www/mysite.com/service/name.yaml
2. /var/www/mysite.com/name.yaml
3. /var/www/name.yaml              (1 level above docRoot)
4. Stop (ceiling reached)
```

This cascading search is why shared definitions (like `html.yaml`,
`navbar.yaml`) in the content root directory work for all sites — they're
found when the search walks up from the site directory.

### Markdown Name Resolution

Names can also resolve to `.md` files. If `name.yaml` isn't found but
`name.md` exists, the markdown file is read, converted to HTML, and used
as the definition. This allows mixing YAML structure with markdown content
seamlessly.

## Two-Pass Rendering

bserver uses a two-pass rendering system:

### Pass 1: Resolution

Walks the entire name tree starting from `html`, loading all referenced YAML
files and processing `+` merges. This ensures that features like `+style`
from component files (e.g., Bootstrap) are applied before rendering begins.

### Pass 2: Rendering

Generates HTML from the fully-resolved definitions. At this point, all names
are loaded, all merges are applied, and all formats are registered.

This two-pass approach prevents ordering issues. For example, if `navbar.yaml`
adds `+headlink` entries for Bootstrap CSS, those entries are available in
the head section even though the navbar is defined after the head in the body
structure.

### Page-Level Format Overrides

Format definitions (`^name`) from the page's own YAML file are preserved
across both passes. If a component file loaded during resolution defines the
same format, the page-level definition takes precedence. This allows
individual pages to customize rendering.

## Cycle Detection

bserver tracks which names are currently being resolved/rendered. If a
name references itself (directly or indirectly), the cycle is broken with
an HTML comment:

```html
<!-- circular reference: "myname" -->
```

The maximum nesting depth is 50 levels to prevent runaway recursion.

## Undefined Names

If a name can't be resolved (no YAML file found, no definition loaded),
bserver outputs the word as plain text. This allows single words to be
placed adjacent to icons or other content without generating errors.

## Debug Mode

Add `?debug` to any URL to enable debug HTML comments throughout the rendered
output:

```html
<!-- resolve "html" from /path/to/html.yaml -->
<!-- ^html: tag="html" contents="" -->
<!-- key "head" -->
<!-- resolve "head" from /path/to/head.yaml -->
```

These comments trace the full resolution and rendering process, showing which
files are loaded, which formats are applied, and how content flows through
the system.

## Ordered Maps

bserver preserves the order of YAML map keys throughout parsing and rendering.
Standard Go maps are unordered, but bserver uses a custom OrderedMap
implementation to ensure that:

- HTML attributes appear in the order defined in YAML
- CSS properties maintain their YAML order
- Navigation links render in definition order
- Content sections appear in the expected sequence

This is important because YAML maps are technically unordered, but in
practice users expect their defined order to be preserved.

## Request URI Computation

bserver derives the URL path from the filesystem path:

| Filesystem Path | Request URI |
|-----------------|-------------|
| `mysite.com/index.yaml` | `/` |
| `mysite.com/about.yaml` | `/about` |
| `mysite.com/service/service.yaml` | `/service` |
| `mysite.com/blog/post.yaml` | `/blog/post` |

This computed URI is used for the `REQUEST_URI` environment variable in
scripts, enabling features like active-page navigation highlighting.

## Next Steps

- [Getting Started](/getting-started) - Set up your first site
- [Format Definitions](/formats) - Master the `^` syntax
- [Server-Side Scripts](/scripts) - Add dynamic rendering
