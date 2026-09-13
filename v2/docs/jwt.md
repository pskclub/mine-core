# JWT

Thin helpers over [golang-jwt v5](https://github.com/golang-jwt/jwt) for HS256
sign/verify with typed claims.

## Claims

Embed `jwt.RegisteredClaims` for the standard time-based fields:

```go
import "github.com/golang-jwt/jwt/v5"

type AppClaims struct {
    UserID string `json:"user_id"`
    Role   string `json:"role"`
    jwt.RegisteredClaims
}
```

## Sign

```go
token, err := core.JWTSign(&AppClaims{
    UserID: "u-1",
    Role:   "admin",
    RegisteredClaims: jwt.RegisteredClaims{
        ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
        IssuedAt:  jwt.NewNumericDate(time.Now()),
    },
}, secret)
```

## Verify

Pass a pointer to a claims value; it is populated on success. Verification checks
the HMAC signature **and** the standard time claims (exp/nbf/iat):

```go
var claims AppClaims
if err := core.JWTVerify(token, secret, &claims); err != nil {
    return err // IError, code INVALID_JWT (401)
}
useUserID(claims.UserID)
```

## API

```go
func JWTSign(claims jwt.Claims, secret string) (string, IError)
func JWTVerify(tokenString string, secret string, dest jwt.Claims) IError
```

- Only HMAC signing methods are accepted (mismatched algorithms are rejected).
- A wrong secret, expired token, or tampered payload returns a 401 `INVALID_JWT`.
