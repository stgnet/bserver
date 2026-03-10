# Proxy Mode

bserver can act as a reverse proxy for any virtual host. Instead of serving
files from disk, all requests to the vhost are forwarded to a backend HTTP
server and the responses are relayed back to the client.

## Setup

Create a vhost directory containing a single `index.yaml` with an `http:` key
pointing to the backend address:

```
www/example.com/index.yaml
```

```yaml
http: '192.168.1.2:8080'
```

That's it. All requests to `example.com` will be proxied to
`http://192.168.1.2:8080`.

## How It Works

When a request arrives, bserver checks the vhost's `index.yaml` before doing
any file serving. If the file contains an `http:` key, a reverse proxy is
created for that backend and the request is forwarded.

- The proxy is created once and cached. Subsequent requests reuse it.
- If you edit `index.yaml`, the change is picked up automatically on the
  next request (mtime-based cache invalidation).
- If the backend is unreachable, the client receives a `502 Bad Gateway`
  response and the error is logged.

## Backend Address Format

The `http:` value can be specified with or without a scheme:

| Value | Proxies to |
|-------|-----------|
| `192.168.1.2:8080` | `http://192.168.1.2:8080` |
| `http://192.168.1.2:8080` | `http://192.168.1.2:8080` |
| `http://localhost:3000` | `http://localhost:3000` |
| `http://10.0.0.5:9090/app` | `http://10.0.0.5:9090/app` |

If no scheme is provided, `http://` is assumed.

## Examples

### Proxy to a local application

Run a Node.js app on port 3000 and expose it as `myapp.example.com`:

```
mkdir -p www/myapp.example.com
```

```yaml
# www/myapp.example.com/index.yaml
http: 'localhost:3000'
```

### Proxy to a backend server on the network

Forward `internal.example.com` to a machine on the LAN:

```
mkdir -p www/internal.example.com
```

```yaml
# www/internal.example.com/index.yaml
http: '192.168.1.50:8080'
```

### Proxy with a path prefix

Forward to a backend that serves from a subpath:

```yaml
# www/api.example.com/index.yaml
http: 'http://10.0.0.5:9090/v2'
```

## What Gets Proxied

When a vhost is in proxy mode, **all** requests to that domain are forwarded
to the backend. This includes:

- All URL paths
- Query strings
- Request headers
- Request bodies (POST, PUT, etc.)

The following bserver features are still applied to proxied requests:

- **Access logging** — proxied requests appear in the log like any other
- **Security headers** — `X-Content-Type-Options`, `X-Frame-Options`, and
  `Referrer-Policy` are added to responses
- **Rate limiting** — error responses from the backend count toward the
  rate limiter
- **TLS termination** — HTTPS is handled by bserver; the backend connection
  uses plain HTTP

## Switching Between Proxy and Normal Mode

To switch a vhost from proxy mode to normal file serving, simply remove or
rename the `http:` key in `index.yaml` (or replace it with a `main:`
definition). The change takes effect on the next request.

To switch from normal mode to proxy mode, add an `http:` key to `index.yaml`.
Any other keys in the file are ignored when `http:` is present.

## Logging

When a proxy vhost is first detected (or its configuration changes), bserver
logs the mapping:

```
Proxy vhost /path/to/www/example.com -> http://192.168.1.2:8080
```

Backend errors are logged with the domain and target:

```
proxy error for example.com -> http://192.168.1.2:8080: dial tcp 192.168.1.2:8080: connect: connection refused
```
