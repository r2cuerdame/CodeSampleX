package main

import (
	"fmt"
	"os"
	"reflect"

	"codesamplex.dev/sample/govalidator/src"
)

func main() {
	v := signup.New()

	eq(signup.Failures(v, signup.Value{Email: "a@example.com", Age: 30, Accepted: true}),
		nil, "a complete value should pass")

	// The trap: both of these were supplied, and both are rejected,
	// because required means "not the zero value".
	eq(signup.Failures(v, signup.Value{Email: "a@example.com", Age: 0, Accepted: true}),
		[]string{"Age:required"}, "age 0 is reported as missing")
	eq(signup.Failures(v, signup.Value{Email: "a@example.com", Age: 30, Accepted: false}),
		[]string{"Accepted:required"}, "false is reported as missing")

	// A real range violation reports the range tag, so the two cases are
	// distinguishable in the response you build for the caller.
	eq(signup.Failures(v, signup.Value{Email: "a@example.com", Age: 17, Accepted: true}),
		[]string{"Age:gte"}, "17 fails gte, not required")

	// With pointers, required means present, which is the meaning most
	// forms actually want.
	f, z := false, 0
	eq(signup.Failures(v, signup.Pointer{}),
		[]string{"Accepted:required", "Count:required"}, "nil pointers are missing")
	eq(signup.Failures(v, signup.Pointer{Accepted: &f, Count: &z}),
		nil, "pointers to false and 0 are present")

	// omitempty skips a nil pointer but not a present zero value.
	eq(signup.Failures(v, signup.Pointer{Accepted: &f, Count: &z, Optional: &z}),
		[]string{"Optional:gte"}, "a present 0 still has to satisfy gte")

	// The same rule applies to Var, so it is the tag and not the struct.
	if v.Var(0, "required") == nil {
		fail("Var(0, required) should fail")
	}

	fmt.Println("contract ok")
}

func eq(got, want []string, what string) {
	if !reflect.DeepEqual(got, want) {
		fail("%s: got %v, want %v", what, got, want)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
