package verifier

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestExtractBuildDiagnosticsFromRecordedToolOutput(t *testing.T) {
	type want struct {
		tool, code string
		kind       domain.BuildDiagnosticKind
		file       string
		line       int
		contains   string
	}
	cases := map[string][]want{
		"node-dep.log": {
			{"node", "DEP0005", domain.BuildDiagnosticDeprecated, "", 0, "Buffer() is deprecated"},
			{"node", "DEP0169", domain.BuildDiagnosticDeprecated, "", 0, "url.parse()"},
		},
		"npm.log":    {{"npm", "deprecated", domain.BuildDiagnosticDeprecated, "", 0, "inflight@1.0.6: This module is not supported"}},
		"py-dep.log": {{"python", "DeprecationWarning", domain.BuildDiagnosticDeprecated, "", 0, "the imp module is deprecated"}},
		"javac.log": {
			{"javac", "removal", domain.BuildDiagnosticDeprecated, "A.java", 1, "Integer(int) in Integer has been deprecated"},
			{"javac", "deprecation", domain.BuildDiagnosticDeprecated, "A.java", 1, "stop() in Thread has been deprecated"},
		},
		"cargo.log": {
			{"rustc", "deprecated", domain.BuildDiagnosticDeprecated, "src/main.rs", 3, "use of deprecated function `foo`: use bar"},
			{"rustc", "deprecated", domain.BuildDiagnosticDeprecated, "src/main.rs", 3, "std::mem::uninitialized"},
		},
		"php.log":  {{"php", "E_DEPRECATED", domain.BuildDiagnosticDeprecated, "p.php", 2, "Optional parameter $a declared before required parameter $b"}},
		"ruby.log": {{"ruby", "warning", domain.BuildDiagnosticDeprecated, "r.rb", 3, "'$;' is deprecated"}},
	}
	env := domain.EnvironmentFingerprint{Runtime: "node", RuntimeVersion: "24.11.1"}
	for name, wants := range cases {
		raw, err := os.ReadFile(filepath.Join("testdata", "warnings", name))
		if err != nil {
			t.Fatal(err)
		}
		got := extractBuildDiagnostics("compile", string(raw), []string{`C:\sample`}, env)
		if len(got) != len(wants) {
			t.Fatalf("%s: got %d diagnostics, want %d: %+v", name, len(got), len(wants), got)
		}
		for i, w := range wants {
			d := got[i]
			if d.Tool != w.tool || d.Code != w.code || d.Kind != w.kind || d.File != w.file || d.Line != w.line ||
				!strings.Contains(d.Message, w.contains) || d.Stage != "compile" || d.Severity != domain.BuildDiagnosticWarning {
				t.Errorf("%s[%d] = %+v, want %+v", name, i, d, w)
			}
			if d.RawLogDigest != domain.SHA256Hex(raw) {
				t.Errorf("%s[%d] rawLogDigest does not attest the stage log", name, i)
			}
		}
		if got != nil {
			if err := serverstore.ValidateBuildDiagnostics(got, true); err != nil {
				t.Errorf("%s: server would reject extracted diagnostics: %v", name, err)
			}
		}
	}
	if d := extractBuildDiagnostics("compile", "(node:1) [DEP0005] DeprecationWarning: x", nil, env); d[0].ToolVersion != "24.11.1" {
		t.Errorf("node toolVersion = %q", d[0].ToolVersion)
	}
}

func TestExtractBuildDiagnosticsIgnoresQuietLogs(t *testing.T) {
	if d := extractBuildDiagnostics("compile", "added 3 packages in 3s\nok\n", nil, domain.EnvironmentFingerprint{}); d != nil {
		t.Fatalf("quiet log produced diagnostics: %+v", d)
	}
}

func TestEngineRecordsWarningsOnlyFromPassingStages(t *testing.T) {
	npmLog, err := os.ReadFile(filepath.Join("testdata", "warnings", "npm.log"))
	if err != nil {
		t.Fatal(err)
	}
	nodeLog, err := os.ReadFile(filepath.Join("testdata", "warnings", "node-dep.log"))
	if err != nil {
		t.Fatal(err)
	}
	r := allPassRunner()
	r.resolve.Log = string(npmLog)
	r.contract.Log = string(nodeLog)
	ident := testIdentity(t)
	receipt, err := Run(context.Background(), r, domain.CapContainerRun, fixtureDir(t), testManifest(), ident, testEnv())
	if err != nil {
		t.Fatal(err)
	}
	var stages []string
	for _, d := range receipt.Diagnostics {
		stages = append(stages, d.Stage+":"+d.Tool+":"+d.Code)
	}
	if got := strings.Join(stages, ","); got != "resolve:npm:deprecated,contract:node:DEP0005,contract:node:DEP0169" {
		t.Fatalf("diagnostics = %s", got)
	}
	if !identity.Verify(receipt.PeerPubkey, receipt.PeerSignature, receipt.SigningBytes()) {
		t.Fatal("signature does not cover diagnostics")
	}

	r = allPassRunner()
	r.contract = sandbox.StageResult{Result: sandbox.ResultFail, Log: string(nodeLog)}
	receipt, err = Run(context.Background(), r, domain.CapContainerRun, fixtureDir(t), testManifest(), ident, testEnv())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Diagnostics != nil {
		t.Fatalf("failed stage warnings were recorded as diagnostics: %+v", receipt.Diagnostics)
	}
}
