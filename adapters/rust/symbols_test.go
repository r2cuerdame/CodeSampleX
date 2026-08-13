package rust

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

func TestScanSymbols(t *testing.T) {
	a := New()
	dir := fixtureDir(t)
	pkgs, err := a.ScanPackages(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	usages, err := a.ScanSymbols(context.Background(), dir, pkgs)
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		pkg, family string
	}
	got := map[key]scanner.SymbolUsage{}
	for _, u := range usages {
		k := key{u.Package.Name, u.Family}
		if _, dup := got[k]; dup {
			t.Errorf("duplicate usage %v", k)
		}
		got[k] = u
	}
	want := []struct {
		pkg, family string
		confidence  domain.SymbolConfidence
	}{
		// use serde::{Serialize, Deserialize} — multi-line group expands
		// to two distinct families.
		{"serde", "serde::Serialize", domain.SymbolProbable},
		{"serde", "serde::Deserialize", domain.SymbolProbable},
		{"serde", "serde::de::DeserializeOwned", domain.SymbolProbable},
		{"serde_json", "serde_json::json", domain.SymbolProbable},
		// extern crate ⇒ package-level UNKNOWN.
		{"serde_json", "serde_json", domain.SymbolUnknown},
		// '-'→'_' normalization: code ident local_helper maps to crate local-helper.
		{"local-helper", "local_helper::helper_fn", domain.SymbolProbable},
	}
	for _, w := range want {
		u, ok := got[key{w.pkg, w.family}]
		if !ok {
			t.Errorf("missing usage pkg=%s family=%s (have %v)", w.pkg, w.family, got)
			continue
		}
		if u.Confidence != w.confidence {
			t.Errorf("%s %s: confidence = %q, want %q", w.pkg, w.family, u.Confidence, w.confidence)
		}
		if u.Package.Ecosystem != "cargo" {
			t.Errorf("%s: ecosystem = %q, want cargo", w.family, u.Package.Ecosystem)
		}
	}
	// Macro-only usage must NOT be attributed: #[derive(Serialize, Deserialize)]
	// contributes nothing beyond the use-statement families above.
	serdeFamilies := 0
	for k := range got {
		if k.pkg == "serde" {
			serdeFamilies++
		}
	}
	if serdeFamilies != 3 {
		t.Errorf("serde families = %d, want exactly 3 (macro invocations must not be attributed)", serdeFamilies)
	}
}

func TestExpandUseTree(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"serde::Deserialize", []string{"serde::Deserialize"}},
		{"serde::{Serialize, Deserialize}", []string{"serde::Serialize", "serde::Deserialize"}},
		{"serde::{de::{DeserializeOwned, Error}, Serialize}", []string{"serde::de::DeserializeOwned", "serde::de::Error", "serde::Serialize"}},
		{"serde::Deserialize as De", []string{"serde::Deserialize"}},
		{"serde", []string{"serde"}},
	}
	for _, tt := range tests {
		got := expandUseTree(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("expandUseTree(%q) = %v, want %v", tt.in, got, tt.want)
			continue
		}
		for i := range tt.want {
			if got[i] != tt.want[i] {
				t.Errorf("expandUseTree(%q)[%d] = %q, want %q", tt.in, i, got[i], tt.want[i])
			}
		}
	}
}
