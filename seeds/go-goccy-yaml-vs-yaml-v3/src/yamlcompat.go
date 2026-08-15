// Package sample pins the ways github.com/goccy/go-yaml differs from
// gopkg.in/yaml.v3 at runtime.
//
// Both modules declare package "yaml" and both export Marshal(v) ([]byte, error)
// and Unmarshal(data, v) error with identical signatures. Swapping the import
// line therefore compiles cleanly and reviews as a no-op. It is not a no-op:
// gopkg.in/yaml.v3 has been frozen at v3.0.1 since 2022 and goccy is where
// projects migrate, so this exact swap is common — and every difference below is
// silent, with no compile error and no panic to point at it.
package sample

import (
	"errors"
	"strings"

	goccy "github.com/goccy/go-yaml"
	v3 "gopkg.in/yaml.v3"
)

// Config is deliberately untagged. Key derivation really is the same in both
// libraries — the exported field name lowercased, so UserName becomes "username".
// That shared behaviour is what makes the differences further down convincing.
type Config struct {
	UserName string
	MaxRetry int
	Items    []string
}

// EncodeBoth marshals one value with each library and returns both documents.
//
// TRAP: the natural assumption is that two libraries implementing the same spec
// emit the same bytes. They do not. gopkg.in/yaml.v3 indents four spaces and
// indents a sequence under its key; goccy indents two and leaves the dashes flush
// with the key. Nothing warns you — the YAML is equivalent, so a round-trip test
// still passes. What breaks is anything that compares the serialized form: golden
// files, config checksums, "did this change?" diffs, signed manifests.
func EncodeBoth(v any) (goccyOut, v3Out []byte, err error) {
	goccyOut, err = goccy.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	v3Out, err = v3.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	return goccyOut, v3Out, nil
}

// EncodeGoccyLikeV3 makes goccy reproduce gopkg.in/yaml.v3's default layout byte
// for byte, which is what a migration actually needs.
//
// Indent(4) on its own is NOT enough. Without IndentSequence(true) the sequence
// dashes stay flush with their key and the output still differs from yaml.v3 —
// the two options fix two independent parts of the layout.
func EncodeGoccyLikeV3(v any) ([]byte, error) {
	return goccy.MarshalWithOptions(v, goccy.Indent(4), goccy.IndentSequence(true))
}

// DecodeDynamicInts decodes a document into map[string]any with each library.
//
// TRAP: gopkg.in/yaml.v3 stores every integer as Go `int`. goccy stores a
// non-negative integer as `uint64` and a negative one as `int64`. Code carried
// over from yaml.v3 that switches on `case int:` matches nothing at all and falls
// through to its default branch. Worse, the obvious one-line fix — changing it to
// `case uint64:` — still silently misses every negative number, because the
// concrete type in goccy depends on the SIGN of the value, not on the schema.
//
// Both libraries agree that a nested mapping decodes to map[string]any; that part
// of the yaml.v2 folklore (map[interface{}]interface{}) applies to neither.
func DecodeDynamicInts(doc []byte) (goccyVals, v3Vals map[string]any, err error) {
	if err = goccy.Unmarshal(doc, &goccyVals); err != nil {
		return nil, nil, err
	}
	if err = v3.Unmarshal(doc, &v3Vals); err != nil {
		return nil, nil, err
	}
	return goccyVals, v3Vals, nil
}

// TypeErrorForms returns the two renderings of one goccy decode error: whatever
// err.Error() gives you, and the one-line form.
//
// TRAP: goccy's err.Error() is not a one-line message. It ALREADY contains an
// annotated excerpt of the source document — the offending line, a ">" marker and
// a caret under the column. So the ordinary log.Printf("%v", err) that was fine
// with yaml.v3 now writes a small ASCII diagram into a structured log field.
//
// FormatError(err, colored, inclSource) is the control, and it runs the opposite
// way round from what the name suggests: it does not ADD the excerpt when
// inclSource is true, it REMOVES it when inclSource is false. Passing false is
// how you get the single-line message back.
func TypeErrorForms(doc []byte, into any) (raw, oneLine string, ok bool) {
	err := goccy.Unmarshal(doc, into)
	if err == nil {
		return "", "", false
	}
	return err.Error(), goccy.FormatError(err, false, false), true
}

// DecodeDuplicateKey decodes a document that defines the same mapping key twice,
// once with goccy's defaults and once opting back out.
//
// Duplicate keys are a hard error by default, which matches yaml.v3 — that much
// is not surprising.
//
// TRAP: goccy exports a type named yaml.DuplicateKeyError, so the obvious way to
// recognise this case is errors.As(err, &dupErr). That never fires. The default
// decode path reports the duplicate as a generic syntax error, and the exported
// DuplicateKeyError is produced elsewhere. Match on behaviour, not on that type.
//
// AllowDuplicateMapKey() downgrades it to plain last-key-wins.
func DecodeDuplicateKey(doc []byte) (defaultErr error, isDuplicateKeyError bool, lastWins map[string]int, allowErr error) {
	var strictTarget map[string]int
	defaultErr = goccy.Unmarshal(doc, &strictTarget)

	var dup *goccy.DuplicateKeyError
	isDuplicateKeyError = errors.As(defaultErr, &dup)

	allowErr = goccy.UnmarshalWithOptions(doc, &lastWins, goccy.AllowDuplicateMapKey())
	return defaultErr, isDuplicateKeyError, lastWins, allowErr
}

type creds struct {
	Token string `yaml:"token"`
}

// pointerDoc uses the bare anchor/alias flags on POINTER fields. With no explicit
// name the anchor is named after the field ("base"), and the alias field is
// resolved by pointer identity.
type pointerDoc struct {
	Base  *creds `yaml:"base,anchor"`
	Child *creds `yaml:"child,alias"`
}

// valueDoc names the anchor and alias explicitly on VALUE fields. This is the
// form most people reach for first, and it is the one that does not work.
type valueDoc struct {
	Base  creds `yaml:"base,anchor=b"`
	Child creds `yaml:"child,alias=b"`
}

// MarshalAnchorAlias emits the same logical document twice: once with pointer
// fields and bare tags, once with value fields and explicit anchor=/alias= names.
//
// TRAP: only the pointer form emits an alias. goccy decides that Child aliases
// Base by comparing POINTER ADDRESSES, not by matching the alias= name against
// the anchor= name. Give it value-typed fields and the explicit alias=b is
// silently ignored: the child is written out in full, expanded, with err == nil.
// A shared secret you expected to appear once as an anchor is now duplicated in
// the output, and nothing failed to tell you.
//
// gopkg.in/yaml.v3 cannot emit anchors from struct tags at all, so this whole
// feature is one an agent tends to assume works the same way in both.
func MarshalAnchorAlias() (pointerForm, valueForm []byte, err error) {
	shared := &creds{Token: "s3cret"}
	pointerForm, err = goccy.Marshal(pointerDoc{Base: shared, Child: shared})
	if err != nil {
		return nil, nil, err
	}
	valueForm, err = goccy.Marshal(valueDoc{
		Base:  creds{Token: "s3cret"},
		Child: creds{Token: "s3cret"},
	})
	if err != nil {
		return nil, nil, err
	}
	return pointerForm, valueForm, nil
}

// CountOccurrences is a small helper the contract uses to show that the value
// form really did duplicate the payload instead of aliasing it.
func CountOccurrences(doc []byte, needle string) int {
	return strings.Count(string(doc), needle)
}
