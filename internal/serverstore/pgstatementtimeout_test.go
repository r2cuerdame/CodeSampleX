package serverstore

// statement_timeout is handed to PostgreSQL as text, and the text has to be
// something PostgreSQL parses. time.Duration.String() is not that: it renders
// ten seconds as "10s", which happens to parse, and eight minutes as "8m0s",
// which does not --
//
//	ERROR: invalid value for parameter "statement_timeout": "8m0s" (SQLSTATE 22023)
//
// Every existing caller passed a sub-minute value and never met the second
// form. The background refresh of the candidate snapshot was the first to
// cross a minute, and TestIntegrationUnhurriedExpansionTakesOneCore caught it
// before the first refresh ran on production (v0.1.106 shipped the "8m0s";
// its refresh would have failed on arrival and kept serving WANTED-only).
//
// Integer milliseconds parse for every value, so that is the only form
// handed over, and this pins it without a database.

import (
	"strings"
	"testing"
	"time"
)

func TestStatementTimeoutIsRenderedAsMillisecondsPostgresAccepts(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{10 * time.Second, "10000"},
		{8 * time.Minute, "480000"},
		{75 * time.Millisecond, "75"},
		{authoringExpansionStatementTimeout, "10000"},
		{authoringExpansionUnhurriedStatementTimeout, "480000"},
		// PostgreSQL treats 0 as "no timeout"; a bound that vanishes is the
		// opposite of what any caller here means, so the floor is 1ms.
		{0, "1"},
		{500 * time.Microsecond, "1"},
	} {
		got := pgStatementTimeout(tc.in)
		if got != tc.want {
			t.Errorf("pgStatementTimeout(%v) = %q, want %q", tc.in, got, tc.want)
		}
		if strings.ContainsAny(got, "hms.") {
			t.Errorf("pgStatementTimeout(%v) = %q carries a unit suffix PostgreSQL may refuse", tc.in, got)
		}
	}
}
