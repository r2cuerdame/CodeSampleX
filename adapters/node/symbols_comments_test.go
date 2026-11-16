package node

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func rawUseOf(uses []rawUse, spec, member string) (rawUse, bool) {
	for _, u := range uses {
		if u.spec == spec && u.member == member {
			return u, true
		}
	}
	return rawUse{}, false
}

func specsOf(uses []rawUse) map[string]bool {
	out := map[string]bool{}
	for _, u := range uses {
		out[u.spec] = true
	}
	return out
}

func TestExtractUsesNamedListLineComment(t *testing.T) {
	src := "import { a, // note\n  b } from 'x'\na(); b();\n"
	uses := extractUses(src)
	for _, m := range []string{"a", "b"} {
		if _, ok := rawUseOf(uses, "x", m); !ok {
			t.Errorf("x.%s missing from %v", m, uses)
		}
	}
	if _, ok := rawUseOf(uses, "x", "note"); ok {
		t.Errorf("comment word recorded: %v", uses)
	}
}

func TestExtractUsesNamedListBlockComment(t *testing.T) {
	src := "import { a, /* x, y */ b } from 'lib'\n" +
		"import {\n  c, /** see {@link Foo}, d */\n  e,\n} from 'other'\n" +
		"a(); b(); c(); e(); x(); y(); d(); Foo();\n"
	uses := extractUses(src)
	for _, want := range [][2]string{{"lib", "a"}, {"lib", "b"}, {"other", "c"}, {"other", "e"}} {
		if _, ok := rawUseOf(uses, want[0], want[1]); !ok {
			t.Errorf("%s.%s missing from %v", want[0], want[1], uses)
		}
	}
	for _, bad := range [][2]string{{"lib", "x"}, {"lib", "y"}, {"other", "d"}, {"other", "Foo"}, {"other", "link"}} {
		if _, ok := rawUseOf(uses, bad[0], bad[1]); ok {
			t.Errorf("comment word %s.%s recorded: %v", bad[0], bad[1], uses)
		}
	}
}

func TestExtractUsesCommentedOutImports(t *testing.T) {
	src := "// import axios from 'axios'\n" +
		"/**\n * Example:\n * import got from 'got'\n * const { request } = require('undici')\n * got.get(url)\n */\n" +
		"export const x = 1\n"
	uses := extractUses(src)
	specs := specsOf(uses)
	for _, s := range []string{"axios", "got", "undici"} {
		if specs[s] {
			t.Errorf("commented import %q recorded: %v", s, uses)
		}
	}
}

func TestExtractUsesCommentAndStringMentions(t *testing.T) {
	src := "import axios from 'axios'\n" +
		"// axios.get is going away\n" +
		"const label = 'axios.get'\n" +
		"const msg = \"use axios.put( instead\"\n" +
		"axios.post('/x')\n"
	uses := extractUses(src)
	if u, ok := rawUseOf(uses, "axios", "post"); !ok || u.kind != "method" || u.conf != domain.SymbolProbable {
		t.Errorf("axios.post = %+v, %v; want PROBABLE method", u, ok)
	}
	for _, m := range []string{"get", "put"} {
		if _, ok := rawUseOf(uses, "axios", m); ok {
			t.Errorf("axios.%s recorded from comment or string: %v", m, uses)
		}
	}
}

func TestExtractUsesSlashesInsideStrings(t *testing.T) {
	src := "import axios from 'axios'\n" +
		"const u = 'https://x'; axios.get(u)\n" +
		"const v = \"http://y/*\"\n" +
		"axios.put(v)\n" +
		"const w = `ftp://z/${axios.head(u)}`; axios.patch(w)\n"
	uses := extractUses(src)
	for _, m := range []string{"get", "put", "head", "patch"} {
		if _, ok := rawUseOf(uses, "axios", m); !ok {
			t.Errorf("axios.%s missing after slashes in string: %v", m, uses)
		}
	}
}

func TestExtractUsesTemplateTextIsNotCode(t *testing.T) {
	src := "import axios from 'axios'\n" +
		"const doc = `call axios.get( here\n// not a comment ${axios.delete(1)}`\n" +
		"axios.options()\n"
	uses := extractUses(src)
	if _, ok := rawUseOf(uses, "axios", "get"); ok {
		t.Errorf("template text recorded as use: %v", uses)
	}
	for _, m := range []string{"delete", "options"} {
		if _, ok := rawUseOf(uses, "axios", m); !ok {
			t.Errorf("axios.%s missing: %v", m, uses)
		}
	}
}

func TestExtractUsesRequireDestructureComment(t *testing.T) {
	src := "const {\n  Pool, // pooled }\n  Client /* single, x */\n} = require('pg')\n" +
		"new Pool(); new Client();\n"
	uses := extractUses(src)
	for _, m := range []string{"Pool", "Client"} {
		if _, ok := rawUseOf(uses, "pg", m); !ok {
			t.Errorf("pg.%s missing: %v", m, uses)
		}
	}
	for _, m := range []string{"x", "pooled", "single"} {
		if _, ok := rawUseOf(uses, "pg", m); ok {
			t.Errorf("comment word pg.%s recorded: %v", m, uses)
		}
	}
}

func TestExtractUsesNamedImportOnlyInComment(t *testing.T) {
	src := "import { debounce } from 'lodash'\n" +
		"// debounce here later\n" +
		"const s = 'debounce'\n"
	uses := extractUses(src)
	var lodash []rawUse
	for _, u := range uses {
		if u.spec == "lodash" {
			lodash = append(lodash, u)
		}
	}
	if len(lodash) != 1 || lodash[0].member != "" || lodash[0].kind != "module" || lodash[0].conf != domain.SymbolUnknown {
		t.Errorf("lodash uses = %+v, want one package-level UNKNOWN row", lodash)
	}
}

func TestExtractUsesRegexLiteralKeepsCode(t *testing.T) {
	src := "import axios from 'axios'\n" +
		"const q = /'/g; axios.get(q)\n" +
		"const r = /[/*]/; axios.put(r)\n" +
		"const half = total / 2; axios.head(half / 3)\n"
	uses := extractUses(src)
	for _, m := range []string{"get", "put", "head"} {
		if _, ok := rawUseOf(uses, "axios", m); !ok {
			t.Errorf("axios.%s missing next to regex or division: %v", m, uses)
		}
	}
}

func TestJSCodeKeepsLength(t *testing.T) {
	src := "a // é\n/* 한글 */ 'x\\'y' `t${b}` /re/g\r\n"
	for _, blank := range []bool{false, true} {
		got := jsCode(src, blank)
		if len(got) != len(src) {
			t.Fatalf("jsCode(blank=%v) length %d, want %d", blank, len(got), len(src))
		}
		for i := range src {
			if (src[i] == '\n') != (got[i] == '\n') {
				t.Fatalf("jsCode(blank=%v) moved a newline at %d: %q", blank, i, got)
			}
		}
	}
}
