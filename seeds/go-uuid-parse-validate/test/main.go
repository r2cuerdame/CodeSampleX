package main

import (
	"fmt"
	"os"

	"github.com/google/uuid"

	sample "codesamplex.dev/sample/gouuid/src"
)

func fail(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
	os.Exit(1)
}

func main() {
	id := sample.New()
	if id.Version() != 4 {
		fail("version = %d, want 4", id.Version())
	}
	if id.Variant() != uuid.RFC4122 {
		fail("variant = %v, want RFC4122", id.Variant())
	}
	if sample.IsNil(id) {
		fail("a generated id must not be Nil")
	}

	canonical := id.String()
	for _, form := range []string{canonical, "urn:uuid:" + canonical, "{" + canonical + "}"} {
		got, err := sample.Parse(form)
		if err != nil || got != id {
			fail("parse %q: %v err=%v", form, got, err)
		}
	}

	if _, err := sample.Parse("not-a-uuid"); err == nil {
		fail("malformed input must return an error")
	}
	if !sample.IsNil(uuid.Nil) {
		fail("uuid.Nil must be Nil")
	}
	fmt.Println("CONTRACT PASS: google/uuid generated, parsed every form and rejected junk")
}
