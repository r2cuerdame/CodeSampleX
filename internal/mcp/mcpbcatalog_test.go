package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The MCPB bundle and this server describe the same eight tools to two
// different readers, and until now they described them from two different
// lists: `TOOLS` in scripts/make-mcpb.py, and toolDefs() here. Nothing
// connected them, so a renamed tool would have shipped a manifest naming a
// tool the binary inside that manifest does not answer — and the manifest is
// what a directory renders when it cannot run the server, which is every
// directory that has ever rendered this one.
//
// scripts/mcp-tools.json is now the one list. It is generated from toolDefs()
// and read by make-mcpb.py, and this test is what keeps it generated:
//
//	CSX_UPDATE_GOLDEN=1 go test ./internal/mcp -run TestTheMCPBToolCatalogMatchesToolDefs
//
// The catalog carries name, title, summary and annotations. It deliberately
// does not carry inputSchema: the MCPB manifest schema declares
// `additionalProperties: false` on each tools[] entry with only `name` and
// `description` allowed, so a schema (or an annotation) placed there would
// fail `mcpb validate`. The annotations reach clients through tools/list.

const catalogRelPath = "scripts/mcp-tools.json"

type catalogTool struct {
	Name        string           `json:"name"`
	Title       string           `json:"title"`
	Summary     string           `json:"summary"`
	Description string           `json:"description"`
	Annotations *toolAnnotations `json:"annotations"`
}

type toolCatalog struct {
	Generated string        `json:"_generated"`
	Tools     []catalogTool `json:"tools"`
}

const catalogNote = "Generated from toolDefs() in internal/mcp/tools.go. Do not edit by hand: " +
	"run `CSX_UPDATE_GOLDEN=1 go test ./internal/mcp -run TestTheMCPBToolCatalogMatchesToolDefs`. " +
	"scripts/make-mcpb.py reads `summary` into the MCPB manifest's tools[].description; " +
	"clients read `description` and `annotations` from tools/list."

func buildCatalog() ([]byte, error) {
	cat := toolCatalog{Generated: catalogNote}
	for _, d := range toolDefs() {
		cat.Tools = append(cat.Tools, catalogTool{
			Name:        d.Name,
			Title:       d.Title,
			Summary:     d.Summary,
			Description: d.Description,
			Annotations: d.Annotations,
		})
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	// Descriptions contain & and < in prose; HTML escaping would render the
	// checked-in file unreadable and make it churn for no reason.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(cat); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func TestTheMCPBToolCatalogMatchesToolDefs(t *testing.T) {
	want, err := buildCatalog()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(docsRepoRoot(t), filepath.FromSlash(catalogRelPath))

	if os.Getenv("CSX_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("regenerated %s", catalogRelPath)
		return
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", catalogRelPath, err)
	}
	// The file is read by Python on Windows checkouts too; compare on
	// content, not on the line ending git happened to hand back.
	if !bytes.Equal(bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n")), want) {
		t.Errorf("%s is stale — the MCPB bundle would describe a different tool set than tools/list.\n"+
			"Regenerate it with:\n  CSX_UPDATE_GOLDEN=1 go test ./internal/mcp -run TestTheMCPBToolCatalogMatchesToolDefs",
			catalogRelPath)
	}
}

// make-mcpb.py must read the catalog rather than keep its own copy. A second
// list is the failure this whole file exists to prevent, so the absence of
// one is checked directly instead of inferred.
func TestMakeMCPBReadsTheGeneratedCatalog(t *testing.T) {
	src := readDoc(t, "scripts/make-mcpb.py")
	if !strings.Contains(src, "mcp-tools.json") {
		t.Error("scripts/make-mcpb.py no longer reads scripts/mcp-tools.json; the manifest's tool list would drift from toolDefs()")
	}
	if strings.Contains(src, "TOOLS = [") {
		t.Error("scripts/make-mcpb.py has its own TOOLS list again; there must be exactly one tool list, and it is toolDefs()")
	}
}

// The MCPB manifest is the only description of this server a directory can
// render without running it, and Anthropic's local-connector submission
// requires a privacy policy in both the README and the manifest. Both are
// pinned here so a rebuild cannot quietly drop either.
func TestThePrivacyPolicyIsPublishedWhereSubmissionRequiresIt(t *testing.T) {
	const policyURL = "https://github.com/r2cuerdame/CodeSampleX/blob/main/PRIVACY.md"

	mk := readDoc(t, "scripts/make-mcpb.py")
	if !strings.Contains(mk, `"privacy_policies"`) || !strings.Contains(mk, policyURL) {
		t.Errorf("scripts/make-mcpb.py does not put %s in the manifest's privacy_policies array", policyURL)
	}

	readme := readDoc(t, "README.md")
	if !strings.Contains(readme, "## Privacy Policy") {
		t.Error(`README.md has no "## Privacy Policy" heading; the submission requirement names the heading, not just the content`)
	}
	if !strings.Contains(readme, "PRIVACY.md") {
		t.Error("README.md does not link PRIVACY.md")
	}
	if readDoc(t, "PRIVACY.md") == "" {
		t.Error("PRIVACY.md is empty")
	}
}

// Annotations are a claim about behaviour, and the four that would be
// convenient to get wrong are the four that matter. Each line below is a fact
// about the implementation, cited where it lives.
func TestToolAnnotationsDescribeWhatTheToolsActuallyDo(t *testing.T) {
	byName := map[string]toolDef{}
	for _, d := range toolDefs() {
		byName[d.Name] = d
	}

	for name, d := range byName {
		if d.Title == "" {
			t.Errorf("%s has no title; every submitted tool must carry one", name)
		}
		if d.Summary == "" {
			t.Errorf("%s has no summary; the MCPB manifest would carry an empty description for it", name)
		}
		if d.Annotations == nil {
			t.Errorf("%s carries no annotations", name)
			continue
		}
		if d.Annotations.Title != d.Title {
			t.Errorf("%s: annotations.title %q disagrees with title %q", name, d.Annotations.Title, d.Title)
		}
		if d.Annotations.ReadOnlyHint && d.Annotations.DestructiveHint != nil {
			t.Errorf("%s is read-only, so destructiveHint is meaningless and must be absent", name)
		}
		if !d.Annotations.ReadOnlyHint && d.Annotations.DestructiveHint == nil {
			t.Errorf("%s is not read-only, so it must state destructiveHint explicitly", name)
		}
	}

	// run_observed_command executes the argv it is handed, in the user's own
	// project (Deps.RunObserved → runObserved → exec). `npm test` writes
	// files; nothing here inspects the command first.
	if a := byName["run_observed_command"].Annotations; a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint {
		t.Error("run_observed_command runs an arbitrary user command; it must be destructive and not read-only")
	}

	// Every search records a hit row and an offer token
	// (recordSearchOutcomeReloaded), which is what list_local_hits lists.
	if byName["search_known_solution"].Annotations.ReadOnlyHint {
		t.Error("search_known_solution records a local hit and an offer token; it is not read-only")
	}
	// propose creates a fresh clean-room directory per call
	// (samples.NewCleanRoom) and saves a proposal row.
	if a := byName["propose_public_sample"].Annotations; a.ReadOnlyHint || a.IdempotentHint {
		t.Error("propose_public_sample creates a new workspace directory on every call; it is neither read-only nor idempotent")
	}
	// Adoption writes the local correlation and, in community mode, queues an
	// upload (reportAdoptionReloaded).
	if byName["report_sample_adoption"].Annotations.ReadOnlyHint {
		t.Error("report_sample_adoption records adoption evidence; it is not read-only")
	}
	// get_sample stores a fetched artifact in the local CAS (peer.storeFetched).
	if byName["get_sample"].Annotations.ReadOnlyHint {
		t.Error("get_sample caches the fetched artifact locally; it is not read-only")
	}

	// The three that really are queries over data already on this machine.
	for _, name := range []string{"explain_compatibility", "list_local_hits", "get_local_stats"} {
		a := byName[name].Annotations
		if !a.ReadOnlyHint {
			t.Errorf("%s reads local state only; it should be read-only", name)
		}
		if a.OpenWorldHint {
			t.Errorf("%s makes no network request; openWorldHint must be false", name)
		}
	}

	// propose_public_sample is the fourth local-only tool: the spec is built
	// on this machine and publishing is a CLI step the user has to approve.
	if byName["propose_public_sample"].Annotations.OpenWorldHint {
		t.Error("propose_public_sample sends nothing; openWorldHint must be false")
	}
}
