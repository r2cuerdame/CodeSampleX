package i18n

import (
	"strings"
	"testing"
	"unicode"
)

// A translation written in the wrong script is invisible to every check a
// JSON file gets: the key is present, the value is non-empty, the length is
// plausible. It is only visible to the one audience that will ever read it.
//
// This caught a real one — a Russian verb sitting inside a Japanese
// sentence on the contribute page, where nothing but a reader of Japanese
// would ever have noticed.
//
// The rule is one-directional on purpose: Latin text inside a CJK string is
// normal (package names, HTTP verbs, PUBLISHED, libc), so only the scripts
// that belong to exactly one of these locales are policed.
func TestNoForeignScriptInTranslations(t *testing.T) {
	owners := []struct {
		lang  string
		table *unicode.RangeTable
		name  string
	}{
		{"ru", unicode.Cyrillic, "Cyrillic"},
		{"ko", unicode.Hangul, "Hangul"},
		{"ja", unicode.Hiragana, "Hiragana"},
		{"ja", unicode.Katakana, "Katakana"},
	}
	for _, lang := range Supported {
		for _, key := range Keys(lang) {
			val := T(lang, key)
			for _, o := range owners {
				if lang == o.lang {
					continue
				}
				if i := strings.IndexFunc(val, func(r rune) bool {
					return unicode.Is(o.table, r)
				}); i >= 0 {
					t.Errorf("%s/%s contains %s text at byte %d: %q",
						lang, key, o.name, i, val)
				}
			}
		}
	}
}
