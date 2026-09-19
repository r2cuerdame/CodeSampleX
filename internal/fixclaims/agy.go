package fixclaims

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The AGY lane (#444 §2) is the cheap, high-volume reasoning step between
// the collector and the queue: it reads a noisy release-note line with its
// linked issue or PR and produces one FIX_CANDIDATE record, or declines.
//
// Two rules make its output safe to ingest without trusting it. The output
// is schema-bounded: ParseAGYOutput accepts exactly one Candidate document,
// refuses unknown fields, and hands it to the same Validate every other
// producer goes through. And the schema has no status: there is no field an
// AGY answer could set that would move a claim past CLAIMED_FIX.

// AGYDecline is the JSON an AGY lane returns instead of a candidate when the
// source describes nothing a contract can run.
type AGYDecline struct {
	Decline bool   `json:"decline"`
	Reason  string `json:"reason"`
}

// AGYInput is what the lane is given: the collector's seed and the source
// text it may read. The text is provenance handed to a model; nothing in
// it is stored except what comes back through the validator.
type AGYInput struct {
	Source     Source   `json:"source"`
	Release    string   `json:"release"`
	Previous   string   `json:"previousRelease,omitempty"`
	ReleaseURL string   `json:"releaseUrl"`
	Line       string   `json:"line"`
	References []string `json:"references,omitempty"`
	// SourceText is the linked issue or PR body, bounded by the caller.
	SourceText string `json:"sourceText,omitempty"`
}

// AGYPrompt renders the instruction an AGY lane is run with. It is fixed
// text plus the input document, so two lanes on two machines ask the same
// question.
func AGYPrompt(in AGYInput) string {
	doc, _ := json.MarshalIndent(in, "", "  ")
	var b strings.Builder
	b.WriteString("You are the fix-claim extractor for CodeSampleX. Read the release-note line and the linked source and decide whether it describes ONE executable bug fix: a behaviour a small program could observe failing on the previous release and passing on this one.\n\n")
	b.WriteString("Output exactly one JSON document and nothing else.\n\n")
	b.WriteString("If it is executable, output a FIX_CANDIDATE with these fields and no others:\n")
	b.WriteString(`{"schemaVersion":1,"ecosystem":"...","name":"...","claimedBadVersion":"...","claimedFixedVersion":"...","claim":"one sentence, what failed and how","sourceUrl":"...","sourceType":"release_note|issue|pr|changelog","references":["..."],"symbols":["the API or feature"],"environmentHints":["windows","python-3.14"],"confidence":"high|medium|low","upstreamReproducer":false}` + "\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- claimedFixedVersion is the release the line appears in. claimedBadVersion is the previous release unless the source names the release that introduced the bug.\n")
	b.WriteString("- symbols name the API the reproducer would call. environmentHints are lowercase tokens only when the source ties the bug to an OS or runtime.\n")
	b.WriteString("- upstreamReproducer is true only when the linked issue or PR contains code that reproduces the bug.\n")
	b.WriteString("- Never include a status, a verdict, or any claim that the fix was verified. You are producing a test hypothesis, not evidence.\n")
	b.WriteString("- If the line is documentation, formatting, tooling, a dependency bump, or a change nothing could assert, output " + `{"decline":true,"reason":"..."}` + " instead.\n\n")
	b.WriteString("Input:\n")
	b.Write(doc)
	b.WriteString("\n")
	return b.String()
}

// ErrAGYDeclined is returned by ParseAGYOutput for a decline document.
var ErrAGYDeclined = errors.New("fixclaims: agy declined the source")

// maxAGYOutput bounds one answer; a candidate is a few hundred bytes.
const maxAGYOutput = 16 << 10

// ParseAGYOutput reads one AGY answer. It accepts either a decline or one
// Candidate document with no unknown fields, then validates the candidate.
// A document carrying any field outside the schema -- a status, a
// verdict, a note -- is refused whole, so a lane cannot smuggle a
// conclusion through a field the validator does not read.
func ParseAGYOutput(raw []byte) (Candidate, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > maxAGYOutput {
		return Candidate{}, fmt.Errorf("fixclaims: agy output exceeds %d bytes", maxAGYOutput)
	}
	// Tolerate a fenced code block, which models add however firmly told
	// not to; nothing else around the document is accepted.
	if bytes.HasPrefix(raw, []byte("```")) {
		raw = bytes.TrimPrefix(raw, []byte("```json"))
		raw = bytes.TrimPrefix(raw, []byte("```"))
		raw = bytes.TrimSuffix(bytes.TrimSpace(raw), []byte("```"))
		raw = bytes.TrimSpace(raw)
	}
	var probe struct {
		Decline bool   `json:"decline"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return Candidate{}, fmt.Errorf("fixclaims: agy output is not a JSON object: %w", err)
	}
	if probe.Decline {
		return Candidate{}, fmt.Errorf("%w: %s", ErrAGYDeclined, strings.TrimSpace(probe.Reason))
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var c Candidate
	if err := dec.Decode(&c); err != nil {
		return Candidate{}, fmt.Errorf("fixclaims: agy output outside the candidate schema: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Candidate{}, errors.New("fixclaims: agy output holds more than one document")
	}
	normalized, rejections := Validate(c)
	if len(rejections) > 0 {
		return normalized, rejections[0]
	}
	return normalized, nil
}
