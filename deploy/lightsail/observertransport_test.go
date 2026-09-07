package lightsail

import (
	"context"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// docker compose exec inherits stdin, even for psql -c. Exercise the actual
// observer transport with an equivalent stdin consumer before more script
// than a shell can buffer. The whole script must parse before that consumer
// runs, including when Windows prefixes the wire payload with a UTF-8 BOM.
func TestObservationTransportCannotConsumeItsOwnScript(t *testing.T) {
	observer := readDeployFixture(t, "observe-production.ps1")
	remote := regexp.MustCompile(`(?m)^\s*"(\{ printf '#'; cat;[^\r\n]+\| sh)"`).FindStringSubmatch(observer)
	prefix := regexp.MustCompile(`(?m)^\s*\$prefix = "([^\r\n]+)"`).FindStringSubmatch(observer)
	if len(remote) != 2 || len(prefix) != 2 {
		t.Fatal("cannot locate the actual observation transport and payload prefix")
	}
	body := "printf 'before\\n'\ncat >/dev/null\n" + strings.Repeat("# simulate a long collector script\n", 4096) + "printf 'after\\n'\n"
	wire := strings.NewReplacer("`n", "\n", "$mode", "0", "$detailMode", "0", "$ExpectedServerStartedAt", "2026-09-07T13:41:00Z").Replace(prefix[1]) + body
	sh, err := exec.LookPath("sh")
	if err != nil && runtime.GOOS == "windows" {
		sh, err = exec.LookPath(`C:\Program Files\Git\bin\sh.exe`)
	}
	if err != nil {
		t.Skip("POSIX shell is unavailable")
	}
	for _, bom := range []string{"", "\xef\xbb\xbf"} {
		t.Run(map[bool]string{true: "bom", false: "plain"}[bom != ""], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, sh, "-c", remote[1])
			cmd.Stdin = strings.NewReader(bom + wire)
			output, err := cmd.CombinedOutput()
			if err != nil || string(output) != "before\nafter\n" {
				t.Fatalf("observer lost source to child stdin: err=%v output=%q", err, output)
			}
		})
	}
}

func TestObservationRequiresAllConfiguredActiveRounds(t *testing.T) {
	observer := readDeployFixture(t, "observe-production.ps1")
	rounds := regexp.MustCompile(`(?m)^\$ActiveBuilderLatencyRounds = (\d+)$`).FindStringSubmatch(observer)
	condition := regexp.MustCompile(`(?m)^\s*if \((\$sample\.builder_fresh[^\r\n]+)\) \{`).FindStringSubmatch(observer)
	if len(rounds) != 2 || rounds[1] != "5" || len(condition) != 2 {
		t.Fatal("observer lost its configured five-round convergence gate")
	}
	if !strings.Contains(observer, "insufficient active-builder latency rounds:") || !strings.Contains(observer, "$evidence.activeBuilder.rounds -lt $ActiveBuilderLatencyRounds") {
		t.Fatal("terminal observation lacks an explicit insufficient-rounds failure")
	}
	ps, err := exec.LookPath("pwsh")
	if err != nil && runtime.GOOS == "windows" {
		ps, err = exec.LookPath("powershell")
	}
	if err != nil {
		t.Skip("PowerShell is unavailable")
	}
	program := "$ErrorActionPreference='Stop'\n$ActiveBuilderLatencyRounds=" + rounds[1] + "\n" +
		"$sample=@{builder_fresh=$true;builder_lifecycle_state='complete'}\n" +
		"foreach ($case in @(@(0,$false),@(1,$false),@(4,$false),@(5,$true),@(6,$true))) {\n" +
		"$evidence=@{activeBuilder=@{observed=$true;rounds=$case[0]}}\n" +
		"$allowed=(" + condition[1] + ")\n" +
		"if ($allowed -ne $case[1]) { throw 'wrong active-round convergence decision' }\n}\n"
	program += "$evidence=@{activeBuilder=@{observed=$true;rounds=5}}\n" +
		"$sample.builder_fresh=$false\nif (" + condition[1] + ") { throw 'stale builder accepted' }\n" +
		"$sample.builder_fresh=$true\n$sample.builder_lifecycle_state='start'\nif (" + condition[1] + ") { throw 'active builder accepted as complete' }\n'ok'\n"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-Command", program).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "ok" {
		t.Fatalf("observer convergence fixture: err=%v output=%s", err, output)
	}
}
