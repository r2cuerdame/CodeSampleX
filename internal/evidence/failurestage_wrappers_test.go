package evidence

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// Windows launches npm, pnpm, yarn and gradlew through .cmd/.bat wrappers,
// often by absolute path. The outer command and the toolchain lineage are
// the same as for the bare name on any host (#339).
func TestSafeOuterCommandNormalizesWindowsWrappers(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"npm.cmd", "test"}, "npm test"},
		{[]string{"pnpm.cmd", "test"}, "pnpm test"},
		{[]string{"yarn.cmd", "build"}, "yarn build"},
		{[]string{"gradlew.bat", "test"}, "gradlew test"},
		{[]string{`C:\nodejs\pnpm.cmd`, "build"}, "pnpm build"},
		{[]string{`C:\Users\user\AppData\Roaming\npm\npm.cmd`, "test"}, "npm test"},
		{[]string{"/usr/local/bin/go", "test"}, "go test"},
		{[]string{"NPM.CMD"}, "npm"},
		{[]string{`C:\tools\private-tool.exe`, "test"}, ""},
	}
	for _, c := range cases {
		if got := safeOuterCommand(c.argv); got != c.want {
			t.Errorf("safeOuterCommand(%q) = %q, want %q", c.argv, got, c.want)
		}
	}

	analysis := AnalyzeFailure(scanner.CommandProfile{}, []string{`C:\nodejs\npm.cmd`, "test"},
		CommandOutput{Stdout: "build started\n[build failed]\n"})
	if analysis.OuterCommand != "npm test" {
		t.Fatalf("OuterCommand = %q, want npm test", analysis.OuterCommand)
	}
	if len(analysis.Events) != 1 || analysis.Events[0].Toolchain != "typescript/tsc" {
		t.Fatalf("events = %+v, want one compile event with the npm toolchain", analysis.Events)
	}
}
