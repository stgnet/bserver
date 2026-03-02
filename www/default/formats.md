# Format Definitions

Format definitions are the core of bserver's reusability. They define how a
name should render as HTML, using the `^` prefix.

## Basic Format

```yaml
^muted:
  tag: div
  params:
    class: text-muted small
  content: '$*'
```

This creates a format named `muted` that renders as a `<div>` with the
specified CSS classes. The `$*` in `content:` means "pass through whatever
content is provided."

Usage:

```yaml
footer:
  muted: This is footer text
```

Renders:

```html
<footer>
  <div class="text-muted small">This is footer text</div>
</footer>
```

## The ^ Prefix

The caret (`^`) prefix is **required** for format definitions. Without it, the
key is treated as a content definition, not a format:

```yaml
# CORRECT: Format definition - registered in the formats registry
^muted:
  tag: div
  params:
    class: text-muted small

# WRONG: Content definition - stored as data, NOT rendered as a format
muted:
  tag: div
  params:
    class: text-muted small
```

This is a common gotcha. If your format isn't working, check for the `^`
prefix first.

## Format Fields

### tag

The HTML tag to render:

```yaml
^card:
  tag: div
  params:
    class: card
```

Usage: `card: "Card content"` renders `<div class="card">Card content</div>`

### params

HTML attributes for the tag. Can use static values or variable substitution:

```yaml
# Static params
^container:
  tag: div
  params:
    class: container

# Variable params
^link:
  tag: a
  params:
    href: '$url'
  content: '$contents'
```

### content (singular)

Defines how inner content is rendered:

- `content: '$*'` — pass through content as-is
- `content: '$varname'` — substitute a named variable
- `content: {wrapper: '$*'}` — wrap all content in a structural wrapper

```yaml
# Pass-through: content rendered inside the tag directly
^muted:
  tag: div
  params:
    class: text-muted
  content: '$*'

# Named variable: specific field used as content
^link:
  tag: a
  params:
    href: '$url'
  content: '$contents'

# Structural wrapper: all content wrapped in a sub-element
^card:
  tag: div
  params:
    class: card
  content:
    card-body: '$*'
```

### contents (plural)

The plural form `contents:` works like `content:` for string values, but
for structural wrappers it wraps **each item individually** rather than
wrapping all content as a single block:

```yaml
# Singular: ONE <li> wrapping ALL items together
^single-wrap:
  tag: ul
  content:
    li: '$*'

# Plural: one <li> PER ITEM
^ulist:
  tag: ul
  contents:
    li: '$*'
```

With `ulist:` and content `["apple", "banana", "cherry"]`:

```html
<!-- contents: (plural) - each item wrapped individually -->
<ul>
  <li>apple</li>
  <li>banana</li>
  <li>cherry</li>
</ul>
```

This distinction is crucial for list rendering. The built-in `^ulist` uses
`contents:` (plural) so each list item gets its own `<li>`.

### params: '$*' (Wildcard Params)

When params is the string `$*`, each content entry's key-value pairs become
HTML attributes directly:

```yaml
^headlink:
  tag: link
  params: '$*'
```

With content:

```yaml
headlink:
  - rel: stylesheet
    href: /styles.css
    crossorigin: anonymous
```

Renders:

```html
<link rel="stylesheet" href="/styles.css" crossorigin="anonymous">
```

This is how `<meta>` and `<link>` tags are generated from structured YAML
data.

## Variable Substitution

### $key and $value

For iterating over map entries where each entry produces its own HTML element:

```yaml
^meta:
  tag: meta
  params:
    name: '$key'
    content: '$value'
```

With:

```yaml
meta:
  viewport: width=device-width, initial-scale=1
  description: My site description
  author: Jane Doe
```

Renders:

```html
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="description" content="My site description">
<meta name="author" content="Jane Doe">
```

Each map entry produces its own `<meta>` tag.

### Named Variables ($url, $contents, etc.)

Format params and content can reference named variables that come from the
content map's keys:

```yaml
^link:
  tag: a
  params:
    href: '$url'
  content: '$contents'
```

Usage with a map providing the variable values:

```yaml
- link:
    url: /about
    contents: About Us
```

Renders: `<a href="/about">About Us</a>`

### $* (Pass-through)

`$*` in `content:` means "render the provided content as-is":

```yaml
^muted:
  tag: div
  params:
    class: text-muted small
  content: '$*'
```

Usage: `muted: Hello world` renders
`<div class="text-muted small">Hello world</div>`

### Unreplaced Variables

If a variable in params can't be resolved (no matching key in the content
data), the entire attribute is omitted. This prevents broken HTML output
from partially-resolved templates.

## The Container Pattern

When a map has a formatted key whose format uses `$var` in content (like
`^link`), and there are sibling entries in the map, the siblings become
children of that container element:

```yaml
- link: /about
  text: Read more about us
```

Here, `^link` defines `content: '$contents'`. Since there's no `contents`
key but there is a `text` sibling, bserver renders the text as a child of
the link:

```html
<a href="/about">Read more about us</a>
```

This works because `text` is recognized as literal content to place inside
the container element.

## Iteration with Lists of Maps

When content is a list of maps and the format uses named variables, each
map in the list produces its own element:

```yaml
^navitem:
  tag: a
  params:
    href: '$url'
    class: nav-link
  content: '$label'
```

With:

```yaml
navitem:
  - url: /
    label: Home
  - url: /about
    label: About
  - url: /contact
    label: Contact
```

Renders:

```html
<a href="/" class="nav-link">Home</a>
<a href="/about" class="nav-link">About</a>
<a href="/contact" class="nav-link">Contact</a>
```

## Combining Formats

Formats compose naturally. A `^ulist` wrapping `links`:

```yaml
^ulist:
  tag: ul
  contents:
    li: '$*'

^links:
  tag: a
  params:
    href: '$key'
  contents: '$value'
```

With:

```yaml
ulist:
  links:
    /about: About
    /contact: Contact
```

Renders:

```html
<ul>
  <li><a href="/about">About</a></li>
  <li><a href="/contact">Contact</a></li>
</ul>
```

The `contents:` (plural) on both formats ensures each link gets its own
`<li>` wrapper.

## Page-Level Format Overrides

If your page YAML file defines a `^format`, it takes precedence over formats
loaded from parent directories. This allows individual pages to customize
rendering behavior:

```yaml
# In your page file:
^card:
  tag: div
  params:
    class: card shadow-lg

main:
  - card: "This card has a shadow"
```

## Next Steps

- [Built-in Components](/components) - See all pre-defined formats
- [Server-Side Scripts](/scripts) - Dynamic rendering with code

## Layout Primitives (New)

Format definitions can now apply common layout behavior directly with format
fields, without manually writing `style:` everywhere.

### Supported layout fields

- `layout`: `flex`, `grid`, or `stack`
- `gap`: spacing between children (for example `1rem`)
- `columns`: grid template columns (for example `repeat(3, 1fr)`)
- `align`: maps to `align-items`
- `justify`: maps to `justify-content`
- `wrap`: `true` enables `flex-wrap: wrap` for flex/stack layouts

Example:

```yaml
^cards:
  tag: section
  layout: grid
  columns: repeat(3, minmax(0, 1fr))
  gap: 1rem
  content: '$*'
```

## Component Props: variants, defaults, required (New)

Formats can now model reusable component APIs.

### defaults

Provide fallback values for variables used in params/content:

```yaml
^button:
  tag: a
  defaults:
    variant: primary
```

### variants

Map `variant` names to attribute sets (typically classes):

```yaml
^button:
  tag: a
  variants:
    primary:
      class: btn btn-primary
    ghost:
      class: btn btn-outline-secondary
  params:
    href: '$url'
  content: '$label'
```

Use by passing `variant` in content data:

```yaml
- button:
    url: /contact
    label: Contact
    variant: ghost
```

### required

Declare required props. If missing, bserver emits an HTML comment describing
missing props so template errors are visible in page output/debugging:

```yaml
^button:
  tag: a
  required: [url, label]
```
