package web

import (
	"html/template"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// GET /skill.md (#318) is the whole contract for an agent that can fetch a
// URL and nothing else, so it is served as markdown, from any origin, with
// the deployment's own origin in every URL it names.
func TestSkillDocumentIsServedWithTheDeploymentOrigin(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/skill.md")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /skill.md = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("Content-Type = %q, want text/markdown", ct)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("skill.md is not readable from another origin")
	}
	body := rec.Body.String()
	if strings.Contains(body, "__CSX_BASE_URL__") {
		t.Error("the origin placeholder survived into the served document")
	}
	for _, want := range []string{
		"api_base: https://codesamplex.dev",
		"NO_SAFE_MATCH",
		"GET /v2/search",
		"POST /v1/footprints/execution",
		"unsigned self-report",
		"nearby environment is not an exact match",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("skill.md does not say %q", want)
		}
	}
	// The document must not read as an instruction to override the agent's
	// operator; it says so in as many words.
	if !strings.Contains(body, "does not ask you to bypass instructions") {
		t.Error("skill.md lost its sentence about not overriding operator instructions")
	}
}

var skillRoute = regexp.MustCompile("`(GET|POST) (/[^` ?]+)")

// Every route skill.md names is one the features page's reference lists,
// and that reference is held to the routers by its own test. A guide that
// names a route nobody registered would send an agent to a 404.
func TestSkillDocumentNamesOnlyPublishedRoutes(t *testing.T) {
	published := map[string]bool{}
	for _, e := range publicReadAPI() {
		path := e.Path
		if i := strings.Index(path, "?"); i >= 0 {
			path = path[:i]
		}
		published[e.Method+" "+path] = true
	}
	matches := skillRoute.FindAllStringSubmatch(skillDocument, -1)
	if len(matches) < 6 {
		t.Fatalf("only %d routes found in skill.md; the document shape changed", len(matches))
	}
	for _, m := range matches {
		if !published[m[1]+" "+m[2]] {
			t.Errorf("skill.md names %s %s, which the API reference does not publish", m[1], m[2])
		}
	}
}

// The features page draws the matrix, the curl line with the deployment's
// origin, and the door to skill.md, in that order after the REST section.
func TestTheFeaturesPageRendersTheThreeDoors(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/features")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /features = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"No install needed to read the network",
		"curl 'https://codesamplex.dev/v2/search?",
		"What each door can do",
		"REST reads the network. The CLI lets the network observe reality.",
		`href="/skill.md"`,
		"https://codesamplex.dev/skill.md",
		"MCP: an adapter on the CLI",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/features does not render %q", want)
		}
	}
	for _, row := range capabilityMatrix() {
		if !strings.Contains(body, template.HTMLEscapeString(row.Capability)) {
			t.Errorf("/features does not draw the %q row", row.Capability)
		}
	}
	rest := strings.Index(body, `id="rest"`)
	matrix := strings.Index(body, `id="surfaces"`)
	mcp := strings.Index(body, `id="developer-reference"`)
	skill := strings.Index(body, `id="skill"`)
	if !(rest < matrix && matrix < mcp && mcp < skill) {
		t.Errorf("section order is rest=%d matrix=%d mcp=%d skill=%d; want REST, matrix, MCP, skill.md", rest, matrix, mcp, skill)
	}
}

// surfaceGlyphs is the three marks in one README row, in column order.
var surfaceGlyphs = regexp.MustCompile("✅|⚠️|❌")

func readmeMatrixRows(t *testing.T, path string) []string {
	t.Helper()
	doc := strings.ReplaceAll(readRepoFile(t, path), "\r\n", "\n")
	a := strings.Index(doc, readmeMatrixStart)
	z := strings.Index(doc, readmeMatrixEnd)
	if a < 0 || z < 0 || z <= a {
		t.Fatalf("%s: surface matrix markers missing", path)
	}
	var rows []string
	for _, line := range strings.Split(doc[a+len(readmeMatrixStart):z], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "| ") || strings.HasPrefix(line, "|---") {
			continue
		}
		rows = append(rows, line)
	}
	if len(rows) < 2 {
		t.Fatalf("%s: no matrix rows between the markers", path)
	}
	// The first row is the translated header.
	return rows[1:]
}

// The nine READMEs carry the same matrix the features page draws. The
// English one is regenerated and compared whole; the eight translations
// translate the words but pin every mark, so a cell cannot say yes in one
// language and no in another.
func TestEveryREADMECarriesTheSurfaceMatrix(t *testing.T) {
	want := capabilityMatrix()
	english := strings.ReplaceAll(readRepoFile(t, "README.md"), "\r\n", "\n")
	a := strings.Index(english, readmeMatrixStart)
	z := strings.Index(english, readmeMatrixEnd)
	if a < 0 || z < 0 {
		t.Fatal("README.md: surface matrix markers missing")
	}
	got := strings.TrimSpace(english[a+len(readmeMatrixStart) : z])
	if exp := strings.TrimSpace(surfaceMatrixMarkdown([4]string{"Capability", "Web REST (no install)", "MCP", "CLI installed"})); got != exp {
		t.Errorf("README.md surface matrix drifted from capabilityMatrix()\n got:\n%s\nwant:\n%s", got, exp)
	}

	for _, path := range publicREADMEs() {
		rows := readmeMatrixRows(t, path)
		if len(rows) != len(want) {
			t.Errorf("%s: %d matrix rows, the matrix has %d", path, len(rows), len(want))
			continue
		}
		for i, row := range rows {
			cells := strings.Split(strings.Trim(row, "|"), "|")
			if len(cells) != 4 {
				t.Errorf("%s row %d: %d cells, want 4: %s", path, i, len(cells), row)
				continue
			}
			for j, m := range []surfaceMark{want[i].REST, want[i].MCP, want[i].CLI} {
				glyphs := surfaceGlyphs.FindAllString(cells[j+1], -1)
				if len(glyphs) != 1 || glyphs[0] != surfaceGlyph(m) {
					t.Errorf("%s row %q column %d draws %v, the matrix says %s", path, want[i].Capability, j+1, glyphs, surfaceGlyph(m))
				}
				if m.Note != "" && strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(cells[j+1]), surfaceGlyph(m))) == "" {
					t.Errorf("%s row %q column %d: a %s with no condition beside it", path, want[i].Capability, j+1, m.Mark)
				}
			}
		}
	}
}
