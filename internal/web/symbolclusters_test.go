package web

import (
	"strings"
	"testing"
)

// A symbol page showed only the failures filed under that exact symbol, so
// yaml 2.9.0's Document page rendered a "failure clusters" heading with
// nothing under it — while the release it belongs to had failures recorded
// against the package, in the environment the reader was asking about.
//
// A package-level failure is a failure of every symbol in it: the build broke,
// and which API you were reading does not change that. It is the same rule the
// cube already applies when it narrows clusters to a coordinate.
func TestASymbolPageShowsThePackagesFailuresToo(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/npm/axios/1.12.0/axios.post").Body.String()

	if !strings.Contains(body, "sha256:ddeeff") {
		t.Error("the package-level failure recorded against 1.12.0 is missing")
	}
}

// A failure filed under a DIFFERENT symbol is not this symbol's.
func TestASymbolPageLeavesAnotherSymbolsFailureAlone(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.clusters["npm|axios"] = []string{`{
	  "symbol": "axios.get", "stage": "PROJECT_TEST",
	  "fingerprint": "sha256:othersymbol", "observationCount": 2,
	  "envSummary": {}, "versions": ["1.12.0"]
	}`}
	body := get(t, mux, "/npm/axios/1.12.0/axios.post").Body.String()
	if strings.Contains(body, "sha256:othersymbol") {
		t.Error("axios.get's failure is shown on axios.post's page")
	}
}
