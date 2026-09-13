package main

import (
	"context"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/valid"
)

// --- Example 6: bearer tokens, roles, and issuing one -----------------------
//
// core.Auth is one middleware over a pluggable TokenVerifier. What changes
// between a JWT service and an API-key service is the verifier; the routes, the
// middleware and c.GetUser() are identical.

// ErrInvalidCredentials is deliberately one error for both "no such account"
// and "wrong password". Splitting them lets anybody enumerate which addresses
// are registered, and the caller can do nothing useful with the difference.
var ErrInvalidCredentials = core.New(http.StatusUnauthorized, "INVALID_CREDENTIALS",
	"email or password is incorrect")

const accessTokenTTL = 12 * time.Hour

// newJWTAuth builds the authenticator, or reports that it cannot.
//
// A missing secret is not something to paper over with a default: a hardcoded
// fallback signs tokens every copy of this source can also mint. The caller
// logs and leaves the protected routes unmounted, which fails visibly at boot
// instead of invisibly in production.
func newJWTAuth(env core.IENV) (*core.Auth, bool) {
	secret := env.Config().JWTSecret
	if secret == "" {
		return nil, false
	}

	return core.NewJWTAuth(secret, core.WithUserResolver(resolveUser)), true
}

// resolveUser maps verified claims to the user the request will carry. The
// default resolver already reads sub/email/username/name/role; override it when
// the tokens are somebody else's shape, or — as here — when a claim the service
// depends on must be present rather than empty.
func resolveUser(claims jwt.MapClaims) (*core.ContextUser, error) {
	id, _ := claims["sub"].(string)
	if id == "" {
		return nil, core.New(http.StatusUnauthorized, "INVALID_TOKEN", "the token has no subject")
	}

	role, _ := claims["role"].(string)

	return &core.ContextUser{ID: id, Segment: role}, nil
}

// newOpaqueAuth is the same middleware with a different verifier: random tokens
// looked up in a table. Only the sha256 of the token is stored, so a leaked
// database contains nothing anyone can present.
//
// ChainVerifiers accepts both shapes on the same routes, which is how a service
// adds API keys without reissuing anybody's session.
func newOpaqueAuth(secret string, lookup func(ctx context.Context, tokenHash string) (*core.ContextUser, error)) *core.Auth {
	return core.NewAuth(core.ChainVerifiers(
		core.JWTVerifier(secret, resolveUser),
		core.HashedTokenVerifier(lookup),
	))
}

type loginRequest struct {
	Email    *string `json:"email" form:"email"`
	Password *string `json:"password" form:"password"`
}

func (r *loginRequest) Valid(ctx core.IContext) core.IError {
	v := valid.New(ctx)
	v.Str("email", r.Email).Trim().Lower().Required().Email()
	v.Str("password", r.Password).Required()

	return v.Error()
}

func mountAuth(e *core.Server, a *core.Auth) {
	e.POST("/auth/login", login(a))

	// Middleware() refuses anything without a valid token: 401 before the
	// handler is entered, so a handler behind it can rely on c.GetUser().
	e.GET("/me", me, a.Middleware())

	// Optional() validates a token when one is present and lets the rest
	// through — for a page that shows more to a signed-in reader. c.GetUser()
	// is nil for the others, and an invalid token is treated as none.
	e.GET("/feed", feed, a.Optional())

	// RequireRole reads the user the previous middleware resolved, so it must
	// come after it. On its own it answers 401, not 403, because there is no
	// user to have insufficient permissions.
	e.DELETE("/admin/articles/:id", deleteArticle, a.Middleware(), a.RequireRole("admin"))
}

// login issues the token. It is a closure over the Auth rather than a plain
// handler because signing needs the same secret verification does — a second
// copy of that string in a second place is how the two drift apart.
func login(a *core.Auth) core.HandlerFunc {
	return func(c core.IHTTPContext) error {
		req := &loginRequest{}
		if err := c.BindWithValidate(req); err != nil {
			return err
		}

		user, aErr := authenticate(c, deref(req.Email), deref(req.Password))
		if aErr != nil {
			return aErr
		}

		// Only what the service needs to authorise the next request. A token is
		// readable by whoever holds it and cannot be revoked before it expires,
		// so anything private or fast-changing does not belong in the claims.
		token, sErr := a.SignToken(jwt.MapClaims{
			"sub":  user.ID,
			"role": user.Segment,
		}, accessTokenTTL)
		if sErr != nil {
			return sErr
		}

		return c.JSON(http.StatusOK, map[string]any{
			"access_token": token,
			"token_type":   "Bearer",
			"expires_in":   int(accessTokenTTL.Seconds()),
		})
	}
}

// authenticate stands in for the user store. The constant-time comparison and
// the password hash are the real implementation's problem (utils.HashPassword /
// utils.ComparePassword); what matters here is that both failures answer with
// the same error.
func authenticate(_ core.IContext, email, password string) (*core.ContextUser, core.IError) {
	if email != "admin@example.com" || password != "example-password" {
		return nil, ErrInvalidCredentials
	}

	return &core.ContextUser{ID: "u_1", Email: email, Segment: "admin"}, nil
}

func me(c core.IHTTPContext) error {
	// WithHTTPContext copies whatever the middleware resolved onto the
	// IContext, so every layer below — services, repositories, jobs started
	// from here — reads the user the same way.
	user := c.GetUser()

	// Not `return c.JSON(200, user)`: ContextUser also carries the raw token,
	// and a struct that grows a field grows it in every response that returns
	// the struct. A response type is the compiler saying no.
	return c.JSON(http.StatusOK, map[string]any{
		"id":    user.ID,
		"email": user.Email,
		"role":  user.Segment,
	})
}

func feed(c core.IHTTPContext) error {
	user := c.GetUser()
	if user == nil {
		return c.JSON(http.StatusOK, map[string]any{"personalised": false})
	}

	return c.JSON(http.StatusOK, map[string]any{"personalised": true, "user_id": user.ID})
}

func deleteArticle(c core.IHTTPContext) error {
	c.Log().Info("article deleted", "article_id", c.Param("id"), "by", c.GetUser().ID)

	return c.NoContent(http.StatusNoContent)
}
