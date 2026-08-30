package i18n

import "strings"

// pluralForm is the CLDR plural category for n in lang.
//
// Only the categories the shipped locales actually use. English, German,
// Spanish and Portuguese split at one; French counts zero with the singular;
// Russian takes three and decides on the last digits, so 11 is "many" while 1
// and 21 are "one". Korean, Japanese and Chinese do not inflect at all and
// always take the one form the catalogue holds.
func pluralForm(lang string, n int64) string {
	if n < 0 {
		n = -n
	}
	switch lang {
	case "ko", "ja", "zh-CN":
		return "other"
	case "fr":
		if n == 0 || n == 1 {
			return "one"
		}
		return "other"
	case "ru":
		mod10, mod100 := n%10, n%100
		switch {
		case mod10 == 1 && mod100 != 11:
			return "one"
		case mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14):
			return "few"
		default:
			return "many"
		}
	default: // en, de, es, pt-BR
		if n == 1 {
			return "one"
		}
		return "other"
	}
}

// Plural renders a count together with the noun it counts, inflected for the
// locale and with the number formatted for it.
//
// The count and the noun travel together deliberately. Every place this
// replaces built the phrase by concatenating a raw number with one hardcoded
// noun, which is exactly how "1 findings across 1 ecosystems", "1 tools" and
// "1 anonymous daily reports" reached the live pages: the number was right and
// nothing could make the word agree with it.
//
// The catalogue holds one entry per form under "<key>.<form>", each containing
// a single %s where the number goes. A missing form falls back to "other", then
// to the bare key, then to the number alone — never to nothing, because an
// empty string is an invisible gap where a visible defect would at least be
// found.
func Plural(lang, key string, n int64) string {
	num := FormatInt(lang, n)
	form := pluralForm(lang, n)

	for _, k := range []string{key + "." + form, key + ".other", key} {
		if s := T(lang, k); s != "" {
			if strings.Contains(s, "%s") {
				return strings.Replace(s, "%s", num, 1)
			}
			return num + " " + s
		}
	}
	return num
}
