package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withAuth(t *testing.T) (*App, *Server, *Auth) {
	t.Helper()
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, nil)
	return app, e, NewJWTAuth("s3cret")
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func TestAuth_protectedRoute_requiresToken(t *testing.T) {
	_, e, auth := withAuth(t)
	e.GET("/me", func(c IHTTPContext) error {
		return c.JSON(http.StatusOK, c.GetUser())
	}, auth.Middleware())

	// no token → 401
	rec := doWithHeaders(e, http.MethodGet, "/me", "", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// invalid token → 401
	rec = doWithHeaders(e, http.MethodGet, "/me", "", bearer("garbage"))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuth_validToken_setsUser(t *testing.T) {
	_, e, auth := withAuth(t)

	var gotUser *ContextUser
	e.GET("/me", func(c IHTTPContext) error {
		gotUser = c.GetUser()
		return c.JSON(http.StatusOK, "ok")
	}, auth.Middleware())

	token, err := auth.SignToken(jwt.MapClaims{
		"sub": "u-1", "email": "a@b.com", "role": "admin",
	}, time.Hour)
	require.NoError(t, err)

	rec := doWithHeaders(e, http.MethodGet, "/me", "", bearer(token))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, gotUser)
	assert.Equal(t, "u-1", gotUser.ID)
	assert.Equal(t, "a@b.com", gotUser.Email)
	assert.Equal(t, "admin", gotUser.Segment)
	assert.Equal(t, token, gotUser.Token)
}

func TestAuth_expiredToken_rejected(t *testing.T) {
	_, e, auth := withAuth(t)
	e.GET("/me", func(c IHTTPContext) error { return c.JSON(http.StatusOK, "ok") }, auth.Middleware())

	// exp in the past → rejected by JWTVerify
	token, _ := auth.SignToken(jwt.MapClaims{"sub": "u-1", "exp": time.Now().Add(-time.Hour).Unix()}, 0)
	rec := doWithHeaders(e, http.MethodGet, "/me", "", bearer(token))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuth_optional_allowsAnonymous(t *testing.T) {
	_, e, auth := withAuth(t)
	var hadUser bool
	e.GET("/feed", func(c IHTTPContext) error {
		hadUser = c.GetUser() != nil
		return c.JSON(http.StatusOK, "ok")
	}, auth.Optional())

	rec := doWithHeaders(e, http.MethodGet, "/feed", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, hadUser, "anonymous request allowed, user nil")
}

func TestAuth_requireRole(t *testing.T) {
	_, e, auth := withAuth(t)
	e.GET("/admin", func(c IHTTPContext) error { return c.JSON(http.StatusOK, "ok") },
		auth.Middleware(), auth.RequireRole("admin"))

	adminTok, _ := auth.SignToken(jwt.MapClaims{"sub": "a", "role": "admin"}, time.Hour)
	userTok, _ := auth.SignToken(jwt.MapClaims{"sub": "u", "role": "user"}, time.Hour)

	assert.Equal(t, http.StatusOK,
		doWithHeaders(e, http.MethodGet, "/admin", "", bearer(adminTok)).Code)
	assert.Equal(t, http.StatusForbidden,
		doWithHeaders(e, http.MethodGet, "/admin", "", bearer(userTok)).Code)
}

func TestAuth_customResolver(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, nil)
	auth := NewJWTAuth("k", WithUserResolver(func(c jwt.MapClaims) (*ContextUser, error) {
		return &ContextUser{ID: "custom-" + c["sub"].(string), Segment: "vip"}, nil
	}))

	var got *ContextUser
	e.GET("/me", func(c IHTTPContext) error { got = c.GetUser(); return c.JSON(http.StatusOK, "ok") },
		auth.Middleware())

	tok, _ := auth.SignToken(jwt.MapClaims{"sub": "42"}, time.Hour)
	doWithHeaders(e, http.MethodGet, "/me", "", bearer(tok))
	require.NotNil(t, got)
	assert.Equal(t, "custom-42", got.ID)
	assert.Equal(t, "vip", got.Segment)
}

// --- opaque / hashed tokens ---

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestAuth_hashedOpaqueToken(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, nil)

	// server stores only the sha256 hash of issued tokens
	store := map[string]*ContextUser{
		sha256hex("secret-api-key"): {ID: "u-7", Segment: "service"},
	}
	auth := NewAuth(HashedTokenVerifier(func(_ context.Context, hash string) (*ContextUser, error) {
		if u, ok := store[hash]; ok {
			return u, nil
		}
		return nil, errUnauthorized("unknown token")
	}))

	var got *ContextUser
	e.GET("/api/ping", func(c IHTTPContext) error {
		got = c.GetUser()
		return c.JSON(http.StatusOK, "ok")
	}, auth.Middleware())

	// valid opaque token → 200 + user resolved by hash lookup
	rec := doWithHeaders(e, http.MethodGet, "/api/ping", "", bearer("secret-api-key"))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, got)
	assert.Equal(t, "u-7", got.ID)

	// unknown token → 401
	rec = doWithHeaders(e, http.MethodGet, "/api/ping", "", bearer("wrong-key"))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuth_chainJWTAndOpaque(t *testing.T) {
	app := newHTTPTestApp(t)
	e := NewHTTPServer(app, nil)

	apiKeys := map[string]*ContextUser{sha256hex("key-123"): {ID: "svc"}}
	auth := NewAuth(ChainVerifiers(
		JWTVerifier("s3cret"),
		HashedTokenVerifier(func(_ context.Context, hash string) (*ContextUser, error) {
			if u, ok := apiKeys[hash]; ok {
				return u, nil
			}
			return nil, errUnauthorized("no")
		}),
	))

	var got *ContextUser
	e.GET("/x", func(c IHTTPContext) error { got = c.GetUser(); return c.JSON(http.StatusOK, "ok") },
		auth.Middleware())

	// a JWT works
	jwtTok, _ := SignJWT("s3cret", jwt.MapClaims{"sub": "jwt-user"}, time.Hour)
	require.Equal(t, http.StatusOK, doWithHeaders(e, http.MethodGet, "/x", "", bearer(jwtTok)).Code)
	assert.Equal(t, "jwt-user", got.ID)

	// an opaque API key also works on the same route
	require.Equal(t, http.StatusOK, doWithHeaders(e, http.MethodGet, "/x", "", bearer("key-123")).Code)
	assert.Equal(t, "svc", got.ID)

	// neither → 401
	assert.Equal(t, http.StatusUnauthorized, doWithHeaders(e, http.MethodGet, "/x", "", bearer("nope")).Code)
}
