package sanitizer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// assertNoLeak enforces the global privacy invariant: no sanitized template
// may contain a Windows drive path, a Unix home path, or the current OS
// username.
func assertNoLeak(t *testing.T, template string) {
	t.Helper()
	for _, bad := range []string{`C:\`, "/home/"} {
		if strings.Contains(template, bad) {
			t.Errorf("template leaks %q:\n%s", bad, template)
		}
	}
	name := currentUsername()
	if len(name) >= 3 && !strings.Contains("node_modules", name) {
		if strings.Contains(strings.ToLower(template), strings.ToLower(name)) {
			t.Errorf("template leaks current username %q:\n%s", name, template)
		}
	}
}

func TestSanitizeTable(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		stage        domain.Stage
		pkgs         []string
		wantCode     string
		wantContains []string
		wantAbsent   []string
		wantSymbols  []string
	}{
		{
			name:         "TS2345 with windows user path",
			raw:          `C:\Users\someone\project\src\index.ts(10,5): error TS2345: Argument of type 'string' is not assignable to parameter of type 'number'.`,
			stage:        domain.StageProjectTypecheck,
			wantCode:     "TS2345",
			wantContains: []string{"TS2345", "<path>", "<str>"},
			wantAbsent:   []string{`C:\`, "someone", "'string'", "(10,5)", "index.ts"},
		},
		{
			name:         "ERR_REQUIRE_ESM keeps public node_modules package",
			raw:          `Error [ERR_REQUIRE_ESM]: require() of ES Module C:\Users\someone\app\node_modules\axios\index.js from C:\Users\someone\app\main.js not supported.`,
			stage:        domain.StageProjectProcess,
			pkgs:         []string{"axios"},
			wantCode:     "ERR_REQUIRE_ESM",
			wantContains: []string{"ERR_REQUIRE_ESM", "node_modules/axios", "<path>"},
			wantAbsent:   []string{`C:\`, "someone", "main.js", "index.js"},
			wantSymbols:  []string{"axios"},
		},
		{
			name: "stack trace with URL, hex token, unix path, email",
			raw: "Error: fetch failed\n" +
				"    at fetchData (https://cdn.example.com/assets/bundle.min.js:1:23456)\n" +
				"    at process (/home/someone/app/dist/worker.js:88:12)\n" +
				"build id deadbeefdeadbeefdeadbeefdeadbeefdeadbeef\n" +
				"contact ops@corp.example.com",
			stage:        domain.StageProjectProcess,
			wantCode:     "",
			wantContains: []string{"<url>", "<token>", "<path>", "<email>", ":<n>:<n>"},
			wantAbsent:   []string{"https://", "deadbeef", "/home/", "someone", "example.com"},
		},
		{
			name:         "scoped package survives node_modules strip",
			raw:          `Cannot find module '@scope/ui' imported from /home/someone/app/node_modules/@scope/ui/dist/index.mjs`,
			stage:        domain.StageProjectProcess,
			pkgs:         []string{"@scope/ui", "axios"},
			wantContains: []string{"node_modules/@scope/ui", "<str>"},
			wantAbsent:   []string{"/home/", "someone", "dist"},
			wantSymbols:  []string{"@scope/ui"},
		},
		{
			name:         "private node_modules package does not survive",
			raw:          `Module not found: C:\repo\node_modules\secret-lib\index.js`,
			stage:        domain.StageProjectCompile,
			pkgs:         []string{"axios"},
			wantContains: []string{"<path>"},
			wantAbsent:   []string{`C:\`, "secret-lib", "index.js"},
		},
		{
			name:         "rustc code and line-col numbers",
			raw:          "error[E0308]: mismatched types\n --> src/main.rs:3:5",
			stage:        domain.StageProjectCompile,
			wantCode:     "E0308",
			wantContains: []string{"E0308", ":<n>:<n>"},
			wantAbsent:   []string{":3:5"},
		},
		{
			name:         "errno code with quoted unix path",
			raw:          `Error: ENOENT: no such file or directory, open '/home/someone/.env'`,
			stage:        domain.StageProjectProcess,
			wantCode:     "ENOENT",
			wantContains: []string{"ENOENT", "<str>"},
			wantAbsent:   []string{"/home/", "someone", ".env"},
		},
		{
			name:         "exit code number becomes placeholder",
			raw:          `Process exited: command "npm run build" failed with exit code 137`,
			stage:        domain.StageProjectCompile,
			wantCode:     "",
			wantContains: []string{"exit code <n>", "<str>"},
			wantAbsent:   []string{"137", "npm run build"},
		},
		{
			name:         "paren line-col placeholder",
			raw:          `index.ts(10,5): error TS2551: Property 'foo' does not exist`,
			stage:        domain.StageProjectTypecheck,
			wantCode:     "TS2551",
			wantContains: []string{"TS2551", "(<n>,<n>)", "<str>"},
			wantAbsent:   []string{"(10,5)", "'foo'"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Sanitize(tc.raw, tc.stage, tc.pkgs)
			if got.Code != tc.wantCode {
				t.Errorf("Code = %q, want %q", got.Code, tc.wantCode)
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(got.Template, want) {
					t.Errorf("template missing %q:\n%s", want, got.Template)
				}
			}
			for _, bad := range tc.wantAbsent {
				if strings.Contains(got.Template, bad) {
					t.Errorf("template must not contain %q:\n%s", bad, got.Template)
				}
			}
			if !strings.HasPrefix(got.Fingerprint, "sha256:") {
				t.Errorf("fingerprint %q lacks sha256: prefix", got.Fingerprint)
			}
			if len(tc.wantSymbols) > 0 {
				if len(got.PublicSymbols) != len(tc.wantSymbols) {
					t.Fatalf("PublicSymbols = %v, want %v", got.PublicSymbols, tc.wantSymbols)
				}
				for i, s := range tc.wantSymbols {
					if got.PublicSymbols[i] != s {
						t.Errorf("PublicSymbols = %v, want %v", got.PublicSymbols, tc.wantSymbols)
					}
				}
			}
			assertNoLeak(t, got.Template)
		})
	}
}

func TestSanitizeScrubsCurrentUser(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	uname := currentUsername()
	raw := "loaded config for " + uname + " from " + filepath.Join(home, "csx", "config.json")
	got := Sanitize(raw, domain.StageProjectProcess, nil)
	if strings.Contains(got.Template, home) {
		t.Errorf("template leaks home dir %q:\n%s", home, got.Template)
	}
	if len(uname) >= 3 && strings.Contains(got.Template, uname) {
		t.Errorf("template leaks username %q:\n%s", uname, got.Template)
	}
	assertNoLeak(t, got.Template)
}

func TestFingerprintStableAndCodeSensitive(t *testing.T) {
	raw := `src/index.ts(10,5): error TS2345: Argument of type 'string' is not assignable`
	a := Sanitize(raw, domain.StageProjectTypecheck, nil)
	b := Sanitize(raw, domain.StageProjectTypecheck, nil)
	if a.Fingerprint != b.Fingerprint {
		t.Errorf("fingerprint unstable: %q vs %q", a.Fingerprint, b.Fingerprint)
	}
	c := Sanitize(strings.ReplaceAll(raw, "TS2345", "TS2551"), domain.StageProjectTypecheck, nil)
	if c.Fingerprint == a.Fingerprint {
		t.Error("fingerprint must differ for different error codes")
	}
	d := Sanitize(raw, domain.StageProjectCompile, nil)
	if d.Fingerprint == a.Fingerprint {
		t.Error("fingerprint must differ for different stages")
	}
}
