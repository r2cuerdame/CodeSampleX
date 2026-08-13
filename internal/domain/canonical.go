package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// CanonicalJSON renders v as deterministic JSON: object keys sorted,
// no insignificant whitespace, numbers preserved verbatim, no trailing
// newline. All content hashes and signatures in CodeSampleX are computed
// over this form.
func CanonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := canonEncode(&buf, tree); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// MustCanonicalJSON panics on marshal failure; for types known to marshal.
func MustCanonicalJSON(v any) []byte {
	b, err := CanonicalJSON(v)
	if err != nil {
		panic(fmt.Sprintf("domain: canonical json: %v", err))
	}
	return b
}

func canonEncode(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return err
			}
			buf.Write(kb)
			buf.WriteByte(':')
			if err := canonEncode(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := canonEncode(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case json.Number:
		buf.WriteString(t.String())
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return err
		}
		buf.Write(b)
	}
	return nil
}
