package domain

import "testing"

func TestRequestDiagnosticsRecognizesLetterOnlyErrnos(t *testing.T) {
	for _, code := range []string{"ENOENT", "EACCES", "EPERM"} {
		got := requestDiagnostics(SearchRequest{Query: "operation failed with " + code})
		found := false
		for _, v := range got {
			if v == code {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s not recognized: %#v", code, got)
		}
	}
}

func TestRequestDiagnosticsRejectsGenericAllCapsWords(t *testing.T) {
	for _, word := range []string{"ERROR", "EXACT", "SHA"} {
		got := requestDiagnostics(SearchRequest{Query: "ordinary prose " + word})
		for _, v := range got {
			if v == word {
				t.Fatalf("generic word %s promoted: %#v", word, got)
			}
		}
	}
}
