package python

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

var lexPyLibs = []scanner.ResolvedPackage{
	{PURL: domain.PURL{Ecosystem: "pypi", Name: "fastapi", Version: "0.115.0"}},
	{PURL: domain.PURL{Ecosystem: "pypi", Name: "pyyaml", Version: "6.0.2"}},
	{PURL: domain.PURL{Ecosystem: "pypi", Name: "requests", Version: "2.32.3"}},
}

// scanPySource scans a project holding one app.py with src and returns the
// usages by family.
func scanPySource(t *testing.T, src string) map[string]scanner.SymbolUsage {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := scanSymbols(context.Background(), dir, lexPyLibs, clocklessPyBudget())
	if err != nil {
		t.Fatal(err)
	}
	byFamily := make(map[string]scanner.SymbolUsage, len(got))
	for _, u := range got {
		byFamily[u.Family] = u
	}
	return byFamily
}

// assertPyFamilies fails unless the scan recorded exactly want, each with its
// confidence.
func assertPyFamilies(t *testing.T, got map[string]scanner.SymbolUsage, want map[string]domain.SymbolConfidence) {
	t.Helper()
	for family, conf := range want {
		u, ok := got[family]
		if !ok {
			t.Errorf("missing %s; got %v", family, got)
			continue
		}
		if u.Confidence != conf {
			t.Errorf("%s confidence = %q, want %q", family, u.Confidence, conf)
		}
	}
	for family := range got {
		if _, ok := want[family]; !ok {
			t.Errorf("unexpected usage %s; got %v", family, got)
		}
	}
}

func TestScanSymbolsMultilineFromImportComments(t *testing.T) {
	src := "from fastapi import (  # routing, see (docs\n" +
		"    FastAPI,  # app object, also wraps (Starlette)\n" +
		"    # Depends, Header  -- dropped, kept for reference\n" +
		"    APIRouter as Router,  # note: comma, words) here\n" +
		"    Query\n" +
		")\n" +
		"from yaml import safe_load  # pick (one) of\n" +
		"import requests\n"
	assertPyFamilies(t, scanPySource(t, src), map[string]domain.SymbolConfidence{
		"fastapi.FastAPI":   domain.SymbolProbable,
		"fastapi.APIRouter": domain.SymbolProbable,
		"fastapi.Query":     domain.SymbolProbable,
		"yaml.safe_load":    domain.SymbolProbable,
		"requests":          domain.SymbolUnknown,
	})
}

// An opening parenthesis in a one-line import's comment must not pull the
// lines after it into that import.
func TestScanSymbolsOneLineImportCommentParenDoesNotSwallow(t *testing.T) {
	src := "from fastapi import FastAPI  # see (issue\n" +
		"from yaml import safe_load\n" +
		"x = compute(a, b)\n"
	assertPyFamilies(t, scanPySource(t, src), map[string]domain.SymbolConfidence{
		"fastapi.FastAPI": domain.SymbolProbable,
		"yaml.safe_load":  domain.SymbolProbable,
	})
}

func TestScanSymbolsAliasedImportCommentKeepsDynamicDegrade(t *testing.T) {
	src := "import fastapi as fa  # the web framework\n" +
		"from fastapi import FastAPI\n" +
		"from yaml import safe_load\n" +
		"thing = getattr(fa, \"FastAPI\")\n"
	assertPyFamilies(t, scanPySource(t, src), map[string]domain.SymbolConfidence{
		"fastapi":         domain.SymbolUnknown,
		"fastapi.FastAPI": domain.SymbolUnknown,
		"yaml.safe_load":  domain.SymbolProbable,
	})
}

func TestScanSymbolsSemicolonAndBackslashStatements(t *testing.T) {
	src := "from fastapi import (FastAPI); fn(x, y)\n" +
		"from yaml import safe_load; z = other(p, q)\n" +
		"import requests; w = call(m, n)\n" +
		"from fastapi import APIRouter, \\\n" +
		"    Query\n" +
		"import yaml.constructor \\\n" +
		"    as yc\n"
	assertPyFamilies(t, scanPySource(t, src), map[string]domain.SymbolConfidence{
		"fastapi.FastAPI":   domain.SymbolProbable,
		"fastapi.APIRouter": domain.SymbolProbable,
		"fastapi.Query":     domain.SymbolProbable,
		"yaml.safe_load":    domain.SymbolProbable,
		"yaml.constructor":  domain.SymbolUnknown,
		"requests":          domain.SymbolUnknown,
	})
}

// Imports and dynamic access written inside docstrings, strings and comments
// are text, not code: they record nothing and degrade nothing.
func TestScanSymbolsIgnoresStringsAndComments(t *testing.T) {
	src := "\"\"\"Example usage:\n" +
		"\n" +
		"    import requests\n" +
		"    from fastapi import Depends\n" +
		"    mod = importlib.import_module(\"yaml\")\n" +
		"\"\"\"\n" +
		"import fastapi\n" +
		"from fastapi import FastAPI\n" +
		"from yaml import safe_load\n" +
		"\n" +
		"def f():\n" +
		"    r'''raw docstring: from fastapi import Header; getattr(fastapi, 'x')'''\n" +
		"    note = 'import requests; __import__(\"yaml\")'\n" +
		"    doc = b\"from fastapi import Cookie\"\n" +
		"    msg = f\"getattr(fastapi, {name})\"\n" +
		"    # getattr(fastapi, \"FastAPI\"); importlib.import_module('yaml')\n" +
		"    return 'it''s'\n"
	assertPyFamilies(t, scanPySource(t, src), map[string]domain.SymbolConfidence{
		"fastapi":         domain.SymbolUnknown,
		"fastapi.FastAPI": domain.SymbolProbable,
		"yaml.safe_load":  domain.SymbolProbable,
	})
}

// A real dynamic import still degrades, including one written right after a
// docstring and one with a single-quoted or raw module literal.
func TestScanSymbolsRealDynamicImportsStillDegrade(t *testing.T) {
	src := "\"\"\"Loads yaml lazily.\"\"\"\n" +
		"import importlib\n" +
		"from fastapi import FastAPI\n" +
		"from yaml import safe_load\n" +
		"mod = importlib.import_module( r'yaml' )  # dynamic\n" +
		"web = __import__('fastapi.routing')\n"
	assertPyFamilies(t, scanPySource(t, src), map[string]domain.SymbolConfidence{
		"fastapi.FastAPI": domain.SymbolUnknown,
		"yaml.safe_load":  domain.SymbolUnknown,
	})
}

// A module named by an f-string is not known, so it degrades nothing.
func TestScanSymbolsInterpolatedDynamicImportIgnored(t *testing.T) {
	src := "import importlib\n" +
		"from yaml import safe_load\n" +
		"mod = importlib.import_module(f\"yaml\")\n"
	assertPyFamilies(t, scanPySource(t, src), map[string]domain.SymbolConfidence{
		"yaml.safe_load": domain.SymbolProbable,
	})
}

func TestPyStatementsSplitsLogicalLines(t *testing.T) {
	src := "a = (1,\r\n 2)  # c ) ;\r\nb = 'x;y' ; c = \"\"\"q\n\"\"\"\nd = \\\n  3\n"
	var got [][]string
	pyStatements(src, func(st []pyTok) {
		var words []string
		for _, tk := range st {
			words = append(words, tk.text)
		}
		got = append(got, words)
	})
	want := [][]string{
		{"a", "=", "(", "1", ",", "2", ")"},
		{"b", "=", "x;y"},
		{"c", "=", "q\n"},
		{"d", "=", "3"},
	}
	if len(got) != len(want) {
		t.Fatalf("statements = %q, want %q", got, want)
	}
	for i := range want {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("statement %d = %q, want %q", i, got[i], want[i])
		}
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Fatalf("statement %d = %q, want %q", i, got[i], want[i])
			}
		}
	}
}
