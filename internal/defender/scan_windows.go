//go:build windows

package defender

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Defender updates itself into a versioned platform directory and leaves the
// copy under %ProgramFiles% behind as a stub, so the newest platform build is
// the one whose verdict a user's real-time protection would give. Falling back
// to the Program Files copy keeps this working on an install that has never
// taken a platform update.
const (
	platformRoot   = `C:\ProgramData\Microsoft\Windows Defender\Platform`
	fallbackVendor = `Windows Defender`
	scannerName    = "MpCmdRun.exe"
	definitionsKey = `SOFTWARE\Microsoft\Windows Defender\Signature Updates`
)

// MpCmdRun prints its own report in English even where the operating system is
// localized, but only the report: the OS error strings inside a `[Failed]` line
// arrive in the machine's language. So the clean and flagged cases are matched
// on Defender's own words and everything else is refused as unknown rather than
// read as clean.
var (
	noThreatsLine = regexp.MustCompile(`(?i)found no threats`)
	threatsLine   = regexp.MustCompile(`(?i)found\s+\d+\s+threats?`)
	threatName    = regexp.MustCompile(`(?im)^\s*Threat\s*:\s*(\S.*?)\s*$`)
	detectedList  = regexp.MustCompile(`(?i)LIST OF DETECTED THREATS`)
)

// Scan asks Defender about each path and returns one verdict per path, in the
// order given.
//
// Remediation is disabled for every scan: asking the question must not
// quarantine the subject, or a test that measures a fixture would delete the
// fixture it is measuring and the next run would fail for a different reason.
//
// An answer that cannot be classified is an error, never a clean verdict.
func Scan(ctx context.Context, paths ...string) ([]Verdict, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	scanner, err := locateScanner()
	if err != nil {
		return nil, err
	}
	definitions := definitionVersion()
	out := make([]Verdict, 0, len(paths))
	for _, p := range paths {
		v, err := scanOne(ctx, scanner, p, definitions)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func scanOne(ctx context.Context, scanner, path, definitions string) (Verdict, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Verdict{}, fmt.Errorf("defender: resolve %s: %w", path, err)
	}
	// Stat first. MpCmdRun answers a missing file with the same exit code it
	// uses for "threats found", so without this a deleted file would be
	// reported as malware and a typo would look like a detection.
	fi, err := os.Stat(abs)
	if err != nil {
		return Verdict{}, fmt.Errorf("defender: scan target %s: %w", abs, err)
	}
	if !fi.Mode().IsRegular() {
		return Verdict{}, fmt.Errorf("defender: scan target %s is not a regular file", abs)
	}

	cmd := exec.CommandContext(ctx, scanner, "-Scan", "-ScanType", "3", "-File", abs, "-DisableRemediation")
	raw, runErr := cmd.CombinedOutput()
	text := string(raw)

	v := Verdict{Path: abs, Definitions: definitions}
	switch {
	case noThreatsLine.MatchString(text):
		return v, nil
	case threatsLine.MatchString(text) || detectedList.MatchString(text):
		v.Flagged = true
		v.Threats = parseThreats(text)
		return v, nil
	}
	// Exit 0 with no report at all still means Defender finished without
	// naming anything; anything else is a scanner that did not answer, and
	// that is reported rather than absorbed.
	if runErr == nil {
		return v, nil
	}
	return Verdict{}, fmt.Errorf("defender: scan %s did not produce a verdict (%v): %s", abs, runErr, tail(text))
}

func parseThreats(text string) []string {
	seen := map[string]bool{}
	var names []string
	for _, m := range threatName.FindAllStringSubmatch(text, -1) {
		name := strings.TrimSpace(m[1])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	if len(names) == 0 {
		// Defender said it found threats but this build did not print them in
		// a shape this parser knows. "Flagged, name unknown" is the truthful
		// reading; inventing a name or dropping the flag are both worse.
		return []string{"unnamed-detection"}
	}
	return names
}

func tail(text string) string {
	text = strings.TrimSpace(text)
	const limit = 400
	if len(text) <= limit {
		return text
	}
	return "..." + text[len(text)-limit:]
}

func locateScanner() (string, error) {
	if pinned := strings.TrimSpace(os.Getenv("CSX_DEFENDER_MPCMDRUN")); pinned != "" {
		if fi, err := os.Stat(pinned); err == nil && fi.Mode().IsRegular() {
			return pinned, nil
		}
		return "", fmt.Errorf("%w: CSX_DEFENDER_MPCMDRUN=%s is not a file", ErrUnavailable, pinned)
	}
	if p, ok := newestPlatformScanner(); ok {
		return p, nil
	}
	for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramW6432")} {
		if base == "" {
			continue
		}
		p := filepath.Join(base, fallbackVendor, scannerName)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p, nil
		}
	}
	return "", ErrUnavailable
}

func newestPlatformScanner() (string, bool) {
	entries, err := os.ReadDir(platformRoot)
	if err != nil {
		return "", false
	}
	var versions []string
	for _, e := range entries {
		if e.IsDir() {
			versions = append(versions, e.Name())
		}
	}
	// Defender's platform directories are dotted version strings that sort
	// correctly enough as text for "pick the newest installed one", and the
	// existence check below is what actually decides.
	sort.Sort(sort.Reverse(sort.StringSlice(versions)))
	for _, v := range versions {
		p := filepath.Join(platformRoot, v, scannerName)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p, true
		}
	}
	return "", false
}

// definitionVersion stamps a verdict with the security intelligence build that
// produced it. It is best effort on purpose: a verdict with no date on it is
// still a verdict, and failing the scan because a registry key moved would
// trade a measurement for nothing.
func definitionVersion() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, definitionsKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("AVSignatureVersion")
	if err != nil {
		return ""
	}
	return v
}
