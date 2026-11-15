package rust

import (
	"context"
	"sort"
	"strings"
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

// TestScanRustSourceIgnoresNonCode: comments, string and char literals and
// attributes are not code, so nothing written in them may become a family —
// and none of them may cost a real use beside it its family (#786).
func TestScanRustSourceIgnoresNonCode(t *testing.T) {
	identToPkg := map[string]domain.PURL{}
	for _, name := range []string{"serde", "serde_json", "tokio", "regex"} {
		identToPkg[name] = domain.PURL{Ecosystem: "cargo", Name: name, Version: "1.0.0"}
	}
	tests := []struct {
		name string
		src  string
		want []string // families, sorted
	}{
		{
			name: "line memo inside a multi-line use group",
			src:  "use serde::{ // memo\n    Deserialize,\n    Serialize, // another\n};\n",
			want: []string{"serde::Deserialize", "serde::Serialize"},
		},
		{
			name: "semicolon inside a memo does not end the statement",
			src:  "use serde::{\n    Deserialize, // keep; or not\n    de::{DeserializeOwned, /* a; b */ Error},\n    Serialize,\n};\n",
			want: []string{"serde::Deserialize", "serde::Serialize", "serde::de::DeserializeOwned", "serde::de::Error"},
		},
		{
			name: "multi-line memo inside a use group",
			src:  "use serde::{\n    /* first line\n       second; line */\n    Serialize,\n};\n",
			want: []string{"serde::Serialize"},
		},
		{
			name: "nested block comment hides an example use",
			src:  "/* outer /* inner */\nuse tokio::runtime::Runtime;\n*/\nuse serde::Serialize;\n",
			want: []string{"serde::Serialize"},
		},
		{
			name: "doc comments hide example uses",
			src:  "/// use tokio::spawn;\n//! extern crate regex;\n// use regex::Regex;\nuse serde::Serialize;\n",
			want: []string{"serde::Serialize"},
		},
		{
			name: "string literals hide example uses",
			src: "const A: &str = \"\nuse tokio::spawn;\n\";\n" +
				"const B: &str = \"escaped \\\" quote\nuse regex::Regex;\";\n" +
				"const C: &[u8] = b\"\nextern crate tokio;\n\";\n" +
				"use serde::Serialize;\n",
			want: []string{"serde::Serialize"},
		},
		{
			name: "raw string literals hide example uses",
			src: "const A: &str = r#\"\nuse tokio::spawn; \"quoted\"\n\"#;\n" +
				"const B: &str = r\"\nuse regex::Regex;\n\";\n" +
				"const C: &[u8] = br##\"\nextern crate tokio; \"# not yet\n\"##;\n" +
				"use serde::Serialize;\n",
			want: []string{"serde::Serialize"},
		},
		{
			name: "lifetimes and char literals before a use",
			src: "fn f<'a>(x: &'a str) -> &'static str { 'outer: loop { break 'outer; } }\n" +
				"const Q: char = '\"';\nconst S: char = ';';\nconst E: char = '\\'';\nconst U: char = '\\u{1F600}';\n" +
				"use serde::Serialize;\n",
			want: []string{"serde::Serialize"},
		},
		{
			name: "attributes before and inside a use group",
			src: "#[cfg(feature = \"x\")] use tokio::runtime::Runtime;\n" +
				"#![allow(unused)]\n" +
				"use serde::{\n    #[cfg(feature = \"derive\")]\n    Deserialize,\n    #[cfg(any(a, b))] Serialize,\n};\n",
			want: []string{"serde::Deserialize", "serde::Serialize", "tokio::runtime::Runtime"},
		},
		{
			name: "raw identifiers are normalized",
			src:  "use r#serde::r#type::Value;\nuse tokio::r#async;\nextern crate r#regex;\n",
			want: []string{"regex", "serde::type::Value", "tokio::async"},
		},
		{
			name: "fragments that are not paths are not recorded",
			src:  "use serde::{Ser ialize, de/x, Good, $m, *};\nuse serde_json::@x;\n",
			want: []string{"serde", "serde::Good"},
		},
		{
			name: "glob and self stay package level",
			src:  "use tokio::prelude::*;\nuse regex::{self};\n",
			want: []string{"regex", "tokio"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			scanRustSource(tt.src, identToPkg, func(u scanner.SymbolUsage) {
				got = append(got, u.Family)
			})
			sort.Strings(got)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("families = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBlankNonCodeKeepsLines(t *testing.T) {
	src := "use a; // x\n/* y\n z */ \"s\nt\" r#\"u\nv\"# 'c' #[attr(\n)]\n"
	got := blankNonCode(src)
	if len(got) != len(src) {
		t.Fatalf("len = %d, want %d", len(got), len(src))
	}
	if strings.Count(got, "\n") != strings.Count(src, "\n") {
		t.Errorf("newlines = %d, want %d: %q", strings.Count(got, "\n"), strings.Count(src, "\n"), got)
	}
	if strings.TrimSpace(got) != "use a;" {
		t.Errorf("blankNonCode left %q, want only %q", strings.TrimSpace(got), "use a;")
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
