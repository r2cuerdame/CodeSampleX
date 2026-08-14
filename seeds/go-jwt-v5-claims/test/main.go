package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"codesamplex.dev/sample/gojwtv5/src"
)

func main() {
	key := []byte("csx-contract-key")

	raw, err := tokens.Sign(key, "ada", "read", time.Hour)
	check(err == nil, "sign: %v", err)

	claims, err := tokens.Verify(key, raw)
	check(err == nil, "verify: %v", err)
	check(claims.Subject == "ada" && claims.Scope == "read",
		"claims: %+v", claims)
	// Time fields are *NumericDate now, not int64. A v4 struct literal
	// does not compile, which is the good kind of breakage.
	check(claims.ExpiresAt.After(time.Now()), "expiry: %v", claims.ExpiresAt)

	// An expired token matches the specific sentinel AND the general one.
	// That pair is what lets a handler answer "refresh" versus "reject".
	expired, err := tokens.Sign(key, "ada", "read", -time.Hour)
	check(err == nil, "sign expired: %v", err)
	_, err = tokens.Verify(key, expired)
	check(errors.Is(err, jwt.ErrTokenExpired), "expected ErrTokenExpired, got %v", err)
	check(errors.Is(err, jwt.ErrTokenInvalidClaims), "expected ErrTokenInvalidClaims, got %v", err)

	// A different key is a signature failure, not a claims failure.
	_, err = tokens.Verify([]byte("other-key"), raw)
	check(errors.Is(err, jwt.ErrTokenSignatureInvalid), "expected signature error, got %v", err)
	check(!errors.Is(err, jwt.ErrTokenExpired), "a bad signature is not an expiry: %v", err)

	// alg=none is refused without opting in to anything.
	none, err := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.RegisteredClaims{Subject: "mallory"}).
		SignedString(jwt.UnsafeAllowNoneSignatureType)
	check(err == nil, "sign none: %v", err)
	_, err = tokens.Verify(key, none)
	check(err != nil, "alg=none must be refused")
	check(errors.Is(err, jwt.ErrTokenUnverifiable), "expected unverifiable, got %v", err)

	// Claim checks are parser options, not something you write by hand.
	_, err = tokens.Verify(key, raw, jwt.WithIssuer("someone-else"))
	check(errors.Is(err, jwt.ErrTokenInvalidIssuer), "expected issuer error, got %v", err)
	_, err = tokens.Verify(key, raw, jwt.WithIssuer("csx"))
	check(err == nil, "matching issuer should pass: %v", err)

	fmt.Println("contract ok")
}

func check(ok bool, format string, args ...any) {
	if !ok {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		os.Exit(1)
	}
}
