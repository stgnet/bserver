# Error Handling

## YAML-Driven Error Pages

bserver renders error pages (such as 404 Not Found) through the same YAML
rendering pipeline as regular pages. This means error pages get the full site
chrome — navbar, footer, styles — instead of plain text responses.

## How It Works

When an error occurs (e.g., a page is not found), bserver looks for an error
template in the site's document root:

1. **Specific template first**: For a 404 error, it looks for `error404.yaml`
2. **Generic fallback**: If no specific template exists, it looks for `error.yaml`
3. **Plain text**: If neither exists, a plain text response is returned

## Pre-Seeded Variables

The rendering system pre-seeds these definitions, available via `$varname`
substitution in format definitions:

| Variable | Description | Example |
|----------|-------------|---------|
| `$errornumber` | HTTP status code | `404` |
| `$errordescription` | Standard status text | `Not Found` |
| `$errortitle` | Title combining code and description | `404 — Not Found` |
| `$errormessage` | Detail message with the requested path | `The requested page "/about" was not found.` |

## Default Error Template

The built-in `error.yaml` uses a `^error` format definition with `$var`
content substitution:

```yaml
^error:
  tag: div
  params:
    class: text-center py-5
  content:
    - h1: $errortitle
    - p: $errormessage

error:
```

The `^error` format wraps the error content in a centered `<div>`. The
`$errortitle` and `$errormessage` variables are replaced with the pre-seeded
values, producing HTML like:

```html
<div class="text-center py-5">
  <h1>404 — Not Found</h1>
  <p>The requested page "/about" was not found.</p>
</div>
```

## Custom Error Pages

### Override the generic template

Create your own `error.yaml` in your site's document root to customize the
look of all error pages:

```yaml
^error:
  tag: section
  params:
    class: error-container
  content:
    - h1: $errortitle
    - p: $errormessage
    - p: Please check the URL or return to the homepage.

error:
```

### Create specific error pages

Create `error404.yaml`, `error500.yaml`, etc. for per-status-code templates.
Specific templates take priority over the generic `error.yaml`:

```yaml
# error404.yaml — custom 404 page
^error404:
  tag: div
  params:
    class: not-found-page
  content:
    - h2: $errortitle
    - p: $errormessage

error404:
```

## Testing Error Pages

Try visiting a page that does not exist to see the error page in action:
[Test 404 page](/doesnotexist)

## Using $varname in Format Definitions

The `$varname` substitution used by error pages is a general-purpose feature
of the rendering engine. When a `$name` string appears as content inside a
format definition's `content:` list, it is replaced with the value of the
named definition. This works in two contexts:

- **Inline tag content**: `h1: $errortitle` renders the definition's value
  inside the `<h1>` tag
- **List items**: `- $errortitle` renders the definition's value as a
  standalone content block
