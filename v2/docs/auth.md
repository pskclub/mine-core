# Authentication

`core.Auth` — bearer-token authentication with echo middleware. A token is
verified by a **`TokenVerifier`** strategy, mapped to a `ContextUser`, and
attached so handlers read it via `c.GetUser()`. Built-in strategies cover **JWT**
and **opaque/hashed tokens** (API keys, sha256 tokens looked up in a store), and
they can be **chained**.

## Setup

```go
// JWT (HS256)
auth := core.NewJWTAuth(env.Config().JWTSecret)

// opaque token (see "Opaque tokens" below)
auth := core.NewAuth(core.HashedTokenVerifier(lookup))

// accept either
auth := core.NewAuth(core.ChainVerifiers(
    core.JWTVerifier(secret),
    core.HashedTokenVerifier(lookup),
))
```

## Protecting routes

Add `auth.Middleware()` per route or per group. A missing/invalid/expired token
returns `401 UNAUTHORIZED` (rendered as the standard error body):

```go
e := core.NewHTTPServer(app, nil)

// per route
e.GET("/me", Me, auth.Middleware())

// per group — everything under /api requires auth
api := e.Group("/api", auth.Middleware())
api.GET("/projects", ListProjects)
```

In a protected handler the user is available:

```go
func Me(c core.IHTTPContext) error {
    user := c.GetUser()      // *core.ContextUser (never nil in a protected route)
    return c.JSON(200, user) // user.ID, user.Email, user.Segment (role), user.Token, ...
}
```

## Optional auth

Validate the token when present, but allow anonymous requests (`c.GetUser()` is
`nil` for those):

```go
e.GET("/feed", Feed, auth.Optional())
```

## Role guard

Chain `RequireRole` after `Middleware`; it checks `ContextUser.Segment`:

```go
admin := e.Group("/admin", auth.Middleware(), auth.RequireRole("admin"))
admin.DELETE("/users/:id", DeleteUser) // 403 FORBIDDEN for non-admins
```

## Opaque tokens (sha256 / API keys)

For non-JWT tokens — a random/opaque string the client sends — verify by looking
it up in a store. Store only the **sha256 hash** so a leaked database exposes no
usable tokens; `HashedTokenVerifier` hashes the incoming token and hands you the
hex digest:

```go
auth := core.NewAuth(core.HashedTokenVerifier(
    func(ctx context.Context, tokenHash string) (*core.ContextUser, error) {
        var t models.AccessToken
        if err := db.WithContext(ctx).Where("token_hash = ?", tokenHash).First(&t).Error; err != nil {
            return nil, err // → 401
        }
        return &core.ContextUser{ID: t.UserID, Segment: t.Role}, nil
    },
))
```

Issuing such a token (at creation time):

```go
raw := utils.NewUUID()                 // give this to the client (shown once)
hash := utils.SHA256(raw)              // store only this
db.Create(&models.AccessToken{UserID: u.ID, TokenHash: hash})
return c.JSON(200, map[string]string{"token": raw})
```

## Accept JWT *and* opaque tokens

```go
auth := core.NewAuth(core.ChainVerifiers(
    core.JWTVerifier(secret),                 // tries JWT first
    core.HashedTokenVerifier(lookupAPIKey),   // then opaque API key
))
```

The first strategy that resolves a user wins; if none do → 401.

## Issuing JWTs (login)

```go
func Login(c core.IHTTPContext) error {
    // ... verify credentials ...
    token, err := auth.SignToken(jwt.MapClaims{ // requires NewJWTAuth
        "sub":   user.ID,
        "email": user.Email,
        "role":  user.Role,
    }, 24*time.Hour) // ttl; iat/exp are set automatically (ttl<=0 → no expiry)
    if err != nil {
        return err
    }
    return c.JSON(200, map[string]string{"token": token})
}
```

`core.SignJWT(secret, claims, ttl)` is also available as a standalone helper.

## Claim mapping

By default these claim names populate the `ContextUser`:

| Claim | ContextUser field |
|---|---|
| `sub` (or `id`) | `ID` |
| `email` | `Email` |
| `username` | `Username` |
| `name` | `Name` |
| `role` | `Segment` |

The raw token is stored in `user.Token`. Override the whole mapping with
`WithUserResolver`:

```go
auth := core.NewJWTAuth(secret, core.WithUserResolver(func(claims jwt.MapClaims) (*core.ContextUser, error) {
    return &core.ContextUser{
        ID:      claims["sub"].(string),
        Segment: claims["tier"].(string),
        Data:    map[string]string{"org": claims["org"].(string)},
    }, nil
}))
```

Custom token extraction (e.g. a cookie or a different header) via
`WithTokenLookup(func(ec *echo.Context) string { ... })`.

## API

```go
// constructors
func NewJWTAuth(secret string, opts ...AuthOption) *Auth
func NewAuth(verify TokenVerifier, opts ...AuthOption) *Auth

// verifier strategies
type TokenVerifier func(ctx context.Context, token string) (*ContextUser, error)
func JWTVerifier(secret string, resolver ...UserResolver) TokenVerifier
func HashedTokenVerifier(lookup func(ctx context.Context, tokenHash string) (*ContextUser, error)) TokenVerifier
func ChainVerifiers(verifiers ...TokenVerifier) TokenVerifier

// options
func WithUserResolver(r UserResolver) AuthOption            // JWT claim mapping
func WithTokenLookup(fn func(ec *echo.Context) string) AuthOption

// tokens
func SignJWT(secret string, claims jwt.MapClaims, ttl time.Duration) (string, IError)
func (a *Auth) SignToken(claims jwt.MapClaims, ttl time.Duration) (string, IError) // NewJWTAuth only

// middleware
func (a *Auth) Middleware() echo.MiddlewareFunc      // required
func (a *Auth) Optional() echo.MiddlewareFunc        // anonymous allowed
func (a *Auth) RequireRole(roles ...string) echo.MiddlewareFunc
```

For lower-level token work (custom claim structs, non-HTTP verification) use the
[JWT helpers](./jwt.md) directly.
