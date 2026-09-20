# Authentication

cobramcp provides authentication hooks at the HTTP transport layer. It does **not**
ship a specific auth engine (no built-in JWT/OIDC/API-key verification) — you either
inject your own middleware or use the built-in static bearer-token check.

Authentication applies to the HTTP-exposed forms only:

| Form | Command / API | Auth applies? |
| --- | --- | --- |
| SSE | `mcp stream` / `NewMCPServer` | Yes |
| REST | `mcp rest` | Yes |
| stdio | `mcp start` | No — no network surface; rely on host/OS process isolation |

## Why the HTTP middleware layer

mcp-go's `SSEContextFunc` / `HTTPContextFunc` only return a `context.Context` — they
have **no error return**, so they cannot reject a request. Hooks (`BeforeAnyHookFunc`
etc.) likewise have no return value. The only place a request can be rejected is an
`http.Handler` middleware wrapped around the transport handler. cobramcp exposes that
as `AuthMiddleware`.

## Three capabilities

### 1. Hook — inject your own middleware

```go
config := &cobramcp.Config{
    AuthMiddleware: func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            token := r.Header.Get("Authorization")
            if !verify(token) { // your logic
                http.Error(w, "unauthorized", http.StatusUnauthorized)
                return
            }
            next.ServeHTTP(w, r)
        })
    },
}
```

Same field exists on `MCPOptions` for the in-process model (`NewMCPServer`).

The type is `func(http.Handler) http.Handler` — identical to the `Middleware` alias in
`transport/http3`, `transport/sse`, etc., so their middlewares are directly reusable.

### 2. Built-in static token

The simplest form: `AuthToken` (or `--auth-token`) checks that the request carries
`Authorization: Bearer <token>`, using `crypto/subtle.ConstantTimeCompare` to avoid
timing attacks.

```go
config := &cobramcp.Config{
    AuthToken: "s3cr3t",
}
```

```bash
my-cli mcp stream --auth-token s3cr3t
```

### 3. OAuth resource metadata (RFC 9728)

Expose `/.well-known/oauth-protected-resource` so MCP clients can discover which
authorization server issues tokens for this resource (aligned with the MCP
2025-06-18 authorization spec).

```go
config := &cobramcp.Config{
    OAuthProtectedResource: &mcpserver.ProtectedResourceMetadataConfig{
        Resource:             "https://mcp.example.com",
        AuthorizationServers: []string{"https://auth.example.com"},
        ScopesSupported:      []string{"mcp:read", "mcp:write"},
    },
}
```

```bash
my-cli mcp stream --oauth-resource https://mcp.example.com \
                  --oauth-auth-server https://auth.example.com
```

The endpoint path is derived by `ProtectedResourceMetadataPath(Resource)`:

- resource without a path (`https://mcp.example.com`) → `/.well-known/oauth-protected-resource`
- resource with a path (`https://mcp.example.com/mcp`) → `/.well-known/oauth-protected-resource/mcp`

## Security note: the well-known path must be public

The OAuth metadata endpoint is a **public discovery endpoint** — clients fetch it
*before* they hold a token. If your auth middleware rejects it with 401, the OAuth
discovery flow deadlocks.

- The built-in `AuthToken` middleware **automatically exempts** the well-known path
  when `OAuthProtectedResource` is configured.
- If you supply your own `AuthMiddleware` **and** configure `OAuthProtectedResource`,
  you must exempt the path yourself:

```go
const wk = "/.well-known/oauth-protected-resource"
AuthMiddleware: func(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == wk { // public discovery endpoint
            next.ServeHTTP(w, r)
            return
        }
        // ... your auth check ...
    })
},
```

## Combining hook + token

If both `AuthMiddleware` and `AuthToken` are set, the user middleware runs **outer**
(first), then the static token check inner. A request must pass both.

## SSE streaming caveat

If your custom middleware wraps the `http.ResponseWriter`, it must forward the
`http.Flusher` interface — SSE relies on `Flush()` for streaming. The built-in
middleware does not wrap the `ResponseWriter` (it only reads headers and passes
through), so it is safe.
