# Content Definitions

Content definitions are the foundation of bserver. Every piece of content on a
page comes from a YAML definition that maps a name to its content.

## Plain Definitions

The simplest form maps a name to content:

```yaml
main:
  - h1: "Page Title"
  - p: "Some content here."
```

When bserver encounters the name `main`, it looks up this definition and
renders it as HTML.

## Three Types of Definitions

YAML keys in bserver have three forms, distinguished by their prefix:

### Content Definitions (plain keys)

```yaml
footer:
  muted: This is the footer text
```

Plain keys define content. The first definition loaded wins - so your
page-level definition overrides inherited ones from parent directories.

### Format Definitions (^ prefix)

```yaml
^muted:
  tag: div
  params:
    class: text-muted small
  content: '$*'
```

The caret `^` prefix registers a format definition that controls how a name
renders as HTML. See [Format Definitions](/formats) for details.

### Merge Definitions (+ prefix)

```yaml
+headlink:
  - rel: stylesheet
    href: https://cdn.jsdelivr.net/npm/bootstrap@5.3.3/dist/css/bootstrap.min.css
```

The `+` prefix merges into an existing definition rather than replacing it.
This is how Bootstrap adds its stylesheet to the head section without
overwriting other stylesheets.

## String Values

A string value can be either **literal text** or a **name reference**:

```yaml
# Name reference - resolved to another definition
body:
  - header
  - main
  - footer

# Literal text - rendered as-is
title: My Website Title
```

**Name references** must start with a letter and contain only letters, digits,
hyphens, and underscores. Anything else is treated as literal text:

| String | Type | Why |
|--------|------|-----|
| `header` | Name reference | Letters only |
| `nav-links` | Name reference | Letters, hyphens |
| `col2` | Name reference | Letters, digits |
| `/about` | Literal text | Starts with `/` |
| `http://example.com` | Literal text | Contains `:` and `/` |
| `#333` | Literal text | Starts with `#` |
| `logo.png` | Literal text | Contains `.` |
| `hello world` | Literal text | Contains space |

## Lists

Lists define ordered content. Each item is rendered in sequence:

```yaml
body:
  - header
  - main
  - footer
```

List items can be strings (name references or literal text), maps (inline
tags), or nested lists.

## Maps (Inline Tags)

When a map key matches an HTML tag or has a format definition, the value
becomes the content inside that tag:

```yaml
main:
  - h1: "Welcome"
  - p: "Hello world"
  - div: "A div with text"
```

Renders as:

```html
<h1>Welcome</h1>
<p>Hello world</p>
<div>A div with text</div>
```

Maps can also nest:

```yaml
main:
  - div:
      h1: "Title"
      p: "Paragraph inside the div"
```

## Merge Behavior

The `+` prefix is powerful for composing definitions from multiple files.

### Map Merge

For map definitions, new keys are added and existing keys are overridden:

```yaml
# In base file:
meta:
  viewport: width=device-width, initial-scale=1

# In page file with +meta:
+meta:
  description: My page description
```

Result: meta has both `viewport` and `description`.

### List Merge

For list definitions, items are appended:

```yaml
# In base file:
headlink:
  - rel: stylesheet
    href: base.css

# In component file with +headlink:
+headlink:
  - rel: stylesheet
    href: bootstrap.min.css
```

Result: headlink has both stylesheet entries.

## First Definition Wins

For plain content definitions (without `+`), the first definition loaded takes
precedence:

```yaml
# In your page's index.yaml:
title: My Page Title

# In a parent directory's title.yaml:
title: Default Title
```

Since bserver loads page-local files first, your page's `title` definition
wins. This is how page-specific content overrides site-wide defaults.

## File-Based Definitions

Each YAML file can contain multiple definitions. The filename determines when
it's loaded:

- `footer.yaml` is loaded when the name `footer` is referenced
- It can contain the `footer:` content definition plus related format
  definitions (like `^muted:`)
- All definitions in the file are processed when it loads

For example, `navbar.yaml` contains both the `navbar:` content structure and
all the `^navbar-*` format definitions needed to render it.

## Null/Empty Values

A key with no value (or explicit null) produces an empty element:

```yaml
main:
  - hr:         # Self-closing <hr> tag
  - br:         # Self-closing <br> tag
  - div:        # Empty <div></div>
```

Void elements (like `hr`, `br`, `img`, `meta`, `link`) are automatically
self-closing and never produce a closing tag.

## Next Steps

- [Format Definitions](/formats) - Learn the `^` prefix system
- [Built-in Components](/components) - See what's already defined
