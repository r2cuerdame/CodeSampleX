package serverstore

// A green run that skipped every PostgreSQL test looks exactly like a green
// run that proved something, which is how the /wanted complexity guard sat in
// CI for a week without ever executing. These cover the one decision that
// separates the two: whether an absent CSX_TEST_DSN is allowed to skip.

import (
	"errors"
	"strings"
	"testing"
)

func TestPostgresDSNIsUsedWhenConfigured(t *testing.T) {
	dsn, err := integrationDSN("postgres://csx:csx@localhost:5432/csx", "")
	if err != nil {
		t.Fatalf("configured DSN rejected: %v", err)
	}
	if dsn != "postgres://csx:csx@localhost:5432/csx" {
		t.Fatalf("integrationDSN returned %q", dsn)
	}
}

func TestPostgresDSNSkipsWhenUnset(t *testing.T) {
	_, err := integrationDSN("", "")
	if !errors.Is(err, errIntegrationDSNUnset) {
		t.Fatalf("a laptop without PostgreSQL must skip, got %v", err)
	}
}

// The whole point of the CI flag: with it set, a missing DSN is a failure the
// run cannot pass through, not a skip nobody reads.
func TestPostgresDSNRefusesToSkipWhenRequired(t *testing.T) {
	_, err := integrationDSN("", "1")
	if err == nil {
		t.Fatal("a required DSN that is absent produced no error")
	}
	if errors.Is(err, errIntegrationDSNUnset) {
		t.Fatalf("a required DSN still asked to skip: %v", err)
	}
	if !strings.Contains(err.Error(), "CSX_TEST_DSN") || !strings.Contains(err.Error(), "CSX_REQUIRE_TEST_DSN") {
		t.Fatalf("the failure names neither variable, so nobody can fix it: %v", err)
	}
}

func TestPostgresDSNRequireOffKeepsTheSkip(t *testing.T) {
	for _, off := range []string{"0", "false", "FALSE", "f"} {
		if _, err := integrationDSN("", off); !errors.Is(err, errIntegrationDSNUnset) {
			t.Fatalf("CSX_REQUIRE_TEST_DSN=%q should leave the skip in place, got %v", off, err)
		}
	}
}

// A guard that reads an unparsable setting as "off" fails silently in exactly
// the situation it exists for, so anything that is not plainly false requires.
func TestPostgresDSNRequireFailsClosedOnAnUnparsableValue(t *testing.T) {
	for _, on := range []string{"1", "true", "yes", "please"} {
		_, err := integrationDSN("", on)
		if err == nil || errors.Is(err, errIntegrationDSNUnset) {
			t.Fatalf("CSX_REQUIRE_TEST_DSN=%q was treated as off: %v", on, err)
		}
	}
}
