package compatibility

import (
	"strings"
	"testing"
)

// The contract is the list of assertions a sample's contract command
// actually RAN, offline in a pinned container, and passed. It is the most
// useful thing the network knows about a library — which overload, which
// argument shape, which option, what it raises instead — and none of it
// left the server: the shard carried the one-line goal and nothing else, so
// every search answer came back with "contract": null and an agent had to
// spend a second get_sample call to learn what was proven, which it would
// only do after already deciding to use the sample.
func TestShardCarriesWhatTheContractProved(t *testing.T) {
	lines := []string{
		"load() strictly requires a binary stream and fails with TypeError on text streams",
		"loads() strictly requires a str and fails with TypeError on raw bytes",
	}
	got := contractForShard(lines)
	if len(got) != 2 || got[0] != lines[0] {
		t.Fatalf("contract lines were not carried: %+v", got)
	}
}

// A shard is fetched by every client warming that package, so the list is
// bounded — and a truncated list that looked complete would itself be a
// claim the system cannot support: that these are ALL the assertions.
func TestATrimmedContractSaysItWasTrimmed(t *testing.T) {
	var many []string
	for i := 0; i < maxShardContractLines+5; i++ {
		many = append(many, "assertion number "+string(rune('a'+i)))
	}
	got := contractForShard(many)
	if len(got) != maxShardContractLines+1 {
		t.Fatalf("got %d lines, want %d plus a note", len(got), maxShardContractLines)
	}
	last := got[len(got)-1]
	if !strings.Contains(last, "more") {
		t.Errorf("the list was trimmed without saying so: %q", last)
	}
}

// A single very long assertion is trimmed with an ellipsis rather than
// dropped: a partial claim marked as partial beats silence.
func TestAnOverlongAssertionIsMarkedNotDropped(t *testing.T) {
	long := strings.Repeat("x", maxShardContractLen+50)
	got := contractForShard([]string{long})
	if len(got) != 1 {
		t.Fatalf("got %d lines, want 1", len(got))
	}
	if !strings.HasSuffix(got[0], "…") {
		t.Errorf("a truncated assertion is not marked as truncated: %q", got[0][:40])
	}
	if len(got[0]) > maxShardContractLen+4 {
		t.Errorf("line is %d bytes, want <= %d", len(got[0]), maxShardContractLen)
	}
}

// Empty and whitespace-only lines never reach a reader.
func TestBlankAssertionsAreDropped(t *testing.T) {
	got := contractForShard([]string{"", "   ", "real assertion"})
	if len(got) != 1 || got[0] != "real assertion" {
		t.Errorf("blank lines survived: %+v", got)
	}
}
