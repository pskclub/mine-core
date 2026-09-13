package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v5"
)

// echo-context keys under which the auth middleware stores the resolved user and
// raw token; WithHTTPContext copies the user onto the IContext.
const (
	userContextKey  = "core.user"
	tokenContextKey = "core.token"
)

// TokenVerifier turns a raw bearer token into a ContextUser, or an error when the
// token is invalid. ctx is the request context (for DB/cache lookups). This is
// the strategy that lets Auth support JWTs, opaque/hashed tokens, or both.
type TokenVerifier func(ctx context.Context, token string) (*ContextUser, error)

// UserResolver maps verified JWT claims to a ContextUser.
type UserResolver func(claims jwt.MapClaims) (*ContextUser, error)

// Auth authenticates bearer tokens with a TokenVerifier and exposes echo middleware.
type Auth struct {
	verify    TokenVerifier
	lookup    func(ec *echo.Context) string
	jwtSecret string // set by NewJWTAuth; enables SignToken
}

type authConfig struct {
	lookup   func(ec *echo.Context) string
	resolver UserResolver
}

// AuthOption configures an Auth.
type AuthOption func(*authConfig)

// WithUserResolver overrides how JWT claims become a ContextUser (JWT auth only).
func WithUserResolver(r UserResolver) AuthOption {
	return func(c *authConfig) { c.resolver = r }
}

// WithTokenLookup overrides how the token is extracted from the request
// (default: the "Authorization: Bearer <token>" header).
func WithTokenLookup(fn func(ec *echo.Context) string) AuthOption {
	return func(c *authConfig) { c.lookup = fn }
}

// NewAuth builds an Auth from a custom TokenVerifier — use it for opaque tokens
// (API keys, sha256/random tokens looked up in a store) or a chain of strategies.
func NewAuth(verify TokenVerifier, opts ...AuthOption) *Auth {
	cfg := &authConfig{lookup: bearerToken}
	for _, o := range opts {
		o(cfg)
	}
	return &Auth{verify: verify, lookup: cfg.lookup}
}

// NewJWTAuth is the convenience for HS256 JWT bearer auth.
func NewJWTAuth(secret string, opts ...AuthOption) *Auth {
	cfg := &authConfig{lookup: bearerToken, resolver: defaultUserResolver}
	for _, o := range opts {
		o(cfg)
	}
	return &Auth{verify: JWTVerifier(secret, cfg.resolver), lookup: cfg.lookup, jwtSecret: secret}
}

// --- verifier strategies ---

// JWTVerifier verifies an HS256 JWT and maps its claims via resolver (the default
// resolver is used when none is given).
func JWTVerifier(secret string, resolver ...UserResolver) TokenVerifier {
	r := defaultUserResolver
	if len(resolver) > 0 && resolver[0] != nil {
		r = resolver[0]
	}
	return func(_ context.Context, token string) (*ContextUser, error) {
		claims := jwt.MapClaims{}
		if err := JWTVerify(token, secret, &claims); err != nil {
			return nil, err // 401 INVALID_JWT
		}
		return r(claims)
	}
}

// HashedTokenVerifier builds a verifier for opaque tokens: it SHA-256-hashes the
// incoming token and hands the hex digest to lookup, which finds the user (e.g.
// by querying a table that stores token hashes). Storing only the hash means a
// leaked database never exposes usable tokens.
//
//	auth := core.NewAuth(core.HashedTokenVerifier(func(ctx context.Context, hash string) (*core.ContextUser, error) {
//	    var t AccessToken
//	    if err := db.WithContext(ctx).Where("token_hash = ?", hash).First(&t).Error; err != nil {
//	        return nil, err
//	    }
//	    return &core.ContextUser{ID: t.UserID, Segment: t.Role}, nil
//	}))
func HashedTokenVerifier(lookup func(ctx context.Context, tokenHash string) (*ContextUser, error)) TokenVerifier {
	return func(ctx context.Context, token string) (*ContextUser, error) {
		sum := sha256.Sum256([]byte(token))
		return lookup(ctx, hex.EncodeToString(sum[:]))
	}
}

// ChainVerifiers tries each verifier in order and returns the first user it
// resolves — handy to accept both JWTs and opaque API keys on the same routes.
func ChainVerifiers(verifiers ...TokenVerifier) TokenVerifier {
	return func(ctx context.Context, token string) (*ContextUser, error) {
		var lastErr error
		for _, v := range verifiers {
			user, err := v(ctx, token)
			if err == nil && user != nil {
				return user, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = errUnauthorized("invalid token")
		}
		return nil, lastErr
	}
}

// --- token issuing (JWT) ---

// SignJWT issues an HS256 token for claims valid for ttl (iat/exp set
// automatically; ttl<=0 means no expiry).
func SignJWT(secret string, claims jwt.MapClaims, ttl time.Duration) (string, IError) {
	if claims == nil {
		claims = jwt.MapClaims{}
	}
	now := time.Now()
	claims["iat"] = now.Unix()
	if ttl > 0 {
		claims["exp"] = now.Add(ttl).Unix()
	}
	return JWTSign(claims, secret)
}

// SignToken issues a JWT with this Auth's secret. Requires an Auth built with
// NewJWTAuth.
func (a *Auth) SignToken(claims jwt.MapClaims, ttl time.Duration) (string, IError) {
	if a.jwtSecret == "" {
		return "", New(500, "CONFIG_ERROR", "auth: SignToken requires a JWT auth (use NewJWTAuth)")
	}
	return SignJWT(a.jwtSecret, claims, ttl)
}

// --- middleware ---

// Middleware requires a valid bearer token; otherwise it returns 401. On success
// the user is available in handlers via c.GetUser().
func (a *Auth) Middleware() echo.MiddlewareFunc { return a.middleware(true) }

// Optional validates the token when present but allows anonymous requests.
func (a *Auth) Optional() echo.MiddlewareFunc { return a.middleware(false) }

func (a *Auth) middleware(required bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ec *echo.Context) error {
			token := a.lookup(ec)
			if token == "" {
				if required {
					return errUnauthorized("missing bearer token")
				}
				return next(ec)
			}

			user, err := a.verify(ec.Request().Context(), token)
			if err != nil || user == nil {
				if required {
					return authError(err)
				}
				return next(ec)
			}

			user.Token = token
			ec.Set(userContextKey, user)
			ec.Set(tokenContextKey, token)
			return next(ec)
		}
	}
}

// ContextUserOf returns the principal an authentication middleware attached to
// the request, or nil when the request is anonymous.
//
// It is the read side of what this package's own Auth middleware writes, and
// what WithHTTPContext copies onto IContext — exported for middleware that runs
// *before* the framework context exists and so cannot call c.GetUser(). A
// service whose authentication keeps the user under a key of its own is not
// visible here; that is the same service that never reads it back through
// c.GetUser() either.
func ContextUserOf(c *echo.Context) *ContextUser {
	if c == nil {
		return nil
	}
	user, _ := c.Get(userContextKey).(*ContextUser)
	return user
}

// RequireRole allows the request only when the authenticated user's Segment
// (role) is one of roles. Chain it after Middleware.
func (a *Auth) RequireRole(roles ...string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ec *echo.Context) error {
			user, ok := ec.Get(userContextKey).(*ContextUser)
			if !ok || user == nil {
				return errUnauthorized("authentication required")
			}
			for _, r := range roles {
				if user.Segment == r {
					return next(ec)
				}
			}
			return New(http.StatusForbidden, "FORBIDDEN", "insufficient permissions")
		}
	}
}

// --- helpers ---

func bearerToken(ec *echo.Context) string {
	h := ec.Request().Header.Get(echo.HeaderAuthorization)
	const prefix = "Bearer "
	if strings.HasPrefix(h, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(h, prefix))
	}
	return ""
}

func defaultUserResolver(claims jwt.MapClaims) (*ContextUser, error) {
	u := &ContextUser{Data: map[string]string{}}
	str := func(key string) string {
		if v, ok := claims[key].(string); ok {
			return v
		}
		return ""
	}
	u.ID = str("sub")
	if u.ID == "" {
		u.ID = str("id")
	}
	u.Email = str("email")
	u.Username = str("username")
	u.Name = str("name")
	u.Segment = str("role")
	return u, nil
}

// authError preserves an IError (e.g. JWT's 401) or falls back to a generic 401.
func authError(err error) IError {
	if ie, ok := err.(IError); ok {
		return ie
	}
	return errUnauthorized("invalid or expired token")
}

func errUnauthorized(msg string) IError {
	return New(http.StatusUnauthorized, "UNAUTHORIZED", msg)
}
