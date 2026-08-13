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
	for _, lang := range Supported {
		keys := keySet(t, lang)
		if len(keys) != len(enKeys) {
			t.Errorf("locale %s: %d keys, en has %d", lang, len(keys), len(enKeys))
		}
		for _, k := range enKeys {
			v, ok := catalogs[lang][k]
			if !ok {
				t.Errorf("locale %s: missing key %q", lang, k)
				continue
			}
			if strings.TrimSpace(v) == "" {
				t.Errorf("locale %s: empty value for %q", lang, k)
			}
		}
		for _, k := range keys {
			if _, ok := catalogs["en"][k]; !ok {
				t.Errorf("locale %s: extra key %q not in en", lang, k)
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
	if got := T("en", "landing.tagline"); got != "Stop solving the same code twice." {
		t.Fatalf("en tagline = %q", got)
	}
	ko := T("ko", "landing.tagline")
	if ko == "" || ko == T("en", "landing.tagline") {
		t.Fatalf("ko tagline must be translated, got %q", ko)
	}
	if !strings.Contains(ko, "코드") {
		t.Fatalf("ko tagline does not read as Korean: %q", ko)
	}
}

func TestI18nFallbackAndArgs(t *testing.T) {
	// Unknown language falls back to en.
	if got := T("xx", "landing.tagline"); got != "Stop solving the same code twice." {
		t.Fatalf("fallback = %q", got)
	}
	// Unknown key returns the key itself (visible, greppable).
	if got := T("en", "no.such.key"); got != "no.such.key" {
		t.Fatalf("missing key = %q", got)
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
