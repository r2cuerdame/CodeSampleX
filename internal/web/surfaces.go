package web

import "strings"

// The three doors onto the network (#318).
//
// Plain HTTPS reads the network: any script, browser or cloud agent that can
// fetch a URL gets the same graded answer the CLI gets, with nothing
// installed. The CLI lets the network observe reality: it wraps the real
// build, sanitizes what happened on this machine, and is the only door that
// can publish. MCP is an adapter on top of the CLI -- `csx mcp` IS the csx
// binary -- so anything MCP can do, the CLI can, and an MCP client without
// csx installed has no server to talk to.
//
// The matrix below is the one place that comparison is written down. The
// features page renders it, and a test holds the nine READMEs to the same
// marks, so a cell cannot say yes on one page and no on another.

// surfaceMark is one cell: full, partial or none. Partial carries the
// condition, because a partial mark with no condition is a guess.
type surfaceMark struct {
	// Mark is "yes", "partial" or "no".
	Mark string
	// Note is the condition on a partial, or the reason on a no, in one
	// clause. Empty on a plain yes.
	Note string
}

// capabilityRow is one line of the matrix: what, and how each door does it.
type capabilityRow struct {
	Capability string
	REST       surfaceMark
	MCP        surfaceMark
	CLI        surfaceMark
}

func yes() surfaceMark                { return surfaceMark{Mark: "yes"} }
func yesIf(note string) surfaceMark   { return surfaceMark{Mark: "yes", Note: note} }
func partial(note string) surfaceMark { return surfaceMark{Mark: "partial", Note: note} }
func no() surfaceMark                 { return surfaceMark{Mark: "no"} }
func noBecause(note string) surfaceMark {
	return surfaceMark{Mark: "no", Note: note}
}

// mcpNeedsCSX is the fact the MCP column keeps repeating: the MCP server is
// the csx binary, so an MCP client without csx installed has no server to
// talk to. It is stated on the row where it decides the answer rather than
// once in a footnote a reader skips.
const mcpNeedsCSX = "the MCP server is the csx binary; the client host must have csx installed"

// capabilityMatrix is checked against the implementation, not against the
// wish list: every yes is a route or a command that exists today.
func capabilityMatrix() []capabilityRow {
	return []capabilityRow{
		{"Search verified samples, graded against an environment", yes(), yes(), yes()},
		{"Compatibility lookup by package, version, symbol and environment", yes(), yes(), partial("csx search grades against the synced shards; there is no explain command")},
		{"Read one sample's manifest, receipts and files", yes(), yes(), partial("through csx search --json; no dedicated sample read command")},
		{"Read the findings collection", yes(), noBecause("web and REST only"), noBecause("web and REST only")},
		{"Read gaps, wanted and public stats", yes(), no(), partial("csx stats shows local counters only")},
		{"Use from a browser, a cloud agent or a script", yes(), partial("the agent's MCP host must run csx locally"), noBecause("a local binary")},
		{"Zero-install use", yes(), partial(mcpNeedsCSX), no()},
		{"Detect the local project and environment automatically", no(), partial("only what the local csx behind the MCP host can see"), yes()},
		{"Run the real local build or test", no(), partial("run_observed_command runs it through the local csx"), yes()},
		{"Capture sanitized, structured execution evidence", no(), partial("only through the local csx the MCP host runs"), yes()},
		{"File an unsigned execution footprint", yes(), noBecause("files correlated adoption evidence instead"), noBecause("files correlated adoption evidence instead")},
		{"Automatic failed-build lookup hook", no(), no(), yes()},
		{"Background sync and offline cache", no(), no(), yes()},
		{"Privacy preview before anything is uploaded", no(), no(), yes()},
		{"Publish a verified sample", no(), noBecause("deliberately no publish tool"), yesIf("a person confirms at the CLI")},
		{"Worker and matrix verification", no(), no(), yes()},
		{"Signed verification receipts (ed25519, from the worker)", no(), no(), yes()},
	}
}

// README markers. The matrix between them is the one above, rendered as
// GitHub markdown; a test regenerates it and compares.
const (
	readmeMatrixStart = "<!-- BEGIN:CSX-SURFACE-MATRIX -->"
	readmeMatrixEnd   = "<!-- END:CSX-SURFACE-MATRIX -->"
)

// surfaceMatrixMarkdown renders the matrix as the table the READMEs carry.
// The header is passed in because the nine READMEs translate it; the rows
// are not, because a translated README pins only the marks (see the test).
func surfaceMatrixMarkdown(header [4]string) string {
	var b strings.Builder
	b.WriteString("| " + header[0] + " | " + header[1] + " | " + header[2] + " | " + header[3] + " |\n")
	b.WriteString("|---|:--|:--|:--|\n")
	for _, row := range capabilityMatrix() {
		b.WriteString("| " + row.Capability)
		for _, m := range []surfaceMark{row.REST, row.MCP, row.CLI} {
			b.WriteString(" | " + surfaceCell(m))
		}
		b.WriteString(" |\n")
	}
	return b.String()
}

// surfaceCell is a glyph, then the condition if there is one.
func surfaceCell(m surfaceMark) string {
	if m.Note == "" {
		return surfaceGlyph(m)
	}
	return surfaceGlyph(m) + " " + m.Note
}

// surfaceGlyph is how a mark is drawn in a document that has no colour: the
// same three glyphs in the READMEs and on the page, so a reader who saw one
// recognises the other.
func surfaceGlyph(m surfaceMark) string {
	switch m.Mark {
	case "yes":
		return "✅"
	case "partial":
		return "⚠️"
	default:
		return "❌"
	}
}
