package utils

import (
	"crypto/md5"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"

	"golang.org/x/crypto/bcrypt"
)

// General-purpose hashing/encoding helpers. (v1's crypto.go additionally carried
// DID/ECDSA/RSA/keypair/sign-verify code — that is domain-specific and lives with
// the consuming service, not the framework.)

// SHA256 returns the hex-encoded SHA-256 of s (v1's NewSha256).
func SHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// SHA384 returns the hex-encoded SHA-384 of s.
func SHA384(s string) string {
	sum := sha512.Sum384([]byte(s))
	return hex.EncodeToString(sum[:])
}

// SHA512 returns the hex-encoded SHA-512 of s.
func SHA512(s string) string {
	sum := sha512.Sum512([]byte(s))
	return hex.EncodeToString(sum[:])
}

// MD5 returns the hex-encoded MD5 of s (v1's GetMD5Hash). For checksums/etags/
// cache keys only — not for security.
func MD5(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec // non-security checksum
	return hex.EncodeToString(sum[:])
}

// Base64Encode standard-encodes s.
func Base64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// Base64Decode standard-decodes s.
func Base64Decode(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	return string(b), err
}

// HexEncode returns the hex encoding of b.
func HexEncode(b []byte) string { return hex.EncodeToString(b) }

// HexDecode decodes a hex string.
func HexDecode(s string) ([]byte, error) { return hex.DecodeString(s) }

// HashPassword returns a bcrypt hash of password (v1's HashPassword, but returns
// a string rather than *string).
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ComparePassword reports whether password matches a bcrypt hash.
func ComparePassword(hashed string, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hashed), []byte(password)) == nil
}
