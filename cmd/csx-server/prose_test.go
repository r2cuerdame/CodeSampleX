package main

import "testing"

// The findings page promises a measurement in plain language. These are
// real contract lines from real samples the mill authored.
func TestReadsAsProse(t *testing.T) {
	prose := []string{
		"assert Deleting a missing field with OpenStruct#delete_field raises NameError and its message uses single-quoted key syntax",
		"Stack::new(layer_a, layer_b).layer(base) wraps base with layer_a first, executing layer_b before layer_a on incoming requests",
		"connect, read, write and pool each get their own five seconds",
		// A sentence that cites a call is still a sentence: it keeps
		// saying things after the closing bracket.
		"ms(604800000) returns the string 7d rather than 1w, because the largest unit it formats is a day",
		"assert that reading a SQLite INTEGER value greater than i32::MAX into i32 returns an error",
		"the server receives the body as application/json with no charset appended",
	}
	code := []string{
		"assert.strictEqual(ms(604800000), '7d');",
		// Reached the live page under the token-count rule: seven tokens,
		// six identifiers, nothing a reader learns from.
		"assert.strictEqual(ms(604800000, { long: true }), '7 days');",
		"assertEquals(parser.parse(input).getValue(), expected.getValue());",
		"record.user.is_a?(Hash) && !record.user.is_a?(OpenStruct)",
		"assert!(call_without_poll_ready_panics())",
		"expect(x).toBe(1)",
		"",
		"   ",
	}
	for _, s := range prose {
		if !readsAsProse(s) {
			t.Errorf("rejected a sentence: %q", s)
		}
	}
	for _, s := range code {
		if readsAsProse(s) {
			t.Errorf("accepted an expression: %q", s)
		}
	}
}

// A sample whose every contract line is an expression contributes no
// finding at all, rather than a finding whose second half is code.
func TestFirstContractLineSkipsExpressions(t *testing.T) {
	got := firstContractLine([]string{
		"assert.strictEqual(ms(604800000), '7d');",
		"ms(1000, {long: true}) returns the string '1 second' rather than '1000 milliseconds'",
	})
	want := "ms(1000, {long: true}) returns the string '1 second' rather than '1000 milliseconds'"
	if got != want {
		t.Errorf("firstContractLine = %q, want the prose line", got)
	}
	if firstContractLine([]string{"expect(a).toBe(b)", "assert(x)"}) != "" {
		t.Error("a contract with no readable line must yield no finding")
	}
}
