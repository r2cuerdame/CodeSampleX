package domain

import (
	"testing"
)

// Regression tests for the symptoms consolidated under #483. Each names the
// sub-issue it pins.

func TestParseCLICommandFindsTheSubcommandAfterLeadingOptions(t *testing.T) {
	env := EnvironmentFingerprint{SchemaVersion: 1, OS: "linux", Arch: "x64"}
	cases := []struct {
		argv            []string
		subcommand      string
		argsPattern     string
		reasonForChange string
	}{
		{[]string{"git", "-C", "/tmp", "status"}, "status", "-C <path>", "#313 value-taking global"},
		{[]string{"git", "-C", "workdir", "status", "--short"}, "status", "-C <arg> --short", "#313 leading options stay ahead of the command's own"},
		{[]string{"git", "--no-pager", "log"}, "log", "--no-pager", "#313 bare global"},
		{[]string{"docker", "-H", "tcp://localhost:2375", "ps"}, "ps", "-H <path>", "#313 docker host"},
		{[]string{"gh", "-R", "cli/cli", "pr", "list"}, "pr list", "-R <path>", "#313 multi-word after options"},
		{[]string{"kubectl", "-n", "kube-system", "get", "pods"}, "get", "-n <arg> <arg>", "#313 kubectl namespace"},
		{[]string{"npm", "--prefix", "web", "test"}, "test", "--prefix <arg>", "#313 npm prefix"},
		{[]string{"git", "-c", "core.autocrlf=false", "commit"}, "commit", "-c <assignment>", "#313 -c consumes its assignment"},
		{[]string{"git", "--git-dir=/x/.git", "status"}, "status", "--git-dir=<path>", "#313 joined option"},
		// An unlisted option followed by a path: the path is not the command.
		{[]string{"git", "--unknown", "./somewhere"}, "", "--unknown <path>", "#313 no guessing"},
		// Only options: unchanged.
		{[]string{"git", "--version"}, "", "--version", "unchanged"},
		// Unchanged for a command given first.
		{[]string{"git", "status"}, "status", "", "unchanged"},
	}
	for _, c := range cases {
		got := ParseCLICommand(c.argv, env)
		if got.Subcommand != c.subcommand || got.ArgsPattern != c.argsPattern {
			t.Errorf("%v (%s): got subcommand %q args %q, want %q / %q",
				c.argv, c.reasonForChange, got.Subcommand, got.ArgsPattern, c.subcommand, c.argsPattern)
		}
	}
}

func TestParseCLICommandMatchesMultiWordSubcommandsOnWordBoundaries(t *testing.T) {
	env := EnvironmentFingerprint{SchemaVersion: 1, OS: "linux"}
	cases := []struct {
		argv        []string
		subcommand  string
		argsPattern string
	}{
		{[]string{"npm", "run", "test:unit"}, "run", "<arg>"},
		{[]string{"npm", "run", "test-all"}, "run", "<arg>"},
		{[]string{"npm", "run", "build:prod"}, "run", "<arg>"},
		{[]string{"pnpm", "run", "test:watch"}, "run", "<arg>"},
		{[]string{"git", "remote", "add-backend"}, "remote", "<arg>"},
		{[]string{"gh", "run", "list-all"}, "run", "<arg>"},
		{[]string{"npm", "run", "test"}, "run test", ""},
		{[]string{"npm", "RUN", "Build", "--", "--watch"}, "run build", "-- --watch"},
		{[]string{"docker", "compose", "up", "-d"}, "compose up", "-d"},
	}
	for _, c := range cases {
		got := ParseCLICommand(c.argv, env)
		if got.Subcommand != c.subcommand || got.ArgsPattern != c.argsPattern {
			t.Errorf("%v: got %q / %q, want %q / %q (#356)", c.argv, got.Subcommand, got.ArgsPattern, c.subcommand, c.argsPattern)
		}
	}
}

// What was recorded is what is read back: every placeholder survives the
// symbol round trip (#303).
func TestCLISymbolRoundTripKeepsEveryPlaceholder(t *testing.T) {
	env := EnvironmentFingerprint{SchemaVersion: 1, OS: "linux", Arch: "x64"}
	argvs := [][]string{
		{"git", "checkout", "-b", "feature/my-branch"},
		{"docker", "build", "-f", "Dockerfile.dev", "."},
		{"git", "show", "4b055b1fd28167f77fb5c554e5eea49b0e4e7377"},
		{"git", "clone", "https://github.com/cli/cli"},
		{"curl", "https://example.com"},
		{"docker", "run", "-e", "KEY=val", "image"},
		{"docker", "run", "-e", "GITHUB_TOKEN=ghp_abcdef", "image"},
		{"gh", "api", "--token", "abc"},
		{"git", "-C", "/tmp/repo", "worktree", "add", "../wt"},
		{"git", "FOO=bar"},
		{"npm", "run", "test:unit"},
		{"ssh", "host.example"},
	}
	for _, argv := range argvs {
		coord := ParseCLICommand(argv, env)
		for _, prov := range []ExperienceProvenance{ProvenanceField, ProvenanceFarm} {
			symbol := EncodeCLISymbol(coord.Subcommand, coord.ArgsPattern, prov)
			sub, args, gotProv := DecodeCLISymbol(symbol, coord.Tool, env)
			if sub != coord.Subcommand || args != coord.ArgsPattern || gotProv != prov {
				t.Errorf("%v: symbol %q decoded to %q / %q / %s, recorded %q / %q / %s",
					argv, symbol, sub, args, gotProv, coord.Subcommand, coord.ArgsPattern, prov)
			}
			decoded := coord
			decoded.Subcommand, decoded.ArgsPattern = sub, args
			if !MatchesExactCoordinate(coord, decoded) {
				t.Errorf("%v: the decoded row no longer matches its own coordinate", argv)
			}
		}
	}
}

func TestCanonicalToolDropsTheDirectory(t *testing.T) {
	bare := CLIExperienceCoordinate{Tool: "git", ToolVersion: "2.45.0"}
	for _, tool := range []string{"/usr/bin/git", `C:\Program Files\Git\cmd\git.exe`, "./bin/git", "GIT.EXE"} {
		c := CLIExperienceCoordinate{Tool: tool, ToolVersion: "2.45.0"}
		if got := c.Canonical().Tool; got != "git" {
			t.Errorf("Canonical(%q).Tool = %q, want git (#341)", tool, got)
		}
		if c.CoordinateID() != bare.CoordinateID() {
			t.Errorf("%q: coordinate id differs from bare git; the directory reached the id", tool)
		}
		p, ok := c.PURL()
		if !ok || p.String() != "pkg:generic/cli/git@2.45.0" {
			t.Errorf("%q: PURL = %v, %v; want pkg:generic/cli/git@2.45.0", tool, p, ok)
		}
	}
}

func TestCompressionKeepsDistinctTerminationsApart(t *testing.T) {
	coord := CLIExperienceCoordinate{Tool: "docker", Subcommand: "container run"}
	zero := 0
	fail := func(term FailureTermination) CLIExperienceObservation {
		return CLIExperienceObservation{Coordinate: coord, Provenance: ProvenanceField, Result: ResultFail, Termination: term, Count: 1}
	}
	cases := []struct {
		name string
		a, b FailureTermination
	}{
		{"signals", FailureTermination{Kind: TerminationSignal, Signal: "SIGKILL"}, FailureTermination{Kind: TerminationSignal, Signal: "SIGTERM"}},
		{"nil vs zero exit", FailureTermination{Kind: TerminationExit}, FailureTermination{Kind: TerminationExit, ExitCode: &zero}},
		{"timeouts", FailureTermination{Kind: TerminationTimeout, TimeoutMillis: 1000}, FailureTermination{Kind: TerminationTimeout, TimeoutMillis: 60000}},
	}
	for _, c := range cases {
		got := CompressExperienceObservations([]CLIExperienceObservation{fail(c.a), fail(c.b)})
		if len(got) != 2 {
			t.Errorf("%s: compressed to %d rows, want 2 (#357)", c.name, len(got))
		}
	}
	// Identical terminations still merge.
	sig := FailureTermination{Kind: TerminationSignal, Signal: "SIGKILL"}
	if got := CompressExperienceObservations([]CLIExperienceObservation{fail(sig), fail(sig)}); len(got) != 1 || got[0].Count != 2 {
		t.Errorf("identical terminations: %+v, want one row of count 2", got)
	}
}

func TestBoundariesSurviveUncanonicalCoordinates(t *testing.T) {
	failCode := 128
	obs := []CLIExperienceObservation{
		{
			Coordinate: CLIExperienceCoordinate{Tool: "git.exe", ToolVersion: "2.40.0", Subcommand: "worktree add",
				Environment: EnvironmentFingerprint{OS: "Windows"}},
			Provenance: ProvenanceField, Result: ResultPass, Count: 1,
		},
		{
			Coordinate: CLIExperienceCoordinate{Tool: "Git", ToolVersion: "2.46.0", Subcommand: " worktree  add ",
				Environment: EnvironmentFingerprint{OS: "windows"}},
			Provenance: ProvenanceField, Result: ResultFail,
			Termination:  FailureTermination{Kind: TerminationExit, ExitCode: &failCode},
			ErrorSummary: "fatal: not a valid repository", Count: 1,
		},
	}
	summary := BuildExperienceSummary(CLIExperienceCoordinate{Tool: "git", Subcommand: "worktree add"}, obs)
	if summary.Status != "COEXISTING_BOUNDARY" {
		t.Fatalf("status = %s, want COEXISTING_BOUNDARY", summary.Status)
	}
	if len(summary.Boundaries) != 1 || summary.Boundaries[0].Axis != "version" ||
		summary.Boundaries[0].TransitionFrom != "2.40.0" || summary.Boundaries[0].TransitionTo != "2.46.0" {
		t.Fatalf("boundaries = %+v, want the 2.40.0 -> 2.46.0 version boundary (#320)", summary.Boundaries)
	}
}

func TestBoundariesAcrossOperatingSystems(t *testing.T) {
	obs := []CLIExperienceObservation{
		{
			Coordinate: CLIExperienceCoordinate{Tool: "git", ToolVersion: "2.46.0", Subcommand: "sparse-checkout set",
				Environment: EnvironmentFingerprint{OS: "linux"}},
			Provenance: ProvenanceField, Result: ResultPass, Count: 5,
		},
		{
			Coordinate: CLIExperienceCoordinate{Tool: "git", ToolVersion: "2.46.0", Subcommand: "sparse-checkout set",
				Environment: EnvironmentFingerprint{OS: "windows"}},
			Provenance: ProvenanceField, Result: ResultFail, ErrorSummary: "unable to access sparse checkout file", Count: 3,
		},
		// Undeclared version: never an OS boundary, it could be a version one.
		{
			Coordinate: CLIExperienceCoordinate{Tool: "git", Subcommand: "sparse-checkout set",
				Environment: EnvironmentFingerprint{OS: "darwin"}},
			Provenance: ProvenanceField, Result: ResultFail, Count: 1,
		},
	}
	boundaries := DetectExperienceBoundaries(obs)
	if len(boundaries) != 1 {
		t.Fatalf("boundaries = %+v, want exactly one os boundary (#323)", boundaries)
	}
	b := boundaries[0]
	if b.Axis != "os" || b.TransitionFrom != "linux" || b.TransitionTo != "windows" ||
		b.FromVerdict != ResultPass || b.ToVerdict != ResultFail {
		t.Fatalf("boundary = %+v, want os linux PASS -> windows FAIL", b)
	}
	summary := BuildExperienceSummary(CLIExperienceCoordinate{Tool: "git", Subcommand: "sparse-checkout set"}, obs)
	if summary.Status != "COEXISTING_BOUNDARY" || len(summary.Boundaries) != 1 || summary.Boundaries[0].Axis != "os" {
		t.Fatalf("summary = %s %+v, want the os boundary in a COEXISTING_BOUNDARY summary", summary.Status, summary.Boundaries)
	}
}

func TestAnomalyReportAcceptsCLIExecutionEvidenceIDs(t *testing.T) {
	r := passFailReport()
	r.EvidenceID = "clievidence:sha256:" + hex64('b')
	r.RelatedIDs = []string{"cliobs:sha256:" + hex64('c'), "clievidence:sha256:" + hex64('d')}
	if err := r.Normalize().Validate(); err != nil {
		t.Fatalf("a clievidence evidence id was refused: %v (#359)", err)
	}
	obs := CLIExperienceObservation{Coordinate: CLIExperienceCoordinate{Tool: "git"}, Provenance: ProvenanceField, Result: ResultPass}
	r.EvidenceID = obs.EvidenceID()
	if err := r.Normalize().Validate(); err != nil {
		t.Fatalf("EvidenceID() output was refused: %v", err)
	}
	for _, bad := range []string{
		"clievidence:sha256:" + hex64('b')[:63],
		"clievidence:sha256:" + hex64('G'),
		"cliobs:sha256:" + hex64('b'), // an observation id is not an evidence id
		"anything:sha256:" + hex64('b'),
		"clievidence:/home/me/repo",
	} {
		r.EvidenceID = bad
		if err := r.Normalize().Validate(); err != ErrAnomalyIdentifier {
			t.Errorf("evidenceId %q: err = %v, want ErrAnomalyIdentifier", bad, err)
		}
	}
	if !IsNamespacedContentID("clisubject:sha256:"+hex64('e')) || IsNamespacedContentID("pkg:sha256:"+hex64('e')) {
		t.Error("IsNamespacedContentID must accept exactly the closed CSX namespaces")
	}
}
