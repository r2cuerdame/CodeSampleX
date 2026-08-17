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
