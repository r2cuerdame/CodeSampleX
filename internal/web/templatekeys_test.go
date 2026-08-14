package web

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// tKeyRe finds every {{.T "some.key"}} a template asks for.
var tKeyRe = regexp.MustCompile(`{{-?[ ]*(?:\.T|\$\.T)[ ]+"([a-zA-Z0-9_.]+)"`)

// A key with no entry used to render as its own name, and one of them —
// landing.network_heading — shipped as a visible <h2> on the homepage in all
// nine languages. The renderer is silent about it now, which means this test
// is the only thing standing between a missing key and a blank heading.
func TestEveryTemplateKeyExists(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("templates", "*.html"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no templates found: %v", err)
	}

	used := map[string][]string{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range tKeyRe.FindAllStringSubmatch(string(raw), -1) {
			used[m[1]] = append(used[m[1]], filepath.Base(f))
		}
	}
	if len(used) == 0 {
		t.Fatal("found no translation keys; the pattern stopped matching")
	}

	keys := make([]string, 0, len(used))
	for k := range used {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, lang := range i18n.Supported {
		have := map[string]bool{}
		for _, k := range i18n.Keys(lang) {
			have[k] = true
		}
		for _, k := range keys {
			if !have[k] {
				t.Errorf("locale %s has no entry for %q (used in %v)", lang, k, used[k])
			}
		}
	}
}
