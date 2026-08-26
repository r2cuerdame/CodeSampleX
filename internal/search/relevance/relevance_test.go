package relevance

import "testing"

func TestGenericVocabularyCannotDriftBetweenPackagesAndSymbols(t *testing.T) {
	for _, token := range []string{"model", "json", "server", "node", "process", "protocol"} {
		if !IsGeneric(token) {
			t.Errorf("%q is no longer generic", token)
		}
		strong, _ := Signal(token, "unrelated topic", nil, []string{"example." + token})
		if strong != 0 {
			t.Errorf("symbol subtoken %q produced strong=%d", token, strong)
		}
		strong, _ = Signal(token, "unrelated topic", []string{"example-" + token}, nil)
		if strong != 0 {
			t.Errorf("package subtoken %q produced strong=%d", token, strong)
		}
	}
}

func TestFullDottedSymbolIdentitySurvivesGenericFiltering(t *testing.T) {
	symbol := "model.server.node.json.process"
	strong, _ := Signal("call model.server.node.json.process directly", "unrelated", nil, []string{symbol})
	if strong == 0 {
		t.Fatal("the complete dotted symbol identity was discarded with its generic subtokens")
	}
	if !MatchesDeclaredSymbols([]string{symbol}, []string{symbol}) {
		t.Fatal("the complete dotted symbol did not match itself")
	}
}

func TestMatchedDeclaredSymbolsReturnsActualLaterIdentity(t *testing.T) {
	got := MatchedDeclaredSymbols(
		[]string{"missing.first", "TOOLS/LIST", "tools/list"},
		[]string{"tools/list", "other.symbol"},
	)
	if len(got) != 1 || got[0] != "tools/list" {
		t.Fatalf("matched symbols = %v, want actual declared tools/list once", got)
	}
}

// Every Go module hosted on GitHub carries "github" and "com" in its name,
// and the word said nothing about which module it is. Splitting the path into
// identifier words made both of them STRONG tokens — the class that means
// "the question named this library" and opens the topic gate on its own — so
// a question about GitHub Actions named github.com/dustin/go-humanize,
// github.com/caddyserver/caddy and every other module in the corpus equally.
//
// This is the R2C-159 fixture's root cause: a deploy question about
// workflow_dispatch was answered with a number-formatting sample whose only
// tie to it was the forge both of them happen to live on.
func TestForgeHostSegmentsAreNotPackageIdentity(t *testing.T) {
	for _, host := range []string{"github", "gitlab", "bitbucket", "com", "org", "www", "gopkg"} {
		if !IsGeneric(host) {
			t.Errorf("%q is treated as package identity", host)
		}
	}
	strong, _ := Signal(
		"GitHub Actions workflow_dispatch deploys an immutable canonical main commit",
		"Format integers and floats with thousand separators",
		[]string{"github.com/dustin/go-humanize"}, []string{"humanize.FormatInteger"})
	if strong != 0 {
		t.Errorf("a GitHub Actions question named a GitHub-hosted module: strong=%d", strong)
	}
}

// A Go major-version suffix is part of the import path, never a subject. It
// let "migrate to v2" name every /v2 module in the corpus at once.
func TestGoMajorVersionSuffixIsNotPackageIdentity(t *testing.T) {
	for _, suffix := range []string{"v2", "v3", "v10"} {
		if !IsGeneric(suffix) {
			t.Errorf("%q is treated as package identity", suffix)
		}
	}
	strong, _ := Signal("migrate this service to v2", "unrelated topic",
		[]string{"github.com/caddyserver/caddy/v2"}, nil)
	if strong != 0 {
		t.Errorf("a bare major-version token named a module: strong=%d", strong)
	}
}

// The word that cannot identify a package inside its NAME cannot identify it
// inside the goal sentence either. The goal for the shopspring sample ends
// "...in github.com/shopspring/decimal", so counting its path words as prose
// let a question that merely said "GitHub" collect the overlap the gate asks
// for before it will call two texts the same subject.
func TestForgeHostSegmentsInAGoalSentenceAreNotProse(t *testing.T) {
	_, prose := Signal(
		"GitHub Actions workflow_dispatch deploys an immutable canonical main commit and checks the exact SHA",
		"Perform exact banker's rounding in github.com/shopspring/decimal",
		[]string{"github.com/shopspring/decimal"}, nil)
	if prose > 1 {
		t.Errorf("forge path words in a goal sentence still count as shared prose: prose=%d", prose)
	}
	if AboutSameThing(
		"GitHub Actions workflow_dispatch deploys an immutable canonical main commit and checks the exact SHA",
		"Perform exact banker's rounding in github.com/shopspring/decimal",
		[]string{"github.com/shopspring/decimal"}, nil) {
		t.Error("a deploy question and a decimal-rounding sample were called the same subject")
	}
}

// The gate has to keep letting real subjects through: naming the module, or
// its last path segment, is what a question about it looks like.
func TestTheModuleNameItselfStillNamesTheModule(t *testing.T) {
	for _, query := range []string{
		"humanize.FormatInteger prints the wrong separator",
		"go-humanize formats integers with the wrong separator",
		"github.com/dustin/go-humanize formats integers with the wrong separator",
	} {
		strong, _ := Signal(query, "Format integers with thousand separators",
			[]string{"github.com/dustin/go-humanize"}, []string{"humanize.FormatInteger"})
		if strong == 0 {
			t.Errorf("a question that named the module found nothing strong: %q", query)
		}
	}
}
