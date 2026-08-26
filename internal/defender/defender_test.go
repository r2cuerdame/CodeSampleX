package defender

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
)

// A verdict is only comparable with another one when it says which security
// intelligence build produced it. The same payload was quarantined as
// Bearfoos.B!ml on 2026-08-25 and scanned clean on 2026-08-26; without the
// definition version, those two records read as a contradiction instead of as
// two dated observations.
func TestVerdictNamesTheDefinitionsThatAnswered(t *testing.T) {
	v := Verdict{Path: `C:\csx\csx-payload.exe`, Flagged: true, Threats: []string{"Trojan:Win32/Bearfoos.A!ml"}, Definitions: "1.457.332.0"}
	line := v.String()
	for _, want := range []string{`C:\csx\csx-payload.exe`, "Trojan:Win32/Bearfoos.A!ml", "1.457.332.0"} {
		if !strings.Contains(line, want) {
			t.Errorf("String() = %q, missing %q", line, want)
		}
	}
	if clean := (Verdict{Path: "a"}).String(); !strings.Contains(clean, "clean") {
		t.Errorf("clean verdict rendered as %q", clean)
	}
}

// Off Windows there is no Defender to ask, and "no answer" must never be
// stored as "no threats".
func TestScanOffWindowsIsUnavailableRatherThanClean(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this asserts the non-Windows build's refusal")
	}
	verdicts, err := Scan(context.Background(), "anything")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if verdicts != nil {
		t.Fatalf("verdicts = %v, want none", verdicts)
	}
}

// Scanning nothing is not an error and not a verdict.
func TestScanWithNoPathsReturnsNothing(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the non-Windows build refuses before it counts paths")
	}
	verdicts, err := Scan(context.Background())
	if err != nil || verdicts != nil {
		t.Fatalf("Scan() = %v, %v; want nil, nil", verdicts, err)
	}
}
