package i18n

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// TestI18nLocaleCompleteness asserts every locale catalog carries exactly the
// same key set as English and that no value is empty. The site must never
// fall back silently for a supported language (plan P6.3).
func TestI18nLocaleCompleteness(t *testing.T) {
	if len(Supported) != 9 {
		t.Fatalf("want 9 supported locales, got %d: %v", len(Supported), Supported)
	}
	want := []string{"en", "ko", "ja", "zh-CN", "es", "fr", "de", "pt-BR", "ru"}
	for _, w := range want {
		if !Has(w) {
			t.Fatalf("locale %q not supported", w)
		}
	}
	enKeys := keySet(t, "en")
	if len(enKeys) == 0 {
		t.Fatal("en catalog is empty")
	}
	// Plural variants are counted separately. A locale's plural CATEGORIES
	// are a property of the language -- Russian needs three where English
	// needs two -- so demanding the same set as English would either forbid
	// the third form or force English to carry a duplicate of "other" under
	// two more names. What every locale must have is the "other" form, which
	// is what Plural falls back to.
	enPlain, enPlural := splitPluralKeys(enKeys)
	for _, lang := range Supported {
		keys := keySet(t, lang)
		plain, plural := splitPluralKeys(keys)
		if len(plain) != len(enPlain) {
			t.Errorf("locale %s: %d non-plural keys, en has %d", lang, len(plain), len(enPlain))
		}
		for _, k := range enPlain {
			v, ok := catalogs[lang][k]
			if !ok {
				t.Errorf("locale %s: missing key %q", lang, k)
				continue
			}
			if strings.TrimSpace(v) == "" {
				t.Errorf("locale %s: empty value for %q", lang, k)
			}
		}
		for _, k := range plain {
			if _, ok := catalogs["en"][k]; !ok {
				t.Errorf("locale %s: extra key %q not in en", lang, k)
			}
		}
		// Every plural key English declares must exist here in at least its
		// "other" form, and every form this locale declares must belong to a
		// key English declares.
		for base := range enPlural {
			if v, ok := catalogs[lang][base+".other"]; !ok || strings.TrimSpace(v) == "" {
				t.Errorf("locale %s: plural %q has no \"other\" form", lang, base)
			}
		}
		for base := range plural {
			if _, ok := enPlural[base]; !ok {
				t.Errorf("locale %s: plural %q is not declared in en", lang, base)
			}
		}
	}
}

func keySet(t *testing.T, lang string) []string {
	t.Helper()
	raw, err := localeFS.ReadFile("locales/" + lang + ".json")
	if err != nil {
		t.Fatalf("read locale %s: %v", lang, err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse locale %s: %v", lang, err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestI18nTagline(t *testing.T) {
	if got := T("en", "landing.tagline"); got != "Does it run there?" {
		t.Fatalf("en tagline = %q", got)
	}
	ko := T("ko", "landing.tagline")
	if ko == "" || ko == T("en", "landing.tagline") {
		t.Fatalf("ko tagline must be translated, got %q", ko)
	}
	if !strings.Contains(ko, "돌아갈까") {
		t.Fatalf("ko tagline does not read as Korean: %q", ko)
	}
}

func TestI18nFallbackAndArgs(t *testing.T) {
	// Unknown language falls back to en.
	if got := T("xx", "landing.tagline"); got != "Does it run there?" {
		t.Fatalf("fallback = %q", got)
	}
	// A missing key renders as NOTHING, never as its own name.
	//
	// This used to return the key so it would be visible and greppable, and
	// that is a good instinct pointed at the wrong audience: it made
	// "landing.network_heading" a visible <h2> on the live homepage in all
	// nine languages. TestEveryTemplateKeyExists is where a missing key gets
	// caught now — loudly, in front of a developer instead of a visitor.
	if got := T("en", "no.such.key"); got != "" {
		t.Fatalf("missing key rendered %q; it must render nothing", got)
	}
	// Args are applied via Sprintf when present.
	if got := T("en", "meta.explorer", "axios"); !strings.Contains(got, "axios") {
		t.Fatalf("args not applied: %q", got)
	}
}

func TestI18nCanonicalAndMatch(t *testing.T) {
	cases := map[string]string{
		"ko":    "ko",
		"KO":    "ko",
		"ko-KR": "ko",
		"zh":    "zh-CN",
		"zh-cn": "zh-CN",
		"pt":    "pt-BR",
		"pt-br": "pt-BR",
		"en-US": "en",
	}
	for in, want := range cases {
		got, ok := Canonical(in)
		if !ok || got != want {
			t.Errorf("Canonical(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	if _, ok := Canonical("tlh"); ok {
		t.Error("Canonical accepted unsupported language")
	}
	if got := Match("ko-KR,ko;q=0.9,en;q=0.8"); got != "ko" {
		t.Errorf("Match korean header = %q", got)
	}
	if got := Match("da, en-gb;q=0.8, en;q=0.7"); got != "en" {
		t.Errorf("Match english fallback = %q", got)
	}
	if got := Match(""); got != "en" {
		t.Errorf("Match empty = %q", got)
	}
	if got := Match("fr-CH;q=0.2, ja;q=0.9"); got != "ja" {
		t.Errorf("Match must honor q values, got %q", got)
	}
}

func TestI18nFormatInt(t *testing.T) {
	if got := FormatInt("en", 1234567); got != "1,234,567" {
		t.Errorf("en = %q", got)
	}
	if got := FormatInt("de", 1234567); got != "1.234.567" {
		t.Errorf("de = %q", got)
	}
	if got := FormatInt("fr", 1234567); got != "1 234 567" {
		t.Errorf("fr = %q", got)
	}
	if got := FormatInt("ko", -42); got != "-42" {
		t.Errorf("negative = %q", got)
	}
}

func TestI18nFormatCompactInt(t *testing.T) {
	cases := []struct {
		lang string
		n    int64
		want string
	}{
		{"en", 999, "999"},
		{"en", 1_000, "1K"},
		{"en", 17_500, "17.5K"},
		{"en", 45_213, "45.2K"},
		{"en", 999_949, "999.9K"},
		{"en", 999_950, "1M"},
		{"en", 1_250_000, "1.3M"},
		{"en", 999_950_000, "1B"},
		{"en", 999_950_000_000, "1T"},
		{"en", -17_500, "-17.5K"},
		{"de", 17_500, "17,5K"},
	}
	for _, tc := range cases {
		if got := FormatCompactInt(tc.lang, tc.n); got != tc.want {
			t.Errorf("FormatCompactInt(%q, %d) = %q, want %q", tc.lang, tc.n, got, tc.want)
		}
	}

	// Every supported catalog must be accepted. The five comma-decimal
	// locales localize the separator; the remaining four use a dot.
	for _, lang := range Supported {
		got := FormatCompactInt(lang, 17_500)
		want := "17.5K"
		switch lang {
		case "de", "es", "fr", "pt-BR", "ru":
			want = "17,5K"
		}
		if got != want {
			t.Errorf("locale %s compact value = %q, want %q", lang, got, want)
		}
	}
}

// splitPluralKeys separates ordinary keys from plural variants, returning the
// plain keys and the set of plural bases ("wanted.n_asks.one" -> "wanted.n_asks").
func splitPluralKeys(keys []string) ([]string, map[string]bool) {
	plain := make([]string, 0, len(keys))
	plural := map[string]bool{}
	for _, k := range keys {
		matched := false
		for _, form := range []string{".one", ".few", ".many", ".other"} {
			if strings.HasSuffix(k, form) {
				plural[strings.TrimSuffix(k, form)] = true
				matched = true
				break
			}
		}
		if !matched {
			plain = append(plain, k)
		}
	}
	return plain, plural
}
