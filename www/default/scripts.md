# Server-Side Scripts

bserver runs server-side scripts in four languages to generate HTML
dynamically: **Python**, **JavaScript** (embedded), **PHP**, and **Shell**
(`sh`/`bash`). Use them when a page needs logic beyond what static YAML
expresses.

bserver supports server-side scripting in three places, each with its own
chapter:

| Context | Prefix / mechanism | What it does |
|---------|--------------------|--------------|
| **Format script** | `^name` with `script:` | Renders a value as HTML (this page) |
| **Data source**   | `$name` with `script:` | Produces the value of a name as JSON ([Data Sources](/data-sources)) |
| **`.php` file**   | A file ending in `.php` | Handled by `php-cgi` as a normal PHP page (see [PHP files](#php-files)) |

This page covers the first — format scripts.

## How It Works

Scripts are defined in format definitions using the `script:` field:

```yaml
^my-renderer:
  script: python
  code: |
    print(f'<p>{record["key"]}: {record["value"]}</p>')
```

When bserver encounters content with this format, it:

1. Serializes the content data as JSON
2. Passes it to the script via stdin
3. Wraps your code in a loop that iterates over each record
4. Captures stdout as the rendered HTML

## Script Languages

### Python

```yaml
^renderer:
  script: python
  code: |
    name = record.get('key', '')
    value = record.get('value', '')
    print(f'<div class="item">{name}: {value}</div>')
```

Available variable: `record` (a Python dict)

bserver looks for `python3` first, then `python`.

### JavaScript (embedded)

```yaml
^renderer:
  script: javascript
  code: |
    print('<div class="item">' + record.key + ': ' + record.value + '</div>');
```

JavaScript runs in an **embedded interpreter** ([goja](https://github.com/dop251/goja))
— no Node.js process is forked. This makes it the fastest scripting option
and the right default for short rendering snippets.

Available per-iteration variable: `record` (a JS object).

Host builtins available to every JS script:

| Helper | Description |
|--------|-------------|
| `env` | Object of CGI environment variables: `env.REQUEST_URI`, `env.DOCUMENT_ROOT`, ... |
| `print(...args)` | Append a space-joined line to captured output |
| `listdir(path)` | List directory entries (path must be under `DOCUMENT_ROOT`) |
| `readFile(path)` | Read an entire file as a string |
| `readFileHead(path, n)` | Read the first `n` bytes |
| `joinPath(a, b, ...)` | Path concatenation |
| `splitExt(name)` | Returns `[basename, ext]` |

File access is restricted to paths under the vhost's `DOCUMENT_ROOT`,
plus up to `parent-levels` directories above it — the same scope used by
YAML name resolution, so a script can read the shared YAML files
(e.g. `../fontawesome.yaml`) that the YAML resolver can also see.
Attempts to escape this scope via `..` or absolute paths are rejected.

Aliases: `javascript`, `js`, `node`.

### PHP

```yaml
^renderer:
  script: php
  code: |
    echo "<div class='item'>{$record['key']}: {$record['value']}</div>\n";
```

Available variable: `$record` (a PHP associative array)

### Shell (sh/bash)

```yaml
^renderer:
  script: sh
  code: |
    echo "<div class='item'>$(echo "$RECORD" | jq -r '.key'): $(echo "$RECORD" | jq -r '.value')</div>"
```

Available variable: `$RECORD` (a JSON string of the current record)

Use `jq` to extract fields from `$RECORD`. Aliases: `sh`, `bash`, `shell`

bserver looks for `bash` first, then `sh`.

## Data Format

### Map Content

When the content is a map (ordered key-value pairs), each entry becomes a
record with `key` and `value` fields:

```yaml
navlinks:
  "/": Home
  "/about": About
  "/contact": Contact
```

The script receives via stdin:

```json
[
  {"key": "/", "value": "Home"},
  {"key": "/about", "value": "About"},
  {"key": "/contact", "value": "Contact"}
]
```

### List Content

When content is a list of maps, each map becomes a record directly:

```yaml
products:
  - name: Widget
    price: "$9.99"
    sku: WDG-001
  - name: Gadget
    price: "$19.99"
    sku: GDG-001
```

The script receives:

```json
[
  {"name": "Widget", "price": "$9.99", "sku": "WDG-001"},
  {"name": "Gadget", "price": "$19.99", "sku": "GDG-001"}
]
```

### Null/Empty Content

Scripts can run even without content data. If the format references a name
that has no definition, the script receives `null` (wrapped as `[null]`).
This is useful for scripts that generate content entirely from environment
variables or external sources.

## External Script Files

Instead of inline code, you can reference an external file:

```yaml
^renderer:
  script: php
  file: scripts/render.php
```

The file path is relative to the document root. For PHP files, `<?php` and
`?>` tags are automatically stripped since bserver provides the execution
wrapper.

## Environment Variables

Scripts have access to CGI-like environment variables, making them behave
similarly to scripts running under Apache or nginx:

### Always Available

| Variable | Description |
|----------|-------------|
| `REQUEST_URI` | URL path for the current request (e.g., `/about`) |
| `DOCUMENT_ROOT` | Filesystem path to the document root |
| `REDIRECT_STATUS` | Always `200` |
| `SCRIPT_NAME` | Same as REQUEST_URI |
| `PHP_SELF` | Same as REQUEST_URI (for PHP compatibility) |

### Available with HTTP Requests

| Variable | Description |
|----------|-------------|
| `REMOTE_ADDR` | Client IP address |
| `SERVER_NAME` | Server hostname |
| `SERVER_ADDR` | Server IP address |
| `SERVER_PORT` | Server port (`80` or `443`) |
| `HTTP_HOST` | HTTP Host header |
| `QUERY_STRING` | URL query string |
| `REQUEST_METHOD` | HTTP method (GET, POST, etc.) |
| `SERVER_PROTOCOL` | Protocol version (e.g., HTTP/1.1) |
| `GATEWAY_INTERFACE` | Always `CGI/1.1` |
| `SERVER_SOFTWARE` | Always `bserver` |
| `SCRIPT_FILENAME` | Path to script file (if using `file:`) |
| `CONTENT_TYPE` | Request Content-Type header |
| `CONTENT_LENGTH` | Request Content-Length |

All HTTP request headers are also available as `HTTP_*` variables (e.g.,
`HTTP_USER_AGENT`, `HTTP_ACCEPT`).

## Real-World Example: Active Navigation

The built-in navbar uses Python scripting to highlight the current page:

```yaml
^navlinks:
  script: python
  code: |
    import os, html as _html
    page = os.environ.get('REQUEST_URI', '/')
    link = record.get('key', '')
    text = record.get('value', '')
    active = ' active bg-primary bg-opacity-10' if link == page else ''
    print(f'<li class="nav-item">'
          f'<a class="nav-link{active}" '
          f'href="{_html.escape(link)}">'
          f'{text}</a></li>')
```

This reads the current page URL from `REQUEST_URI` and adds Bootstrap's
`active` class to the matching navigation link.

## Script Wrapper Details

bserver wraps your code in a language-specific boilerplate:

### Python Wrapper

```python
import json, sys
_data = json.loads(sys.stdin.read())
if not isinstance(_data, list): _data = [_data]
for record in _data:
    # your code here (indented 4 spaces)
```

### JavaScript Wrapper

```javascript
var _data = _SCRIPT_DATA ? JSON.parse(_SCRIPT_DATA) : [];
if (!Array.isArray(_data)) _data = [_data];
for (var _i = 0; _i < _data.length; _i++) {
  var record = _data[_i];
  (function(){
    // your code here
  }).call(this);
}
```

The data is passed via the `_SCRIPT_DATA` variable that bserver injects
into the embedded VM — not via stdin or `fs`. Use the `print()` builtin
for output rather than `console.log`.

### PHP Wrapper

```php
$_data = json_decode(file_get_contents('php://stdin'), true);
if (!is_array($_data)) $_data = [$_data];
foreach ($_data as $record) {
  // your code here
}
```

### Shell Wrapper

```bash
_INPUT=$(cat)
_COUNT=$(printf '%s' "$_INPUT" | jq -r 'if type=="array" then length else 1 end')
_IDX=0
while [ "$_IDX" -lt "$_COUNT" ]; do
  RECORD=$(printf '%s' "$_INPUT" | jq -c "if type==\"array\" then .[${_IDX}] else . end")
  export RECORD
  # your code here
  _IDX=$((_IDX + 1))
done
```

The shell wrapper uses `jq` to parse JSON and iterate over records. Each
record is available as the `$RECORD` environment variable containing a JSON
string. Use `jq` within your code to extract fields:

```bash
name=$(echo "$RECORD" | jq -r '.key')
value=$(echo "$RECORD" | jq -r '.value')
echo "<p>$name: $value</p>"
```

**Note:** `jq` must be installed on the system for shell scripts to work.

## Execution Details

- **Working directory**: set to the document root, so relative file paths
  in your scripts resolve from there.
- **Timeout**: scripts have a 30-second execution timeout. If exceeded,
  the process is killed and an HTML comment is inserted.
- **Error handling**: script errors (non-zero exit) produce an HTML comment
  with the error message and stderr output.
- **Output**: everything written to stdout becomes part of the page HTML.
  Stderr is captured separately for error reporting.
- **Output limit**: stdout is capped at 10 MB to prevent runaway scripts
  from exhausting memory.
- **Environment leakage**: the server process's full environment is *not*
  inherited by scripts. Only `PATH`, `HOME`, the CGI variables listed
  above, and forwarded `HTTP_*` headers are exposed. Secrets in the
  parent environment stay in the parent.
- **POST bodies**: the request body is piped on the script's **stdin**
  (Python/PHP/Shell) — it is not put into an environment variable, so it
  is not subject to OS env-block limits and cannot be command-injected.
  For embedded JS, the body is not yet exposed in the embedded VM.
- **JS heap cap**: each JS invocation has a soft heap-growth cap
  (`js-heap-mb` in `_config.yaml`, default 128 MB) so a runaway script
  cannot exhaust the server's memory.

## PHP Files

In addition to inline PHP via the `^php` format, bserver serves any file
ending in `.php` as a regular CGI request through the system `php-cgi`
binary. This is what you want for traditional PHP apps and for any script
that uses sessions, custom headers, or large file uploads.

```
www/example.com/contact.php
```

Visiting `/contact.php` runs that file through `php-cgi` with the standard
CGI environment (`SERVER_NAME`, `REQUEST_METHOD`, `QUERY_STRING`,
`HTTP_HOST`, all `HTTP_*` headers, etc.). The request body is piped on
stdin.

Per-vhost PHP behavior is controlled by two `_config.yaml` settings:

| Setting | Default | Description |
|---------|---------|-------------|
| `php-timeout` | 60 | Idle timeout: if `php-cgi` produces no output for this many seconds, it is killed. Total runtime is *not* capped, so long-running scripts that keep printing can run indefinitely. |
| `php-stream-after` | 5 | Buffer `php-cgi` output for this many seconds before switching to chunked streaming. Short responses get a buffered reply with `Content-Length`; long ones stream incrementally. Set to 0 to stream immediately. |

The path to `php-cgi` is auto-detected from `$PATH` and common
locations, and can be overridden with `php:` in `_config.yaml` or the
`PHP_CGI` environment variable. If `php-cgi` is not found, a warning is
logged at startup and `.php` files return 404.

## Inline PHP With Sessions and Headers

The inline `^php` format also supports `session_start()`, `header()`, and
`setcookie()`. Output is buffered with `ob_start()` while the script runs;
any headers PHP queues are extracted and added to the HTTP response.
Session cookies are set automatically when `session_start()` is called.
See `default/session.yaml` for a complete demo.

## Next Steps

- [Data Sources](/data-sources) — `$name` scripts that produce data rather than HTML
- [Built-in Components](/components) — see the navbar's script in context
- [Advanced Features](/advanced) — virtual hosting, custom tags, and more
