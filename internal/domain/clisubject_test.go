package domain

import (
	"strings"
	"testing"
)

// The issue's canonical identity: tool + version + command path + normalized
// options + OS/arch + shell/runtime. `gh workflow run --ref main --yes ci.yml`
// keeps its structure — the path is two segments, the options are names with a
// value class, the file is an operand class — and nothing typed by the user
// survives as a literal.
func TestCLISubjectFromArgvModelsTheCommandPathStructurally(t *testing.T) {
	env := EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "generic", OS: "linux", Arch: "x64"}
	s := CLISubjectFromArgv([]string{"gh", "workflow", "run", "--ref", "main", "--yes", "ci.yml"}, env, "2.40.0", "bash")

	if s.SchemaVersion != CLISubjectSchemaVersion {
		t.Errorf("schemaVersion = %d, want %d", s.SchemaVersion, CLISubjectSchemaVersion)
	}
	if s.Tool != "gh" || s.ToolVersion != "2.40.0" {
		t.Errorf("tool = %q@%q", s.Tool, s.ToolVersion)
	}
	if strings.Join(s.Path, "/") != "workflow/run" {
		t.Errorf("path = %v, want [workflow run]", s.Path)
	}
	// --ref is not in the sanitizer's value-consuming vocabulary, so "main"
	// cannot safely be called its value: it stays an operand class. That is
	// the "where safe" in the contract — an unknown flag is a bare flag.
	wantOpts := []CLIOption{{Name: "--ref"}, {Name: "--yes"}}
	if len(s.Options) != len(wantOpts) {
		t.Fatalf("options = %+v, want %+v", s.Options, wantOpts)
	}
	for i := range wantOpts {
		if s.Options[i] != wantOpts[i] {
			t.Errorf("option[%d] = %+v, want %+v", i, s.Options[i], wantOpts[i])
		}
	}
	if len(s.Operands) != 2 || s.Operands[0] != CLIValueArg || s.Operands[1] != CLIValuePath {
		t.Errorf("operands = %v, want [arg path]", s.Operands)
	}
	if s.OS != "linux" || s.Arch != "x64" || s.Shell != "bash" {
		t.Errorf("os/arch/shell = %q/%q/%q", s.OS, s.Arch, s.Shell)
	}
	for _, field := range []string{"main", "ci.yml"} {
		if strings.Contains(string(MustCanonicalJSON(s)), field) {
			t.Errorf("literal %q leaked into the subject identity: %s", field, MustCanonicalJSON(s))
		}
	}
}

// Option order is not command semantics for the tools this network records;
// `--yes --ref x` and `--ref x --yes` are the same subject. Operand order is.
func TestCLISubjectIdentityIgnoresOptionOrderButKeepsOperandOrder(t *testing.T) {
	env := EnvironmentFingerprint{SchemaVersion: 1, OS: "linux", Arch: "x64"}
	a := CLISubjectFromArgv([]string{"gh", "workflow", "run", "--ref", "main", "--yes"}, env, "2.40.0", "bash")
	b := CLISubjectFromArgv([]string{"gh", "workflow", "run", "--yes", "--ref", "dev"}, env, "2.40.0", "bash")
	if a.SubjectID() != b.SubjectID() {
		t.Errorf("option order split one subject in two:\n%s\n%s", a.Ref(), b.Ref())
	}
	c := CLISubjectFromArgv([]string{"docker", "compose", "up", "./x", "https://h"}, env, "27.0.1", "bash")
	d := CLISubjectFromArgv([]string{"docker", "compose", "up", "https://h", "./x"}, env, "27.0.1", "bash")
	if c.SubjectID() == d.SubjectID() {
		t.Errorf("operand order was erased: %s", c.Ref())
	}
	if !strings.HasPrefix(a.SubjectID(), "clisubject:sha256:") {
		t.Errorf("subject id = %q, want clisubject:sha256: prefix", a.SubjectID())
	}
}

// A `--with-token ghp_x` flag is semantic (the command took a token) but its
// value is never identity, and a host operand is reduced to its class.
func TestCLISubjectKeepsSecretsOutOfIdentity(t *testing.T) {
	env := EnvironmentFingerprint{SchemaVersion: 1, OS: "linux", Arch: "x64"}
	s := CLISubjectFromArgv([]string{"gh", "auth", "login", "--with-token", "ghp_abcdef0123456789", "--hostname", "github.com"}, env, "2.40.0", "bash")
	json := string(MustCanonicalJSON(s))
	if strings.Contains(json, "ghp_") || strings.Contains(json, "github.com") {
		t.Fatalf("secret or host leaked: %s", json)
	}
	var sawToken bool
	for _, o := range s.Options {
		if o.Name == "--with-token" {
			sawToken = true
			if o.ValueClass != CLIValueSecret {
				t.Errorf("--with-token value class = %q, want secret", o.ValueClass)
			}
		}
	}
	if !sawToken {
		t.Errorf("semantic flag --with-token dropped: %+v", s.Options)
	}
}

// Windows spellings of the same command are the same subject: the launcher
// suffix, the case and PowerShell's capitalised shell name all normalize.
func TestCLISubjectCanonicalizesLauncherSuffixAndCase(t *testing.T) {
	env := EnvironmentFingerprint{SchemaVersion: 1, OS: "windows", Arch: "x64"}
	a := CLISubjectFromArgv([]string{`C:\Program Files\nodejs\npm.cmd`, "RUN", "build"}, env, "10.8.2", "PowerShell")
	b := CLISubjectFromArgv([]string{"npm", "run", "build"}, env, "10.8.2", "powershell")
	if a.SubjectID() != b.SubjectID() {
		t.Errorf("spellings split the subject:\n%s\n%s", a.Ref(), b.Ref())
	}
	if a.Shell != "powershell" {
		t.Errorf("shell = %q, want powershell", a.Shell)
	}
}

// The reference is the address downstream work uses. It must survive a
// round trip and it must not be a package purl.
func TestCLISubjectRefRoundTrips(t *testing.T) {
	env := EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "golang", OS: "linux", Arch: "arm64", Runtime: "go", RuntimeVersion: "1.26.5"}
	s := CLISubjectFromArgv([]string{"go", "test", "-race", "-count=1", "./..."}, env, "1.26.5", "bash")
	ref := s.Ref()
	if !strings.HasPrefix(ref, "cli:go@1.26.5/test?") {
		t.Fatalf("ref = %q", ref)
	}
	if strings.HasPrefix(ref, "pkg:") || strings.Contains(ref, "generic") {
		t.Fatalf("ref is a fake package identity: %q", ref)
	}
	back, err := ParseCLISubjectRef(ref)
	if err != nil {
		t.Fatalf("parse %q: %v", ref, err)
	}
	if back.SubjectID() != s.SubjectID() {
		t.Errorf("round trip changed identity:\n%s\n%s", ref, back.Ref())
	}
	if back.Runtime != "go 1.26.5" {
		t.Errorf("runtime = %q, want %q", back.Runtime, "go 1.26.5")
	}
	for _, bad := range []string{"pkg:npm/axios@1.0.0", "cli:", "cli:?os=linux", "gh workflow run"} {
		if _, err := ParseCLISubjectRef(bad); err == nil {
			t.Errorf("ParseCLISubjectRef(%q) accepted a non-reference", bad)
		}
	}
}

// Runtime is identity only for a tool that runs inside that runtime. `gh`
// in a Node project is not "gh on node 22"; `npx` is.
func TestCLISubjectRuntimeOnlyForRuntimeBoundTools(t *testing.T) {
	env := EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64", Runtime: "node", RuntimeVersion: "22.18"}
	if s := CLISubjectFromArgv([]string{"gh", "pr", "view"}, env, "2.40.0", "bash"); s.Runtime != "" {
		t.Errorf("gh carried the project runtime %q", s.Runtime)
	}
	if s := CLISubjectFromArgv([]string{"npx", "tsc"}, env, "10.8.2", "bash"); s.Runtime != "node 22.18" {
		t.Errorf("npx runtime = %q, want node 22.18", s.Runtime)
	}
}

func TestMatchCLISubjectGrades(t *testing.T) {
	env := EnvironmentFingerprint{SchemaVersion: 1, OS: "linux", Arch: "x64"}
	target := CLISubjectFromArgv([]string{"git", "worktree", "add", "../w", "feature/x"}, env, "2.46.0", "bash")

	cases := []struct {
		name      string
		candidate CLISubject
		grade     MatchGrade
		different []string
	}{
		{"same everything", target, GradeExact, nil},
		{"other os", CLISubjectFromArgv([]string{"git", "worktree", "add", "../w", "feature/x"},
			EnvironmentFingerprint{SchemaVersion: 1, OS: "windows", Arch: "x64"}, "2.46.0", "pwsh"),
			GradeAdaptationRequired, []string{"os", "shell"}},
		{"other version", CLISubjectFromArgv([]string{"git", "worktree", "add", "../w", "feature/x"}, env, "2.39.0", "bash"),
			GradeAdaptationRequired, []string{"toolVersion"}},
		{"extra option", CLISubjectFromArgv([]string{"git", "worktree", "add", "--detach", "../w", "feature/x"}, env, "2.46.0", "bash"),
			GradeAdaptationRequired, []string{"options"}},
		{"undeclared shell", CLISubjectFromArgv([]string{"git", "worktree", "add", "../w", "feature/x"}, env, "2.46.0", ""),
			GradeCompatible, nil},
		{"other subcommand", CLISubjectFromArgv([]string{"git", "worktree", "list"}, env, "2.46.0", "bash"),
			GradeNoSafeMatch, []string{"path"}},
		{"other tool", CLISubjectFromArgv([]string{"gh", "worktree", "add"}, env, "2.46.0", "bash"),
			GradeNoSafeMatch, []string{"tool"}},
	}
	for _, tc := range cases {
		m := MatchCLISubject(target, tc.candidate)
		if m.Grade != tc.grade {
			t.Errorf("%s: grade = %s, want %s (different=%v undeclared=%v)", tc.name, m.Grade, tc.grade, m.Different, m.Undeclared)
		}
		if strings.Join(m.Different, ",") != strings.Join(tc.different, ",") {
			t.Errorf("%s: different = %v, want %v", tc.name, m.Different, tc.different)
		}
	}
	if m := MatchCLISubject(CLISubject{}, target); m.Grade != GradeNoSafeMatch {
		t.Errorf("empty target graded %s", m.Grade)
	}
}

// The subject is derived from the coordinate the recorder already produces,
// so every stored execution gains an address without being re-recorded.
func TestCoordinateSubjectAgreesWithArgvSubject(t *testing.T) {
	env := EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "generic", OS: "linux", Arch: "x64"}
	argv := []string{"docker", "compose", "up", "-d", "--build", "-f", "compose.yml"}
	fromArgv := CLISubjectFromArgv(argv, env, "27.0.1", "bash")
	coord := ParseCLICommand(argv, env)
	coord.ToolVersion = "27.0.1"
	coord.Shell = "bash"
	if got := coord.Subject(); got.SubjectID() != fromArgv.SubjectID() {
		t.Errorf("coordinate subject differs:\n%s\n%s", got.Ref(), fromArgv.Ref())
	}
}
