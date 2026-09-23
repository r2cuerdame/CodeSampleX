package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"
)

// schemaValidator is the subset of JSON Schema draft 2020-12 the files in
// schemas/ use, enough to validate a Go fixture against the published
// contract at every depth (#345). It deliberately fails on a keyword it does
// not implement, so a schema cannot quietly stop being checked by growing a
// new one.
//
// A key-presence comparison against the top-level properties map was all
// the fixture test did before, and it let four schema breaks through CI
// (#324, #326, #327, #337): nested objects, patterns and enums were never
// looked at.
type schemaValidator struct {
	t     *testing.T
	files map[string]map[string]any
}

func newSchemaValidator(t *testing.T) *schemaValidator {
	return &schemaValidator{t: t, files: map[string]map[string]any{}}
}

func (v *schemaValidator) load(path string) map[string]any {
	v.t.Helper()
	path = filepath.Clean(path)
	if s, ok := v.files[path]; ok {
		return s
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		v.t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	var s map[string]any
	if err := json.Unmarshal(raw, &s); err != nil {
		v.t.Fatalf("%s: not valid JSON: %v", path, err)
	}
	v.files[path] = s
	return s
}

// validateGo marshals a Go value and validates the JSON it produces.
func (v *schemaValidator) validateGo(schemaPath string, value any) []string {
	v.t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		v.t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		v.t.Fatal(err)
	}
	return v.validate(schemaPath, doc)
}

func (v *schemaValidator) validate(schemaPath string, doc any) []string {
	var errs []string
	v.check(schemaPath, v.load(schemaPath), doc, "$", &errs)
	return errs
}

// handled lists every keyword check understands or deliberately ignores.
var handledSchemaKeywords = map[string]bool{
	"$schema": true, "$id": true, "$defs": true, "$ref": true,
	"title": true, "description": true, "examples": true, "default": true, "format": true,
	"type": true, "const": true, "enum": true, "pattern": true,
	"minLength": true, "maxLength": true, "minimum": true, "maximum": true,
	"properties": true, "required": true, "additionalProperties": true,
	"items": true, "minItems": true, "maxItems": true, "uniqueItems": true,
	"allOf": true, "if": true, "then": true, "else": true,
}

func (v *schemaValidator) check(file string, schema map[string]any, doc any, at string, errs *[]string) {
	fail := func(format string, args ...any) {
		*errs = append(*errs, at+": "+fmt.Sprintf(format, args...))
	}
	for key := range schema {
		if !handledSchemaKeywords[key] {
			v.t.Fatalf("%s: schema keyword %q at %s is not implemented by the test validator", file, key, at)
		}
	}
	if ref, ok := schema["$ref"].(string); ok {
		target, fragment, _ := strings.Cut(ref, "#")
		refFile := file
		if target != "" {
			refFile = filepath.Join(filepath.Dir(file), filepath.FromSlash(target))
		}
		resolved := v.load(refFile)
		if fragment != "" {
			var node any = resolved
			for _, part := range strings.Split(strings.TrimPrefix(fragment, "/"), "/") {
				m, _ := node.(map[string]any)
				node = m[part]
			}
			m, ok := node.(map[string]any)
			if !ok {
				v.t.Fatalf("%s: unresolvable $ref %q", file, ref)
			}
			resolved = m
		}
		v.check(refFile, resolved, doc, at, errs)
	}
	if typ, ok := schema["type"].(string); ok && !jsonTypeIs(doc, typ) {
		fail("want type %s, got %T", typ, doc)
		return
	}
	if want, ok := schema["const"]; ok && !reflect.DeepEqual(doc, want) {
		fail("want const %v, got %v", want, doc)
	}
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if reflect.DeepEqual(doc, e) {
				found = true
				break
			}
		}
		if !found {
			fail("%v is not one of %v", doc, enum)
		}
	}
	switch d := doc.(type) {
	case string:
		if p, ok := schema["pattern"].(string); ok {
			if !compileSchemaPattern(v.t, p).MatchString(d) {
				fail("%q does not match %s", d, p)
			}
		}
		n := float64(utf8.RuneCountInString(d))
		if m, ok := schema["minLength"].(float64); ok && n < m {
			fail("length %v < minLength %v", n, m)
		}
		if m, ok := schema["maxLength"].(float64); ok && n > m {
			fail("length %v > maxLength %v", n, m)
		}
	case float64:
		if m, ok := schema["minimum"].(float64); ok && d < m {
			fail("%v < minimum %v", d, m)
		}
		if m, ok := schema["maximum"].(float64); ok && d > m {
			fail("%v > maximum %v", d, m)
		}
	case []any:
		if m, ok := schema["minItems"].(float64); ok && float64(len(d)) < m {
			fail("%d items < minItems %v", len(d), m)
		}
		if m, ok := schema["maxItems"].(float64); ok && float64(len(d)) > m {
			fail("%d items > maxItems %v", len(d), m)
		}
		if u, _ := schema["uniqueItems"].(bool); u {
			for i := range d {
				for j := i + 1; j < len(d); j++ {
					if reflect.DeepEqual(d[i], d[j]) {
						fail("items %d and %d are equal under uniqueItems", i, j)
					}
				}
			}
		}
		if items, ok := schema["items"].(map[string]any); ok {
			for i, item := range d {
				v.check(file, items, item, fmt.Sprintf("%s[%d]", at, i), errs)
			}
		}
	case map[string]any:
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if _, present := d[r.(string)]; !present {
					fail("required key %q missing", r)
				}
			}
		}
		props, _ := schema["properties"].(map[string]any)
		keys := make([]string, 0, len(d))
		for k := range d {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if sub, ok := props[k].(map[string]any); ok {
				v.check(file, sub, d[k], at+"."+k, errs)
				continue
			}
			switch additional := schema["additionalProperties"].(type) {
			case bool:
				if !additional {
					fail("key %q rejected by additionalProperties:false", k)
				}
			case map[string]any:
				v.check(file, additional, d[k], at+"."+k, errs)
			}
		}
	}
	if all, ok := schema["allOf"].([]any); ok {
		for _, sub := range all {
			v.check(file, sub.(map[string]any), doc, at, errs)
		}
	}
	if cond, ok := schema["if"].(map[string]any); ok {
		var condErrs []string
		v.check(file, cond, doc, at, &condErrs)
		branch := "then"
		if len(condErrs) > 0 {
			branch = "else"
		}
		if sub, ok := schema[branch].(map[string]any); ok {
			v.check(file, sub, doc, at, errs)
		}
	}
}

func jsonTypeIs(doc any, typ string) bool {
	switch typ {
	case "object":
		_, ok := doc.(map[string]any)
		return ok
	case "array":
		_, ok := doc.([]any)
		return ok
	case "string":
		_, ok := doc.(string)
		return ok
	case "boolean":
		_, ok := doc.(bool)
		return ok
	case "number":
		_, ok := doc.(float64)
		return ok
	case "integer":
		f, ok := doc.(float64)
		return ok && f == float64(int64(f))
	case "null":
		return doc == nil
	}
	return false
}

// JSON Schema patterns are ECMA-262; the only construct the schemas use that
// RE2 spells differently is the \uXXXX escape.
var schemaUnicodeEscape = regexp.MustCompile(`\\u([0-9A-Fa-f]{4})`)

func compileSchemaPattern(t *testing.T, p string) *regexp.Regexp {
	t.Helper()
	re, err := regexp.Compile(schemaUnicodeEscape.ReplaceAllString(p, `\x{$1}`))
	if err != nil {
		t.Fatalf("schema pattern %q: %v", p, err)
	}
	return re
}
