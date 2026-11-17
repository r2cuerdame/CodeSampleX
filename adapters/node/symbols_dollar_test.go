package node

import "testing"

func TestExtractUsesDollarDefaultMember(t *testing.T) {
	uses := extractUses("import $ from 'jquery'\n$.ajax({})\n")
	u, ok := rawUseOf(uses, "jquery", "ajax")
	if !ok || u.kind != "method" {
		t.Fatalf("jquery.ajax method missing: %v", uses)
	}
	if _, ok := rawUseOf(uses, "jquery", ""); ok {
		t.Errorf("module-level row kept beside a member use: %v", uses)
	}
}

func TestExtractUsesDollarNamedTemplateCall(t *testing.T) {
	uses := extractUses("import { $ } from 'zx'\nawait $`ls -la`\n")
	if _, ok := rawUseOf(uses, "zx", "$"); !ok {
		t.Fatalf("zx.$ missing: %v", uses)
	}
}

func TestExtractUsesDollarPrefixedAndSuffixedNames(t *testing.T) {
	src := "import { $fetch } from 'ofetch'\n" +
		"import { interval as obs$ } from 'rxjs'\n" +
		"await $fetch('/x')\nobs$(10).subscribe()\n"
	uses := extractUses(src)
	if _, ok := rawUseOf(uses, "ofetch", "$fetch"); !ok {
		t.Errorf("ofetch.$fetch missing: %v", uses)
	}
	if _, ok := rawUseOf(uses, "rxjs", "interval"); !ok {
		t.Errorf("rxjs.interval missing: %v", uses)
	}
}

func TestExtractUsesDollarAdjacentNameIsNotUse(t *testing.T) {
	uses := extractUses("import { get } from 'lodash'\n$get(1)\nget$(2)\n")
	if _, ok := rawUseOf(uses, "lodash", "get"); ok {
		t.Fatalf("unused lodash.get recorded: %v", uses)
	}
	n := 0
	for _, u := range uses {
		if u.spec == "lodash" {
			n++
			if u.member != "" || u.kind != "module" {
				t.Errorf("unexpected lodash row %v", u)
			}
		}
	}
	if n != 1 {
		t.Errorf("want one module-level lodash row, got %d: %v", n, uses)
	}
}

func TestExtractUsesDollarAdjacentBindingIsNotMemberUse(t *testing.T) {
	uses := extractUses("import axios from 'axios'\n$axios.get('/x')\naxios$.post('/y')\n")
	for _, m := range []string{"get", "post"} {
		if _, ok := rawUseOf(uses, "axios", m); ok {
			t.Errorf("axios.%s recorded from another name: %v", m, uses)
		}
	}
	if _, ok := rawUseOf(uses, "axios", ""); !ok {
		t.Errorf("module-level axios row missing: %v", uses)
	}
}

func TestExtractUsesPlainNamesUnchanged(t *testing.T) {
	src := "import axios from 'axios'\nimport { get } from 'lodash'\n" +
		"axios.get('/x')\nthis.axios.post('/y')\nget(1)\n"
	uses := extractUses(src)
	for _, want := range [][2]string{{"axios", "get"}, {"axios", "post"}, {"lodash", "get"}} {
		if _, ok := rawUseOf(uses, want[0], want[1]); !ok {
			t.Errorf("%s.%s missing: %v", want[0], want[1], uses)
		}
	}
}

func TestExtractUsesRejectedPrefixDoesNotHideLaterUse(t *testing.T) {
	uses := extractUses("import a from 'lib'\nba.a.x\n")
	if _, ok := rawUseOf(uses, "lib", "x"); !ok {
		t.Errorf("lib.x missing after rejected prefix match: %v", uses)
	}
	if _, ok := rawUseOf(uses, "lib", "a"); ok {
		t.Errorf("lib.a recorded from ba.a: %v", uses)
	}
}
