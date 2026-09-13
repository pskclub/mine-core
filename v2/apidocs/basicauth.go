package apidocs

import (
	"crypto/subtle"
	"net/http"

	"github.com/labstack/echo/v5"
	core "github.com/pskclub/mine-core/v2"
)

// BasicAuth guards the reference with a username and password the browser asks
// for.
//
// It exists because the alternative — writing an echo middleware — is a lot of
// ceremony for "let the analysts try staging this week", and what people do
// instead is mount the reference unguarded and mean to fix it later.
//
// Set it in code, or leave it empty and set APP_APIDOCS_PASSWORD in the
// deployment: the second needs no code change at all.
//
//	apidocs.Mount(srv, apidocs.Options{
//	    Spec:      spec,
//	    BasicAuth: &apidocs.BasicAuth{User: "sa", Password: os.Getenv("DOCS_PASSWORD")},
//	})
//
// It is deliberately a separate lock from the devtools panel's, with its own
// variables: the people who should be able to read the API reference and the
// people who should be able to read the process's configuration are different
// groups, and one shared password makes them the same group.
type BasicAuth struct {
	// User is the username. Empty means DefaultBasicAuthUser.
	User string
	// Password is required: a BasicAuth with no password guards nothing, and
	// Mount refuses it rather than pretending to be a lock.
	Password string
	// Realm is what the browser shows in its prompt. Empty means
	// DefaultBasicAuthRealm.
	Realm string
}

// Basic-auth defaults.
const (
	DefaultBasicAuthUser  = "apidocs"
	DefaultBasicAuthRealm = "api reference"

	// EnvBasicAuthUser and EnvBasicAuthPassword configure the login without
	// touching code — APP_APIDOCS_USER and APP_APIDOCS_PASSWORD as the
	// deployment writes them, since every APP_-prefixed variable is loaded.
	EnvBasicAuthUser     = "apidocs_user"
	EnvBasicAuthPassword = "apidocs_password"
)

// resolveBasicAuth is the login this mount will use: the one given in code,
// otherwise the one in the environment, otherwise none.
//
// Configuration does not silently override code — a caller that wrote a password
// gets that password — but a caller that wrote nothing gets whatever the
// deployment set.
func resolveBasicAuth(opts Options, env core.IENV) *BasicAuth {
	if opts.BasicAuth != nil && opts.BasicAuth.Password != "" {
		return opts.BasicAuth
	}
	if env == nil {
		return nil
	}
	password := env.String(EnvBasicAuthPassword)
	if password == "" {
		return nil
	}
	return &BasicAuth{User: env.String(EnvBasicAuthUser), Password: password}
}

// middleware refuses every request that does not carry the right credentials.
func (b *BasicAuth) middleware() echo.MiddlewareFunc {
	user := b.User
	if user == "" {
		user = DefaultBasicAuthUser
	}
	realm := b.Realm
	if realm == "" {
		realm = DefaultBasicAuthRealm
	}
	password := b.Password

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			givenUser, givenPassword, ok := c.Request().BasicAuth()
			// Both halves are always compared, and with a constant-time
			// comparison: returning early on a wrong username tells an attacker
			// which half to work on, and byte-by-byte string equality tells them
			// how much of the password they have.
			userOK := subtle.ConstantTimeCompare([]byte(givenUser), []byte(user)) == 1
			passOK := subtle.ConstantTimeCompare([]byte(givenPassword), []byte(password)) == 1
			if ok && userOK && passOK {
				return next(c)
			}

			// The header is what makes the browser show its login box, which is
			// the whole reason this is basic auth and not a token.
			c.Response().Header().Set(echo.HeaderWWWAuthenticate, `Basic realm="`+realm+`", charset="UTF-8"`)
			return core.New(http.StatusUnauthorized, "APIDOCS_UNAUTHORIZED",
				"apidocs: authentication required")
		}
	}
}
