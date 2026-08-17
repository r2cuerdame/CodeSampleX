// Package relevance provides the provenance-aware lexical gate shared by
// local and server search. It deliberately distinguishes full identifiers
// from their component words: a complete declared symbol is authoritative,
// while generic pieces such as "server" or "json" are not.
package relevance

import "strings"

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true,
	"how": true, "why": true, "what": true, "when": true, "does": true,
	"can": true, "you": true, "your": true, "this": true, "that": true,
	"into": true, "out": true, "not": true, "but": true, "get": true,
	"use": true, "using": true, "make": true, "want": true, "need": true,
	"there": true, "here": true, "some": true, "any": true, "all": true,
}

// genericWords occur inside package and symbol names without identifying a
// subject. Keep one list for both provenance classes so a token cannot become
// strong merely because it moved from a package name to a symbol.
var genericWords = map[string]bool{
	"python": true, "python3": true, "node": true, "nodejs": true,
	"javascript": true, "typescript": true, "golang": true, "rust": true,
	"ruby": true, "php": true, "java": true, "kotlin": true, "swift": true,
	"dart": true, "flutter": true, "elixir": true, "erlang": true,
	"deno": true, "bun": true, "dotnet": true, "csharp": true, "perl": true,
	"scala": true, "haskell": true,

	"lib": true, "libs": true, "core": true, "util": true, "utils": true,
	"common": true, "api": true, "sdk": true, "client": true, "server": true,
	"tools": true, "toolkit": true, "plugin": true, "plugins": true,
	"package": true, "packages": true, "module": true, "modules": true,
	"helper": true, "helpers": true,

	// These are especially common protocol/runtime nouns. Treating symbol
	// subtokens as identifiers made JSON-RPC/process.stdin questions match
	// unrelated model, JSON, server, and process APIs.
	"json": true, "model": true, "models": true, "process": true,
	"protocol": true, "protocols": true,
}

// ContentTokens reduces prose to the words that carry its topic.
func ContentTokens(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(f) < 3 || stopWords[f] {
			continue
		}
		out = append(out, f)
	}
	return out
}

// IsGeneric reports whether a package/symbol subtoken lacks identity value.
// It is exported so regression tests can pin the single shared vocabulary.
func IsGeneric(token string) bool { return genericWords[strings.ToLower(token)] }

// NameTokens retains a full package name and only its non-generic pieces.
func NameTokens(name string) []string {
	name = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "@")))
	if name == "" {
		return nil
	}
	out := []string{name}
	for _, token := range ContentTokens(name) {
		if !IsGeneric(token) {
			out = append(out, token)
		}
	}
	return out
}

// ContainsIdentifier matches a complete identifier with alphanumeric
// boundaries. Punctuation inside a dotted/slashed identifier is preserved.
func ContainsIdentifier(text, identifier string) bool {
	text = strings.ToLower(text)
	identifier = strings.ToLower(strings.TrimSpace(identifier))
	if identifier == "" {
		return false
	}
	for offset := 0; offset < len(text); {
		at := strings.Index(text[offset:], identifier)
		if at < 0 {
			return false
		}
		start := offset + at
		end := start + len(identifier)
		if !alnumAt(text, start-1) && !alnumAt(text, end) {
			return true
		}
		offset = start + 1
	}
	return false
}

func alnumAt(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	r := s[i]
	return r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
}

// MatchedDeclaredSymbols returns the actual declared identities matched by
// the request, in request order. Returning the declared spelling (rather than
// req[0]) lets evidence lookup use the symbol that really passed the gate.
// Full identities compare case-insensitively; subtokens never satisfy it.
func MatchedDeclaredSymbols(requested, declared []string) []string {
	if len(requested) == 0 {
		return nil
	}
	have := make(map[string]string, len(declared))
	for _, symbol := range declared {
		actual := strings.TrimSpace(symbol)
		if key := strings.ToLower(actual); key != "" {
			if _, exists := have[key]; !exists {
				have[key] = actual
			}
		}
	}
	seen := make(map[string]bool, len(requested))
	var matched []string
	for _, symbol := range requested {
		key := strings.ToLower(strings.TrimSpace(symbol))
		if actual := have[key]; actual != "" && !seen[key] {
			matched = append(matched, actual)
			seen[key] = true
		}
	}
	return matched
}

// MatchesDeclaredSymbols reports whether any full requested identity matched.
func MatchesDeclaredSymbols(requested, declared []string) bool {
	return len(MatchedDeclaredSymbols(requested, declared)) > 0
}

// Signal counts strong identifier and weaker goal-prose overlap. Full symbol
// identity is retained even when every one of its subtokens is generic.
func Signal(query, goal string, packageNames, symbols []string) (strong, prose int) {
	strongSet := map[string]bool{}
	proseSet := map[string]bool{}
	for _, token := range ContentTokens(goal) {
		proseSet[token] = true
	}
	for _, name := range packageNames {
		for _, token := range NameTokens(name) {
			strongSet[token] = true
		}
	}
	for _, symbol := range symbols {
		for _, token := range ContentTokens(symbol) {
			if !IsGeneric(token) {
				strongSet[token] = true
			}
		}
		if ContainsIdentifier(query, symbol) {
			strong++
		}
	}
	for _, name := range packageNames {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" && !IsGeneric(name) && ContainsIdentifier(query, name) {
			strong++
		}
	}
	for _, token := range ContentTokens(query) {
		switch {
		case strongSet[token]:
			strong++
		case proseSet[token]:
			prose++
		}
	}
	return strong, prose
}

// AboutSameThing applies the shared local/server topic gate.
func AboutSameThing(query, goal string, packageNames, symbols []string) bool {
	strong, prose := Signal(query, goal, packageNames, symbols)
	if strong > 0 {
		return true
	}
	if len(ContentTokens(query)) >= 4 {
		return prose >= 2
	}
	return prose >= 1
}
