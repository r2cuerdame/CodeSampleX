package python

import "strings"

type pyTokKind uint8

const (
	pyName pyTokKind = iota // identifier or keyword
	pyNum                   // numeric literal, kept only so it is not read as a name
	pyStr                   // string literal; text is the body between the quotes
	pyOp                    // any other single character: . , * ( ) = ...
)

type pyTok struct {
	kind pyTokKind
	text string
	// interp marks an f- or t-string, whose body is not a plain value.
	interp bool
}

// pyStatements splits Python source into logical statements and calls emit
// with the code tokens of each. It is a lexer, not a parser: comments are
// dropped, a string literal (single, triple-quoted, any prefix) becomes one
// token so nothing inside it reads as code, and a statement ends at a newline
// or a semicolon outside brackets. A newline inside brackets, or after a
// backslash, continues the statement. emit must not keep the slice.
func pyStatements(src string, emit func([]pyTok)) {
	var toks []pyTok
	depth := 0
	flush := func() {
		if len(toks) > 0 {
			emit(toks)
			toks = toks[:0]
		}
	}
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '\n':
			if depth == 0 {
				flush()
			}
			i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '\\':
			// A line continuation joins the next line; a stray backslash is
			// skipped like whitespace.
			i++
			if i < len(src) && src[i] == '\r' {
				i++
			}
			if i < len(src) && src[i] == '\n' {
				i++
			}
		case c == '\'' || c == '"':
			body, end := pyStringAt(src, i)
			toks = append(toks, pyTok{kind: pyStr, text: body})
			i = end
		case isPyIdentStart(c):
			j := i + 1
			for j < len(src) && isPyIdentByte(src[j]) {
				j++
			}
			word := src[i:j]
			if j < len(src) && (src[j] == '\'' || src[j] == '"') && isPyStrPrefix(word) {
				body, end := pyStringAt(src, j)
				toks = append(toks, pyTok{kind: pyStr, text: body, interp: strings.ContainsAny(strings.ToLower(word), "ft")})
				i = end
				continue
			}
			toks = append(toks, pyTok{kind: pyName, text: word})
			i = j
		case c >= '0' && c <= '9':
			j := i + 1
			for j < len(src) && (isPyIdentByte(src[j]) || src[j] == '.') {
				j++
			}
			toks = append(toks, pyTok{kind: pyNum, text: src[i:j]})
			i = j
		case c == ';' && depth == 0:
			flush()
			i++
		default:
			switch c {
			case '(', '[', '{':
				depth++
			case ')', ']', '}':
				if depth > 0 {
					depth--
				}
			}
			toks = append(toks, pyTok{kind: pyOp, text: src[i : i+1]})
			i++
		}
	}
	flush()
}

// pyStringAt reads the string literal whose opening quote is at src[q] and
// returns its body and the index just past it. A backslash always keeps the
// next character from closing the literal, raw or not, so the prefix does not
// matter here; the body is returned as written. A single-quoted literal that
// meets an unescaped newline is unterminated and ends there, leaving the
// newline to end the statement.
func pyStringAt(src string, q int) (string, int) {
	quote := src[q]
	triple := q+2 < len(src) && src[q+1] == quote && src[q+2] == quote
	start := q + 1
	if triple {
		start = q + 3
	}
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
		case '\n':
			if !triple {
				return src[start:i], i
			}
		case quote:
			if !triple {
				return src[start:i], i + 1
			}
			if i+2 < len(src) && src[i+1] == quote && src[i+2] == quote {
				return src[start:i], i + 3
			}
		}
	}
	return src[start:], len(src)
}

// isPyStrPrefix reports whether word is a string prefix Python accepts.
func isPyStrPrefix(word string) bool {
	switch strings.ToLower(word) {
	case "r", "u", "b", "f", "t", "rb", "br", "rf", "fr", "rt", "tr":
		return true
	}
	return false
}

// Bytes at or above 0x80 belong to non-ASCII identifiers; they are never
// Python punctuation.
func isPyIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isPyIdentByte(c byte) bool { return isPyIdentStart(c) || (c >= '0' && c <= '9') }
