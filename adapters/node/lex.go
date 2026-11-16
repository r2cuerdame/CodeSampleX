package node

import "strings"

var jsKeywordsBeforeExpr = map[string]bool{
	"return": true, "typeof": true, "instanceof": true, "in": true, "of": true,
	"new": true, "delete": true, "void": true, "throw": true, "case": true,
	"do": true, "else": true, "yield": true, "await": true,
}

func jsCode(src string, blankStrings bool) string {
	out := []byte(src)
	blank := func(from, to int) {
		to = min(to, len(out))
		for k := from; k < to; k++ {
			if out[k] != '\n' && out[k] != '\r' {
				out[k] = ' '
			}
		}
	}
	var holes []int
	depth := 0
	inTpl := false
	regexOK := true
	for i := 0; i < len(src); {
		c := src[i]
		if inTpl {
			switch {
			case c == '\\':
				if blankStrings {
					blank(i, i+2)
				}
				i += 2
			case c == '`':
				inTpl, regexOK = false, false
				i++
			case c == '$' && i+1 < len(src) && src[i+1] == '{':
				holes = append(holes, depth)
				depth++
				inTpl, regexOK = false, true
				i += 2
			default:
				if blankStrings {
					blank(i, i+1)
				}
				i++
			}
			continue
		}
		switch {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				end = len(src)
			} else {
				end += i
			}
			blank(i, end)
			i = end
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := len(src)
			if j := strings.Index(src[i+2:], "*/"); j >= 0 {
				end = i + 2 + j + 2
			}
			blank(i, end)
			i = end
		case c == '/' && regexOK:
			if end, ok := jsRegexEnd(src, i); ok {
				i, regexOK = end, false
			} else {
				i++
			}
		case c == '\'' || c == '"':
			bodyEnd, next := jsQuotedEnd(src, i)
			if blankStrings {
				blank(i+1, bodyEnd)
			}
			i, regexOK = next, false
		case c == '`':
			inTpl = true
			i++
		case isJSIdentStart(c):
			j := i + 1
			for j < len(src) && isJSIdentByte(src[j]) {
				j++
			}
			regexOK = jsKeywordsBeforeExpr[src[i:j]]
			i = j
		case c >= '0' && c <= '9':
			j := i + 1
			for j < len(src) && (isJSIdentByte(src[j]) || src[j] == '.') {
				j++
			}
			i, regexOK = j, false
		case c == '{':
			depth++
			i, regexOK = i+1, true
		case c == '}':
			if n := len(holes); n > 0 && holes[n-1] == depth-1 {
				holes = holes[:n-1]
				inTpl = true
			}
			if depth > 0 {
				depth--
			}
			i, regexOK = i+1, false
		case c == ')' || c == ']':
			i, regexOK = i+1, false
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		default:
			i, regexOK = i+1, true
		}
	}
	return string(out)
}

func jsQuotedEnd(src string, q int) (bodyEnd, next int) {
	quote := src[q]
	for i := q + 1; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
			if i+1 < len(src) && src[i] == '\r' && src[i+1] == '\n' {
				i++
			}
		case '\n':
			return i, i
		case quote:
			return i, i + 1
		}
	}
	return len(src), len(src)
}

func jsRegexEnd(src string, s int) (int, bool) {
	inClass := false
	for i := s + 1; i < len(src); i++ {
		switch src[i] {
		case '\\':
			i++
		case '\n', '\r':
			return 0, false
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if inClass {
				continue
			}
			i++
			for i < len(src) && isJSIdentByte(src[i]) {
				i++
			}
			return i, true
		}
	}
	return 0, false
}

func isJSIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isJSIdentByte(c byte) bool { return isJSIdentStart(c) || (c >= '0' && c <= '9') }
