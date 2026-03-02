# Server-Side Scripts

bserver can execute server-side scripts in Python, JavaScript (Node.js), PHP,
or Shell (sh/bash) to dynamically generate HTML content. This is used for
rendering that requires logic beyond what static YAML can express.

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

### JavaScript (Node.js)

```yaml
^renderer:
  script: javascript
  code: |
    console.log(`<div class="item">${record.key}: ${record.value}</div>`);
```

Available variable: `record` (a JavaScript object)

Aliases: `javascript`, `js`, `node`

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
const _data = JSON.parse(require('fs').readFileSync(0, 'utf8'));
const _records = Array.isArray(_data) ? _data : [_data];
for (const record of _records) {
  // your code here
}
```

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

- **Working directory**: Set to the document root, so relative file paths
  in your scripts resolve from there.
- **Timeout**: Scripts have a 30-second execution timeout. If exceeded, the
  process is killed and an HTML comment is inserted.
- **Error handling**: Script errors (non-zero exit) produce an HTML comment
  with the error message and stderr output.
- **Output**: Everything written to stdout becomes part of the page HTML.
  Stderr is captured separately for error reporting.

## Next Steps

- [Built-in Components](/components) - See the navbar's script in context
- [Advanced Features](/advanced) - Virtual hosting, custom tags, and more
