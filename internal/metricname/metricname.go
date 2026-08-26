// Package metricname holds the measured-vs-estimated naming rule for the
// stats documents this product publishes, as code rather than as prose.
//
// The failure it prevents is one specific misreading: a count of records
// presented under a name that makes it a count of people. Every number in
// these documents is a count of observation rows, of anonymous rotating
// buckets, or of reports — and none of them is a count of users, installs or
// machines, because no defensible method for any of those exists here
// (docs/activation-funnel.md §6, and the sentence the README already carries:
// unique/active users, live MCP processes and successful installs are not
// measured).
//
// A caveat printed beside a number does not survive being quoted. A field
// name does. So the rule is applied to names.
package metricname

import (
	"reflect"
	"strings"
)

// Rule identifiers, so a failing test says which rule and not only which
// field.
const (
	RuleForbiddenActor     = "forbidden-actor-noun"
	RuleUndeclaredBucket   = "undeclared-bucket-noun"
	RuleUnlabelledEstimate = "unlabelled-estimate"
)

// Violation is one field that breaks the rule.
type Violation struct {
	// Field is the JSON path, so a nested document reports where it is.
	Field string
	Rule  string
	Why   string
}

// forbiddenActors are the nouns that name a person, a machine, an install or
// a session. There is no override: the point is not that the words are
// imprecise, it is that nothing behind them was measured.
var forbiddenActors = map[string]string{
	"user": "", "users": "",
	// "actives" the noun, not "active" the adjective: the offending word in
	// activeUsers is users, and forbidding the adjective would refuse an
	// honest activeJobs on some future document for no reason.
	"actives": "",
	"dau":     "", "mau": "",
	"install": "", "installs": "", "installation": "", "installations": "",
	"device": "", "devices": "",
	"machine": "", "machines": "",
	"person": "", "persons": "", "people": "",
	"account": "", "accounts": "",
	"seat": "", "seats": "",
	"subscriber": "", "subscribers": "",
	"visitor": "", "visitors": "",
	"session": "", "sessions": "",
	"developer": "", "developers": "",
}

// bucketTokens name things that are counted in rotating anonymous buckets.
// They are not forbidden — they are the honest unit — but every spelling has
// to be declared in BucketNouns, because the declaration is the only place
// the "not a head count" note lives.
var bucketTokens = map[string]struct{}{
	"peer": {}, "peers": {},
	"project": {}, "projects": {},
}

// BucketNouns is the deliberate allowlist: JSON field name → what one unit
// actually is. Adding an entry is how a reviewer is made to say it.
var BucketNouns = map[string]string{
	"peers": "today's distinct daily anonymous buckets; they rotate at UTC " +
		"midnight, so this cannot be summed over time and is never a head count",
	"projectsMonth": "distinct monthly project buckets; the path is HMAC input " +
		"only and is not recoverable, and one person's three checkouts are three",
}

// Check reports every naming violation in the struct type of doc.
//
// Only fields a consumer can actually read are checked: unexported fields and
// json:"-" never reach the wire, so they make no claim to anyone.
func Check(doc any) []Violation {
	t := reflect.TypeOf(doc)
	var out []Violation
	walk(t, "", 0, map[reflect.Type]struct{}{}, &out)
	return out
}

func walk(t reflect.Type, prefix string, depth int, seen map[reflect.Type]struct{}, out *[]Violation) {
	t = jsonValueType(t)
	if t == nil || t.Kind() != reflect.Struct || depth > 4 {
		return
	}
	if _, ok := seen[t]; ok {
		return
	}
	seen[t] = struct{}{}
	defer delete(seen, t)

	names := jsonNames(t)
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, ok := jsonName(f)
		if !ok {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		checkName(name, path, out)
		checkEstimateLabel(f, name, path, names, out)
		walk(f.Type, path, depth+1, seen, out)
	}
}

func checkName(name, path string, out *[]Violation) {
	for _, tok := range tokens(name) {
		if _, bad := forbiddenActors[tok]; bad {
			*out = append(*out, Violation{
				Field: path,
				Rule:  RuleForbiddenActor,
				Why: "names an actor (" + tok + "); this document counts records and " +
					"rotating anonymous buckets, and no method behind that noun exists here",
			})
			return
		}
		if _, bucket := bucketTokens[tok]; bucket {
			if _, declared := BucketNouns[name]; !declared {
				*out = append(*out, Violation{
					Field: path,
					Rule:  RuleUndeclaredBucket,
					Why: "counts anonymous buckets (" + tok + ") without a declaration; " +
						"add it to metricname.BucketNouns with what one unit is",
				})
				return
			}
		}
	}
}

// checkEstimateLabel holds the other half: an estimate a consumer cannot tell
// apart from a measurement. The label may sit on the field's own type (an
// EstimatedStat carries it) or beside it in the same struct, which is how the
// simple consumers read it.
func checkEstimateLabel(f reflect.StructField, name, path string, siblings map[string]reflect.Type, out *[]Violation) {
	lower := strings.ToLower(name)
	if lower == "estimated" || !strings.HasPrefix(lower, "estimated") {
		return
	}
	if carriesEstimatedFlag(f.Type) {
		return
	}
	if typ, ok := siblings["estimated"]; ok && typ.Kind() == reflect.Bool {
		return
	}
	*out = append(*out, Violation{
		Field: path,
		Rule:  RuleUnlabelledEstimate,
		Why: "is an estimate with no estimated flag a consumer can read; carry one " +
			"on its own type or beside it, with the formula and the assumptions",
	})
}

func carriesEstimatedFlag(t reflect.Type) bool {
	t = jsonValueType(t)
	if t == nil || t.Kind() != reflect.Struct {
		return false
	}
	typ, ok := jsonNames(t)["estimated"]
	return ok && typ.Kind() == reflect.Bool
}

func jsonNames(t reflect.Type) map[string]reflect.Type {
	names := make(map[string]reflect.Type, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		if name, ok := jsonName(t.Field(i)); ok {
			names[strings.ToLower(name)] = t.Field(i).Type
		}
	}
	return names
}

// jsonName is the name a consumer sees, or ok=false when the field never
// reaches one.
func jsonName(f reflect.StructField) (string, bool) {
	if f.PkgPath != "" {
		return "", false
	}
	tag, tagged := f.Tag.Lookup("json")
	if !tagged {
		return f.Name, true
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "-" {
		return "", false
	}
	if name == "" {
		return f.Name, true
	}
	return name, true
}

// jsonValueType unwraps the containers whose elements or values are encoded
// beneath the field name. A public claim inside []T, [N]T, map[string]T, or
// any pointer-wrapped combination is still visible to a JSON consumer and
// must be checked just like a directly nested struct.
func jsonValueType(t reflect.Type) reflect.Type {
	for t != nil {
		switch t.Kind() {
		case reflect.Ptr, reflect.Slice, reflect.Array, reflect.Map:
			t = t.Elem()
		default:
			return t
		}
	}
	return nil
}

// tokens splits a camelCase or PascalCase field name into lowercase words.
// Acronym runs break where the run meets a following word, so MCPSessions is
// mcp + sessions rather than one token nothing matches.
func tokens(name string) []string {
	runes := []rune(name)
	var out []string
	start := 0
	for i := 1; i < len(runes); i++ {
		prev, cur := runes[i-1], runes[i]
		boundary := false
		switch {
		case isUpper(cur) && !isUpper(prev):
			boundary = true
		case isUpper(prev) && isUpper(cur) && i+1 < len(runes) && isLower(runes[i+1]):
			boundary = true
		case !isLetter(cur) != !isLetter(prev):
			boundary = true
		}
		if boundary {
			if word := clean(runes[start:i]); word != "" {
				out = append(out, word)
			}
			start = i
		}
	}
	if word := clean(runes[start:]); word != "" {
		out = append(out, word)
	}
	return out
}

func clean(r []rune) string {
	var b strings.Builder
	for _, c := range r {
		if isLetter(c) {
			b.WriteRune(toLower(c))
		}
	}
	return b.String()
}

func isUpper(r rune) bool  { return r >= 'A' && r <= 'Z' }
func isLower(r rune) bool  { return r >= 'a' && r <= 'z' }
func isLetter(r rune) bool { return isUpper(r) || isLower(r) }
func toLower(r rune) rune {
	if isUpper(r) {
		return r + ('a' - 'A')
	}
	return r
}
