// Package i18n is the website message catalog for the nine Public v1
// locales (plan Global Constraints + P6.3). Catalogs are embedded JSON
// maps of key → translated string; English is the reference catalog and
// the fallback for any unknown language or missing key.
//
// Only UI strings are translated. Data values — package names, versions,
// context labels like "node 22", confidence values, error codes — stay
// exactly as aggregated.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

//go:embed locales/*.json
var localeFS embed.FS

// Default is the fallback language.
const Default = "en"

// Supported lists the nine Public v1 locales in switcher display order.
var Supported = []string{"en", "ko", "ja", "zh-CN", "es", "fr", "de", "pt-BR", "ru"}

// NativeName maps a locale to its own-language display name for the
// language switcher (a language name is data, not UI copy).
var NativeName = map[string]string{
	"en":    "English",
	"ko":    "한국어",
	"ja":    "日本語",
	"zh-CN": "简体中文",
	"es":    "Español",
	"fr":    "Français",
	"de":    "Deutsch",
	"pt-BR": "Português (Brasil)",
	"ru":    "Русский",
}

var catalogs = map[string]map[string]string{}

func init() {
	for _, lang := range Supported {
		raw, err := localeFS.ReadFile("locales/" + lang + ".json")
		if err != nil {
			panic("i18n: missing locale file for " + lang + ": " + err.Error())
		}
		m := map[string]string{}
		if err := json.Unmarshal(raw, &m); err != nil {
			panic("i18n: bad locale file " + lang + ": " + err.Error())
		}
		catalogs[lang] = m
	}
}

// Has reports whether lang is a supported locale (exact code).
func Has(lang string) bool {
	_, ok := catalogs[lang]
	return ok
}

// T resolves key in lang, falling back to English, then to the key
// itself (visible and greppable rather than silently blank). When args
// are given the message is treated as a Sprintf format.
func T(lang, key string, args ...any) string {
	s, ok := "", false
	if m := catalogs[lang]; m != nil {
		s, ok = m[key]
	}
	if !ok {
		s, ok = catalogs[Default][key]
	}
	if !ok {
		return key
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

// Keys returns the sorted key set of a locale (mainly for tests).
func Keys(lang string) []string {
	m := catalogs[lang]
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Canonical maps a user-supplied language tag to a supported locale:
// exact (case-insensitive) match first, then base-language match with
// zh→zh-CN and pt→pt-BR region defaults.
func Canonical(tag string) (string, bool) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return "", false
	}
	low := strings.ToLower(tag)
	for _, l := range Supported {
		if strings.ToLower(l) == low {
			return l, true
		}
	}
	base := strings.SplitN(low, "-", 2)[0]
	switch base {
	case "zh":
		return "zh-CN", true
	case "pt":
		return "pt-BR", true
	}
	for _, l := range Supported {
		if strings.SplitN(strings.ToLower(l), "-", 2)[0] == base {
			return l, true
		}
	}
	return "", false
}

// Match negotiates a locale from an Accept-Language header value,
// honoring q-values, defaulting to English.
func Match(acceptLanguage string) string {
	type cand struct {
		lang string
		q    float64
		pos  int
	}
	var cands []cand
	for i, part := range strings.Split(acceptLanguage, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tag, q := part, 1.0
		if base, params, ok := strings.Cut(part, ";"); ok {
			tag = strings.TrimSpace(base)
			for _, p := range strings.Split(params, ";") {
				if k, v, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.TrimSpace(k) == "q" {
					if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
						q = f
					}
				}
			}
		}
		if q <= 0 {
			continue
		}
		cands = append(cands, cand{lang: tag, q: q, pos: i})
	}
	sort.SliceStable(cands, func(a, b int) bool {
		if cands[a].q != cands[b].q {
			return cands[a].q > cands[b].q
		}
		return cands[a].pos < cands[b].pos
	})
	for _, c := range cands {
		if l, ok := Canonical(c.lang); ok {
			return l
		}
	}
	return Default
}

// groupSep is the thousands separator per locale.
func groupSep(lang string) string {
	switch lang {
	case "de", "es", "pt-BR":
		return "."
	case "fr", "ru":
		return " " // no-break space
	default: // en, ko, ja, zh-CN
		return ","
	}
}

// FormatInt renders n with locale-appropriate digit grouping.
func FormatInt(lang string, n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	digits := strconv.FormatInt(n, 10)
	sep := groupSep(lang)
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	lead := len(digits) % 3
	if lead == 0 {
		lead = 3
	}
	b.WriteString(digits[:lead])
	for i := lead; i < len(digits); i += 3 {
		b.WriteString(sep)
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}

// FormatPercent renders a 0..1 ratio as a whole percentage with the
// locale's spacing convention.
func FormatPercent(lang string, ratio float64) string {
	pct := strconv.Itoa(int(ratio*100 + 0.5))
	switch lang {
	case "de", "fr", "ru":
		return pct + " %"
	default:
		return pct + "%"
	}
}
