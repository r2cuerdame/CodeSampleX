package main

import (
	"fmt"
	"os"
	"strings"

	sample "codesamplex.dev/sample/gogoccyyaml/src"
)

func fail(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
	os.Exit(1)
}

func main() {
	cfg := sample.Config{UserName: "ada", MaxRetry: 3, Items: []string{"a", "b"}}

	// 1. The two libraries do NOT emit the same bytes for the same value.
	goccyOut, v3Out, err := sample.EncodeBoth(cfg)
	if err != nil {
		fail("EncodeBoth: %v", err)
	}
	if string(goccyOut) == string(v3Out) {
		fail("expected goccy and yaml.v3 to disagree on layout, both produced:\n%s", goccyOut)
	}
	// goccy: two-space indent, sequence dashes flush with the key.
	const wantGoccy = "username: ada\nmaxretry: 3\nitems:\n- a\n- b\n"
	if string(goccyOut) != wantGoccy {
		fail("goccy default layout changed\n got: %q\nwant: %q", goccyOut, wantGoccy)
	}
	// yaml.v3: four-space indent, sequence indented under the key.
	const wantV3 = "username: ada\nmaxretry: 3\nitems:\n    - a\n    - b\n"
	if string(v3Out) != wantV3 {
		fail("yaml.v3 default layout changed\n got: %q\nwant: %q", v3Out, wantV3)
	}

	// 2. Indent(4) alone would still differ; it takes IndentSequence(true) too.
	matched, err := sample.EncodeGoccyLikeV3(cfg)
	if err != nil {
		fail("EncodeGoccyLikeV3: %v", err)
	}
	if string(matched) != string(v3Out) {
		fail("Indent(4)+IndentSequence(true) should reproduce yaml.v3 exactly\n got: %q\nwant: %q", matched, v3Out)
	}

	// 3. Integers decoded into any: goccy splits on sign, yaml.v3 always gives int.
	doc := []byte("pos: 7\nneg: -7\nnested:\n  k: 1\n")
	gv, vv, err := sample.DecodeDynamicInts(doc)
	if err != nil {
		fail("DecodeDynamicInts: %v", err)
	}
	if _, isUint := gv["pos"].(uint64); !isUint {
		fail("goccy positive int should decode as uint64, got %T", gv["pos"])
	}
	if _, isInt64 := gv["neg"].(int64); !isInt64 {
		fail("goccy negative int should decode as int64, got %T", gv["neg"])
	}
	// The yaml.v3 habit of switching on `case int:` matches neither of them.
	if _, isInt := gv["pos"].(int); isInt {
		fail("goccy should NOT decode a positive int as plain int")
	}
	if _, isInt := vv["pos"].(int); !isInt {
		fail("yaml.v3 positive int should decode as int, got %T", vv["pos"])
	}
	if _, isInt := vv["neg"].(int); !isInt {
		fail("yaml.v3 negative int should decode as int, got %T", vv["neg"])
	}
	// Both agree nested mappings are map[string]any, not map[interface{}]interface{}.
	if _, ok := gv["nested"].(map[string]any); !ok {
		fail("goccy nested mapping should be map[string]any, got %T", gv["nested"])
	}
	if _, ok := vv["nested"].(map[string]any); !ok {
		fail("yaml.v3 nested mapping should be map[string]any, got %T", vv["nested"])
	}

	// 4. err.Error() already carries the annotated source; FormatError strips it.
	var into sample.Config
	raw, oneLine, ok := sample.TypeErrorForms([]byte("maxretry: notanint\n"), &into)
	if !ok {
		fail("expected a decode error for a string in an int field")
	}
	if !strings.Contains(raw, "^") {
		fail("goccy err.Error() should already include the caret-annotated source, got %q", raw)
	}
	if strings.Count(strings.TrimRight(raw, "\n"), "\n") < 1 {
		fail("goccy err.Error() should span several lines, got %q", raw)
	}
	if strings.Contains(oneLine, "\n") || strings.Contains(oneLine, "^") {
		fail("FormatError(err, false, false) should strip the source excerpt, got %q", oneLine)
	}
	// The one-line form is exactly the head of the raw form.
	if !strings.HasPrefix(raw, oneLine) {
		fail("one-line form should be the prefix of the raw form\n raw: %q\nline: %q", raw, oneLine)
	}
	if !strings.Contains(oneLine, "[1:11]") {
		fail("goccy error should carry line:column, got %q", oneLine)
	}

	// 5. Duplicate keys error by default, but NOT as a *DuplicateKeyError.
	dupErr, isDupType, lastWins, allowErr := sample.DecodeDuplicateKey([]byte("k: 1\nk: 2\n"))
	if dupErr == nil {
		fail("goccy should reject a duplicated mapping key by default")
	}
	if isDupType {
		fail("errors.As(*yaml.DuplicateKeyError) is expected NOT to match the default decode error")
	}
	if allowErr != nil {
		fail("AllowDuplicateMapKey should accept the document: %v", allowErr)
	}
	if lastWins["k"] != 2 {
		fail("AllowDuplicateMapKey should keep the last value, got %d", lastWins["k"])
	}

	// 6. anchor/alias tags only alias through shared pointers.
	pointerForm, valueForm, err := sample.MarshalAnchorAlias()
	if err != nil {
		fail("MarshalAnchorAlias: %v", err)
	}
	if !strings.Contains(string(pointerForm), "&base") || !strings.Contains(string(pointerForm), "*base") {
		fail("pointer fields should emit an anchor and an alias, got:\n%s", pointerForm)
	}
	if sample.CountOccurrences(pointerForm, "s3cret") != 1 {
		fail("aliased document should carry the payload once, got:\n%s", pointerForm)
	}
	// The explicit alias=b on a value field is silently ignored, no error.
	if strings.Contains(string(valueForm), "*b") {
		fail("value fields were not expected to emit an alias, got:\n%s", valueForm)
	}
	if sample.CountOccurrences(valueForm, "s3cret") != 2 {
		fail("value form should have duplicated the payload, got:\n%s", valueForm)
	}

	fmt.Println("CONTRACT PASS: goccy/go-yaml is not a drop-in for gopkg.in/yaml.v3")
}
