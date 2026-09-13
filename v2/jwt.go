package core

import (
	"github.com/golang-jwt/jwt/v5"
)

// JWTSign signs claims with HS256 using secret. claims is any jwt.Claims
// implementation (e.g. jwt.RegisteredClaims or a struct embedding it).
func JWTSign(claims jwt.Claims, secret string) (string, IError) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", Wrap(err, "jwt: sign")
	}
	return signed, nil
}

// JWTVerify parses tokenString, verifies the HMAC signature with secret and the
// standard time-based claims, and decodes into dest (a pointer to a jwt.Claims).
func JWTVerify(tokenString string, secret string, dest jwt.Claims) IError {
	_, err := jwt.ParseWithClaims(tokenString, dest, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, New(401, "INVALID_JWT", "unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil {
		return New(401, "INVALID_JWT", "jwt is invalid").WithCause(err)
	}
	return nil
}
