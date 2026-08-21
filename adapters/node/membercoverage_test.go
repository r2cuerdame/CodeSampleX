package node

import (
	"strings"
	"testing"
)

func families(t *testing.T, src string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, u := range extractUses(src) {
		if u.member != "" {
			out[u.spec+"."+u.member] = true
		}
	}
	return out
}

// A member usage was only recorded when the member was called immediately:
// the pattern required "(" or a template backtick right after it. Everything
// else about a package read as "imported, no symbol", which is why 84% of the
// most-observed packages carry no symbol-level evidence at all — the
// package-level row already says "it was imported", so a dropped member is
// the only part that was ever going to be new.
//
// The local name is an import binding, so "local.member" is a member of that
// package whatever punctuation follows it.
func TestMemberUsageIsNotOnlyCalls(t *testing.T) {
	src := `
import axios from 'axios';
import * as pg from 'pg';
axios.defaults.baseURL = 'https://x';
const create = axios.create;
const client = new pg.Client({});
if (axios.isCancel) {}
export const t = axios.CanceledError;
`
	got := families(t, src)
	for _, want := range []string{
		"axios.defaults",      // chained property assignment
		"axios.create",        // reference without a call
		"pg.Client",           // constructor
		"axios.isCancel",      // condition
		"axios.CanceledError", // re-export
	} {
		if !got[want] {
			t.Errorf("missed %q; found %v", want, keys(got))
		}
	}
}

// Calls must keep working, and a member of something that was never imported
// must never be attributed to a package.
func TestMemberUsageStillRequiresAnImportBinding(t *testing.T) {
	src := `
import axios from 'axios';
axios.post('/x');
notImported.somethingElse = 1;
res.status(200);
`
	got := families(t, src)
	if !got["axios.post"] {
		t.Errorf("lost a plain call: %v", keys(got))
	}
	for f := range got {
		if strings.HasPrefix(f, "notImported.") || strings.HasPrefix(f, "res.") {
			t.Errorf("attributed %q to a package that was never imported", f)
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
