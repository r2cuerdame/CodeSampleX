// Package tokens signs and verifies with golang-jwt v5.
//
// The v4 -> v5 change that costs the most time is error handling. v4
// returned a *ValidationError carrying a bitmask you inspected with
// err.Errors&jwt.ValidationErrorExpired. v5 returns wrapped sentinels, so
// errors.Is is the whole API — and an expired token matches BOTH
// ErrTokenExpired and the broader ErrTokenInvalidClaims, which is what
// lets a handler distinguish "log in again" from "this token is junk".
//
// The other one is quieter: RegisteredClaims replaced StandardClaims, and
// its time fields are *jwt.NumericDate rather than int64, so a struct
// literal copied from a v4 answer does not compile.
package tokens

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the shape every v5 example embeds: your fields plus the
// registered set.
type Claims struct {
	Scope string `json:"scope"`
	jwt.RegisteredClaims
}

func Sign(key []byte, subject, scope string, ttl time.Duration) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
		Scope: scope,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
			Issuer:    "csx",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
		},
	}).SignedString(key)
}

// Verify returns the claims, or the wrapped sentinel error describing why
// the token was rejected.
func Verify(key []byte, raw string, opts ...jwt.ParserOption) (*Claims, error) {
	var claims Claims
	keyFunc := func(*jwt.Token) (any, error) { return key, nil }
	if _, err := jwt.ParseWithClaims(raw, &claims, keyFunc, opts...); err != nil {
		return nil, err
	}
	return &claims, nil
}
