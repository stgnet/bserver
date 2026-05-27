# Data Sources

Data sources let a name's *content* be produced by a script at request time,
instead of being hard-coded in YAML. They are declared with the `$` prefix and
behave just like a content definition once the script has run: the JSON
returned by the script becomes the value of that name and flows through the
normal rendering pipeline (including any matching `^name` format).

This is what powers the built-in `$navlinks` data source, which scans the
vhost directory and produces the nav bar from whatever pages it finds.

## Basic Shape

```yaml
$mydata:
  script: javascript
  code: |
    print(JSON.stringify([
      {key: "/", value: "Home"},
      {key: "/about", value: "About"}
    ]));
```

After this runs, the name `mydata` has the content
`{"/": "Home", "/about": "About"}` — identical to writing it out by hand:

```yaml
mydata:
  "/": Home
  "/about": About
```

So any format that consumes `mydata` (such as `^links`, `^ulist`, or a custom
`^mydata`) sees the script's output as if it were YAML.

## Fields

| Field    | Description |
|----------|-------------|
| `script` | Language: `javascript` (alias `js`, `node`), `python`, `php`, or `sh`/`bash` |
| `code`   | Inline script source |
| `file`   | Path to a script file, relative to the document root (alternative to `code`) |

The script's **stdout must be a JSON document**. Objects become OrderedMaps
(insertion order is preserved), arrays become lists.

## Difference From Format Scripts (`^name`)

| Aspect | Format script (`^name`) | Data source (`$name`) |
|--------|-------------------------|-----------------------|
| Purpose | Render a value as HTML | Produce content for a name |
| Output  | HTML fragment           | JSON |
| Called  | Once per record (iterated) | Once total |
| Sees `record` | Yes, per iteration   | No |

A common pattern is to pair them: a `$name` data source produces structured
data, and a `^name` format renders it.

## Language Notes

### JavaScript (embedded)

JavaScript runs in an embedded [goja](https://github.com/dop251/goja)
interpreter — no Node.js process is forked. Available host builtins:

| Helper | Description |
|--------|-------------|
| `env`  | Object of CGI environment variables: `env.REQUEST_URI`, `env.DOCUMENT_ROOT`, etc. |
| `print(...args)` | Append a space-joined line to captured output |
| `listdir(path)` | List entries in a directory (must be under `DOCUMENT_ROOT`) |
| `readFile(path)` | Read an entire file as a string |
| `readFileHead(path, n)` | Read the first `n` bytes of a file |
| `joinPath(a, b, ...)` | Path concatenation (like `path.join`) |
| `splitExt(name)` | `[basename, ext]` split |

File access is restricted to paths under the vhost's `DOCUMENT_ROOT`;
traversal via `..` or absolute paths outside the root is rejected.

### Python / PHP / Shell

For these languages bserver forks the system interpreter and runs your
`code` with the standard CGI-style environment variables set (see
[Server-Side Scripts](/scripts) for the full list). Write JSON to stdout.

## Built-in Example: `$navlinks`

The default `www/navlinks.yaml` auto-discovers nav items by scanning the
vhost root for pages:

```yaml
$navlinks:
  script: javascript
  code: |
    var docroot = env.DOCUMENT_ROOT || '.';
    var results = [{key: "/", value: "Home"}];
    var files = listdir(docroot);
    files.sort();
    for (var i = 0; i < files.length; i++) {
      var f = files[i];
      var parts = splitExt(f);
      var name = parts[0], ext = parts[1];
      if (name.charAt(0) === '.' || name.charAt(0) === '_' || name === 'index') continue;
      var title = name.replace(/[-_]/g, ' ').replace(/\b\w/g, function(c) { return c.toUpperCase(); });
      if (ext === '.md') {
        results.push({key: "/" + name, value: title});
      } else if (ext === '.yaml') {
        try {
          var head = readFileHead(joinPath(docroot, f), 500);
          if (/^main:/m.test(head)) {
            results.push({key: "/" + name, value: title});
          }
        } catch (e) {}
      }
    }
    print(JSON.stringify(results));
```

Every `.md` file and every `.yaml` file that defines `main:` shows up
automatically in the navbar. Drop in `pricing.md` and a "Pricing" link
appears with no other changes.

To replace this with a hand-written list, just define `navlinks:` (plain
content) in your site — your definition wins because page-level content is
loaded before parent definitions:

```yaml
navlinks:
  "/": Home
  "/about": About
  "/contact": Contact
```

## Execution Details

- **Working directory**: set to the request directory (`requestDir`), so
  relative file paths resolve from there.
- **Timeout**: 30 seconds per invocation. Timeouts produce an HTML comment
  and the name resolves to nothing.
- **Output limits**: stdout is capped at 10 MB to protect the server.
- **Errors**: a script error, non-JSON output, or empty output makes the
  name unresolved (rendered as plain text, per the usual fallback).
- **Cached**: results are not cached separately. They're embedded in the
  rendered page, which is cached just like any other render and
  invalidated when source files change.

## Next Steps

- [Server-Side Scripts](/scripts) — the `^name` format-script counterpart
- [Format Definitions](/formats) — pair a `$name` with a `^name` to render it
- [Built-in Components](/components) — see `$navlinks` and `^navlinks` working together
