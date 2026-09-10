package lightsail

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Builder convergence is operationally important, but it is not a smoke
// test. A healthy, correctly identified deployment must not sit inside the
// rollback transaction for the duration of a full corpus rebuild.
func TestDeploySuccessDoesNotWaitForBuilderFreshness(t *testing.T) {
	deploy := readDeployFixture(t, "deploy.ps1")
	wrapper := readDeployFixture(t, "deploy-production.ps1")

	for _, forbidden := range []string{
		"builderFreshPollAttempts",
		"builderFreshPollSeconds",
		"collectBuilderFreshScript",
		"did not complete a fresh full builder pass",
	} {
		if strings.Contains(deploy, forbidden) {
			t.Errorf("deploy critical path still contains builder wait %q", forbidden)
		}
	}
	if strings.Contains(wrapper, `$after.builder_fresh -ne "true"`) {
		t.Fatal("production wrapper still rejects a safe deployment while builderFresh is false")
	}
	for _, required := range []string{
		`$evidence.conclusion = "success"`,
	} {
		if !strings.Contains(wrapper, required) {
			t.Errorf("deploy evidence no longer records the lightweight success boundary: missing %q", required)
		}
	}
}

// The wait moved rather than disappeared. The observer has its own bounded
// budget and a hard failure path, so a builder that never converges is visible
// on the tracking issue without rolling back an otherwise safe deployment.
func TestPostDeployObserverAlertsWhenBuilderNeverConverges(t *testing.T) {
	observer := readDeployFixture(t, "observe-production.ps1")
	for _, required := range []string{
		`$BuilderPollAttempts = 240`,
		`$BuilderPollSeconds = 20`,
		`$ActiveBuilderLatencyRounds = 5`,
		`$MaxActiveBuilderTTFBSeconds = 10.0`,
		`$MaxPressureWaitSeconds = 3.0`,
		`Start-Sleep -Seconds $BuilderPollSeconds`,
		`builder did not converge`,
		`conclusion = "failure"`,
		`Write-ObservationEvidence`,
		`server restart or exit detected during observation`,
		`server OOM detected during observation`,
		`builder error detected during observation`,
		`rollback detected: configured revision returned to the previous production SHA`,
		`Latency while builder work was active`,
		`no latency sample was captured while builder work was active`,
		`query timeouts were observed during builder convergence`,
		`pool-busy refusals were observed during builder convergence`,
		`maximum DB-pressure wait exceeded`,
		`/golang/github.com/jackc/pgx/v5/v5.10.0`,
		`/v1/wanted`,
		`/samples/sha256:13f4bcf31db6296c4d9325831f69e508e320520ab70dd6b2d237a11557c9fe9a`,
		`Settled unbalanced cluster rows`,
	} {
		if !strings.Contains(observer, required) {
			t.Errorf("post-deploy observer does not fail closed on convergence: missing %q", required)
		}
	}
}

func TestPostDeployObserverBoundsReplacementExitEvents(t *testing.T) {
	collector := readDeployFixture(t, "collect-post-deploy-observation.sh")
	observer := readDeployFixture(t, "observe-production.ps1")
	for _, required := range []string{
		`--filter event=die --format '{{.Time}}'`,
		`die_event_first_epoch`,
		`die_event_last_epoch`,
	} {
		if !strings.Contains(collector, required) {
			t.Errorf("post-deploy collector cannot attribute replacement exits: missing %q", required)
		}
		if !strings.Contains(observer, `'`+required+`'`) && required != `--filter event=die --format '{{.Time}}'` {
			t.Errorf("post-deploy evidence parser omits replacement exit bound %q", required)
		}
	}
}

func TestPostDeployObserverMeasuresTTFBDuringActiveBuilderWork(t *testing.T) {
	observer := readDeployFixture(t, "observe-production.ps1")
	collector := readDeployFixture(t, "collect-post-deploy-observation.sh")

	for _, required := range []string{
		`$sample = Read-ObservationSample $false $false`,
		`$sample.builder_active -and $evidence.activeBuilder.rounds -lt $ActiveBuilderLatencyRounds`,
		`$latencySample = Read-ObservationSample $true $false`,
		`if ($latencySample.builder_active)`,
		`Add-RouteLatencyEvidence $evidence $latencySample 'active-builder'`,
		`$sample.builder_lifecycle_state -eq 'complete'`,
		`http503Count`,
		`maxTTFBSeconds`,
	} {
		if !strings.Contains(observer, required) {
			t.Errorf("observer does not retain active-builder HTTP evidence: missing %q", required)
		}
	}
	for _, required := range []string{
		`%{time_starttransfer}`,
		`probe_latency landing /`,
		`probe_latency wanted /v1/wanted '"schemaVersion":1'`,
		`probe_latency package /golang/github.com/jackc/pgx/v5/v5.10.0`,
		`probe_latency sample /samples/sha256:13f4bcf31db6296c4d9325831f69e508e320520ab70dd6b2d237a11557c9fe9a`,
		`latency_%s_content_valid=%s`,
		`grep -E "$builder_lifecycle_pattern"`,
		`grep -Ec "$builder_error_pattern"`,
		`[ "$builder_lifecycle_before" = "$builder_lifecycle_after" ]`,
		`builder_lifecycle_state=complete`,
		`builder_lifecycle_state=error`,
		`builder_error_events=`,
		`max_pressure_wait_seconds=`,
	} {
		if !strings.Contains(collector, required) {
			t.Errorf("collector does not produce required active-builder evidence: missing %q", required)
		}
	}
	if strings.Contains(collector, `%{time_total}`) {
		t.Fatal("production latency probe reports total duration instead of TTFB")
	}
}

func TestBuilderEmitsAnObservablePassStart(t *testing.T) {
	builder := readDeployFixture(t, "../../internal/compatibility/builder.go")
	if !strings.Contains(builder, `compatibility: builder pass start full=%t since=%s`) {
		t.Fatal("post-deploy acceptance cannot prove that latency was sampled during active builder work")
	}
}

func TestCollectorRecognizesActualBuilderLifecycleFormats(t *testing.T) {
	collector := readDeployFixture(t, "collect-post-deploy-observation.sh")
	lifecyclePattern := collectorPattern(t, collector, "builder_lifecycle_pattern")
	errorPattern := collectorPattern(t, collector, "builder_error_pattern")
	if got := strings.Count(collector, `grep -E "$builder_lifecycle_pattern"`); got != 2 {
		t.Fatalf("collector must use its lifecycle pattern before and after probes; got %d uses", got)
	}
	if got := strings.Count(collector, `grep -Ec "$builder_error_pattern"`); got != 1 {
		t.Fatalf("collector must count failures with its error pattern; got %d uses", got)
	}

	lifecycleRE := regexp.MustCompile(lifecyclePattern)
	errorRE := regexp.MustCompile(errorPattern)
	seen := map[string]bool{}
	errorCount := 0
	for _, format := range builderLifecycleLogFormats(t) {
		message := materializeBuilderLogFormat(t, format)
		if !lifecycleRE.MatchString(message) {
			t.Errorf("collector lifecycle regex does not select builder format %q", format)
		}

		wantState := ""
		switch {
		case strings.HasPrefix(format, "compatibility: builder pass start "):
			wantState = "start"
			seen["start"] = true
		case strings.HasPrefix(format, "compatibility: builder pass complete "):
			wantState = "complete"
			seen["complete"] = true
		case strings.HasPrefix(format, "compatibility: builder run failed: "):
			wantState = "error"
			seen["retry"] = seen["retry"] || strings.Contains(format, "background retry %d/%d")
		case strings.HasPrefix(format, "compatibility: builder run failed after "):
			wantState = "error"
			seen["deferred"] = seen["deferred"] ||
				(strings.Contains(format, "after %d retries") &&
					strings.Contains(format, "state=failed/deferred"))
		default:
			t.Fatalf("builder added an unclassified lifecycle format %q", format)
		}

		gotState, gotActive := collectorClassification(collector, message)
		if gotState != wantState {
			t.Errorf("collector classified builder format %q as %q, want %q", format, gotState, wantState)
		}
		isError := errorRE.MatchString(message)
		if isError {
			errorCount++
		}
		if got, want := isError, wantState == "error"; got != want {
			t.Errorf("collector error count classification for %q = %t, want %t", format, got, want)
		}
		if got, want := gotActive, wantState == "start"; got != want {
			t.Errorf("collector active classification for %q = %t, want %t", format, got, want)
		}
	}
	if errorCount != 2 {
		t.Errorf("collector counted %d actual builder error formats, want 2", errorCount)
	}
	for _, required := range []string{"start", "complete", "retry", "deferred"} {
		if !seen[required] {
			t.Errorf("builder source no longer proves the %s lifecycle format", required)
		}
	}

	legacy := "compatibility: builder run: legacy failure"
	legacyState, _ := collectorClassification(collector, legacy)
	if !lifecycleRE.MatchString(legacy) || !errorRE.MatchString(legacy) ||
		legacyState != "error" {
		t.Fatal("collector no longer recognizes the legacy builder failure format")
	}
}

func TestBuilderCancellationLeavesLifecycleToExitEvidence(t *testing.T) {
	builder := readDeployFixture(t, "../../internal/compatibility/builder.go")
	if !strings.Contains(builder, "func runBuilderLoopWith(") {
		t.Fatal("builder source no longer contains runBuilderLoopWith")
	}
	cancellationReturn := regexp.MustCompile(`(?s)err := runBoundedPass\(ctx, passTimeout, budget, run\)\s+if ctx\.Err\(\) != nil \{\s*return\s*\}`)
	if !cancellationReturn.MatchString(builder) {
		t.Fatal("builder cancellation path must return without inventing a terminal lifecycle marker")
	}
	// The pass now runs under its own deadline, so there are two ways a pass
	// can end early and only one of them is this process stopping. The
	// ceiling must defer to the caller's context: wrapping a shutdown as a
	// breached ceiling would hand the collector a terminal builder error on
	// every ordinary replacement.
	if !strings.Contains(builder, "func runBoundedPass(") {
		t.Fatal("builder source no longer contains runBoundedPass")
	}
	ceilingDefersToShutdown := regexp.MustCompile(
		`(?s)func runBoundedPass\(.*?if ctx\.Err\(\) != nil \|\| !errors\.Is\(passCtx\.Err\(\), context\.DeadlineExceeded\) \{\s*return err\s*\}`)
	if !ceilingDefersToShutdown.MatchString(builder) {
		t.Fatal("builder pass ceiling must defer to caller cancellation instead of reporting a breached ceiling")
	}

	collector := readDeployFixture(t, "collect-post-deploy-observation.sh")
	for _, required := range []string{
		"restart_events", "die_events", "die_event_first_epoch", "die_event_last_epoch",
	} {
		if !strings.Contains(collector, required) {
			t.Errorf("collector lost cancellation/replacement bound %q", required)
		}
	}
}

func builderLifecycleLogFormats(t *testing.T) []string {
	t.Helper()
	builder := readDeployFixture(t, "../../internal/compatibility/builder.go")
	printfFormat := regexp.MustCompile(`log\.Printf\(("(?:[^"\\]|\\.)*")`)
	var formats []string
	for _, match := range printfFormat.FindAllStringSubmatch(builder, -1) {
		format, unquoteErr := strconv.Unquote(match[1])
		if unquoteErr != nil {
			t.Fatal(unquoteErr)
		}
		if strings.HasPrefix(format, "compatibility: builder ") {
			formats = append(formats, format)
		}
	}
	if len(formats) == 0 {
		t.Fatal("builder source contains no observable lifecycle formats")
	}
	return formats
}

func materializeBuilderLogFormat(t *testing.T, format string) string {
	t.Helper()
	printfDirective := regexp.MustCompile(`%[tdsv]`)
	message := printfDirective.ReplaceAllStringFunc(format, func(directive string) string {
		return map[string]string{"%t": "true", "%d": "5", "%s": "1s", "%v": "boom"}[directive]
	})
	if strings.Contains(message, "%") {
		t.Fatalf("unsupported printf directive in builder format %q", format)
	}
	return message
}

func collectorPattern(t *testing.T, collector, name string) string {
	t.Helper()
	assignment := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `='([^']+)'\r?$`)
	match := assignment.FindStringSubmatch(collector)
	if len(match) != 2 {
		t.Fatalf("collector does not define %s as a single-quoted regex", name)
	}
	if _, err := regexp.Compile(match[1]); err != nil {
		t.Fatalf("collector %s is not a valid regex: %v", name, err)
	}
	return match[1]
}

func collectorClassification(collector, message string) (string, bool) {
	literalPattern := regexp.MustCompile(`'([^']+)'`)
	statePattern := regexp.MustCompile(`builder_lifecycle_state=([a-z]+)`)
	for _, line := range strings.Split(collector, "\n") {
		state := statePattern.FindStringSubmatch(line)
		if len(state) != 2 {
			continue
		}
		for _, literal := range literalPattern.FindAllStringSubmatch(line, -1) {
			if strings.Contains(message, literal[1]) {
				return state[1], strings.Contains(line, "builder_active=true")
			}
		}
	}
	return "race", false
}
