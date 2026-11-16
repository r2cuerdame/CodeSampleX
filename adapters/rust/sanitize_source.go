package rust

import "unicode/utf8"

// blankNonCode returns src with every comment, string literal, character
// literal and attribute replaced by spaces. Newlines are kept, so the
// line-anchored use/extern crate regexes still see each statement start a
// line, and nothing written in prose — a memo inside a multi-line use group,
// an example in a doc comment or a raw string — can reach a symbol family.
// It is a lexer pass, not a parse: it only has to tell code from non-code.
func blankNonCode(src string) string {
	return blankAttributes(blankCommentsAndLiterals(src))
}

func blankCommentsAndLiterals(src string) string {
	out := []byte(src)
	blank := func(from, to int) {
		for k := from; k < to; k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
	}
	n := len(src)
	for i := 0; i < n; {
		c := src[i]
		switch {
		case c == '/' && i+1 < n && src[i+1] == '/':
			end := i + 2
			for end < n && src[end] != '\n' {
				end++
			}
			blank(i, end)
			i = end
		case c == '/' && i+1 < n && src[i+1] == '*':
			end := blockCommentEnd(src, i)
			blank(i, end)
			i = end
		case c == 'r' && rawStringStarts(src, i):
			end := rawStringEnd(src, i)
			blank(i, end)
			i = end
		case c == '"':
			end := quotedStringEnd(src, i)
			blank(i, end)
			i = end
		case c == '\'':
			// A char literal is consumed; a lifetime or label ('a, 'static,
			// '_) is left as code and must not open a quote.
			if end, ok := charLiteralEnd(src, i); ok {
				blank(i, end)
				i = end
			} else {
				i++
			}
		default:
			i++
		}
	}
	return string(out)
}

// blockCommentEnd returns the index just past the /* … */ comment opening at
// i. Rust block comments nest; an unterminated one runs to the end.
func blockCommentEnd(src string, i int) int {
	depth := 0
	for j := i; j < len(src); {
		switch {
		case src[j] == '/' && j+1 < len(src) && src[j+1] == '*':
			depth++
			j += 2
		case src[j] == '*' && j+1 < len(src) && src[j+1] == '/':
			depth--
			j += 2
			if depth == 0 {
				return j
			}
		default:
			j++
		}
	}
	return len(src)
}

// rawStringStarts reports whether the 'r' at i opens a raw string literal:
// r"…", r#"…"#, or the b/c-prefixed forms. An r inside an identifier, or a
// raw identifier like r#type (no quote after the hashes), is code.
func rawStringStarts(src string, i int) bool {
	if i > 0 {
		prev := src[i-1]
		if prev == '\'' {
			return false
		}
		if isIdentByte(prev) {
			if prev != 'b' && prev != 'c' {
				return false
			}
			if i > 1 && (isIdentByte(src[i-2]) || src[i-2] == '\'') {
				return false
			}
		}
	}
	j := i + 1
	for j < len(src) && src[j] == '#' {
		j++
	}
	return j < len(src) && src[j] == '"'
}

// rawStringEnd returns the index just past the raw string opening at i: the
// first quote followed by as many hashes as opened it.
func rawStringEnd(src string, i int) int {
	j := i + 1
	hashes := 0
	for src[j] == '#' {
		hashes++
		j++
	}
	for j++; j < len(src); j++ {
		if src[j] != '"' {
			continue
		}
		k := j + 1
		for k < len(src) && k-(j+1) < hashes && src[k] == '#' {
			k++
		}
		if k-(j+1) == hashes {
			return k
		}
	}
	return len(src)
}

// quotedStringEnd returns the index just past the "…" literal opening at i,
// honouring backslash escapes. A b or c prefix is left as code; it is
// harmless.
func quotedStringEnd(src string, i int) int {
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return len(src)
}

// charLiteralEnd recognises a closed char or byte literal at i: 'x', '"',
// '\n', '\u{1F600}' or an escaped quote. Anything else is a lifetime or a
// label.
func charLiteralEnd(src string, i int) (int, bool) {
	j := i + 1
	if j >= len(src) {
		return 0, false
	}
	if src[j] == '\\' {
		// The longest escape is \u{10FFFF}; stop well before a stray quote
		// further on could be mistaken for the close.
		for k := j + 2; k < len(src) && k <= j+10; k++ {
			if src[k] == '\'' {
				return k + 1, true
			}
			if src[k] == '\n' {
				break
			}
		}
		return 0, false
	}
	if src[j] == '\'' || src[j] == '\n' {
		return 0, false
	}
	_, size := utf8.DecodeRuneInString(src[j:])
	if k := j + size; k < len(src) && src[k] == '\'' {
		return k + 1, true
	}
	return 0, false
}

// blankAttributes blanks #[…] and #![…] in source whose comments and literals
// are already blank, so a bracket inside a string cannot unbalance it. An
// attribute on a use, or on one item of a use group (#[cfg(…)]), would
// otherwise sit inside the captured tree. An unclosed attribute is left alone.
func blankAttributes(src string) string {
	out := []byte(src)
	n := len(src)
	for i := 0; i < n; i++ {
		if src[i] != '#' {
			continue
		}
		j := i + 1
		if j < n && src[j] == '!' {
			j++
		}
		if j >= n || src[j] != '[' {
			continue
		}
		depth, end := 0, -1
		for k := j; k < n; k++ {
			if src[k] == '[' {
				depth++
			} else if src[k] == ']' {
				depth--
				if depth == 0 {
					end = k + 1
					break
				}
			}
		}
		if end < 0 {
			continue
		}
		for k := i; k < end; k++ {
			if out[k] != '\n' {
				out[k] = ' '
			}
		}
		i = end - 1
	}
	return string(out)
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= utf8.RuneSelf
}
