// Package defender asks Microsoft Defender what it thinks of a file, and
// reports the answer as an observation rather than as a grade.
//
// It exists because of a measured surprise. This project's Windows install
// lost its payload to Defender five times between 2026-08-24 and 2026-08-26
// (`Trojan:Win32/Bearfoos.A!ml`, ThreatID 2147731250), and the obvious
// explanations — an unsigned Go binary, a stripped PE, a self-updating
// executable in %LOCALAPPDATA% — all turned out to be untestable stories.
// What was testable was cheaper and stranger: the eleven ASCII bytes
// `old-payload`, written by this repository's own launcher test fixture, are
// classified as that same threat. Eleven bytes of lowercase text, no PE
// header, no code. `old-payloa` is clean, `old-payload2` is clean.
//
// The lesson is not about those bytes. It is that a Defender verdict is a
// fact you measure, never one you predict from what a file looks like — so
// the repository measures it, in the one place that can: on Windows, with
// Defender's own scanner.
//
// Two properties are deliberate:
//
//   - Scanning never remediates. Every scan passes -DisableRemediation, so
//     asking the question cannot quarantine the file being asked about. A
//     measurement that destroys its own subject is not a measurement.
//   - An absent scanner is ErrUnavailable, never a clean verdict. Reading
//     "Defender is not installed" as "Defender is happy" is exactly the
//     inversion this package exists to prevent.
//
// A clean verdict is scoped to the security intelligence version that
// produced it. The same bytes get different answers on different days:
// v0.1.39's payload was quarantined as Bearfoos.B!ml on 2026-08-25 and
// scanned clean on 2026-08-26. So Verdict carries the definition version
// that answered, and a caller that records a verdict without it has recorded
// an opinion with no date on it.
package defender

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnavailable means no verdict could be obtained: this is not Windows, or
// MpCmdRun.exe is not installed, or the platform directory Defender updates
// itself into could not be read. It is returned instead of an empty result so
// that "nothing was measured" can never be mistaken for "nothing was found".
var ErrUnavailable = errors.New("defender: Microsoft Defender command-line scanner is unavailable")

// Verdict is what Defender said about one file at one moment, with the
// security intelligence version that said it.
type Verdict struct {
	// Path is the file that was scanned.
	Path string
	// Flagged reports whether Defender named at least one threat.
	Flagged bool
	// Threats are the threat names Defender reported, in the order printed.
	Threats []string
	// Definitions is the security intelligence version that produced this
	// verdict, when Defender reported one. A verdict without it is still a
	// verdict; it just cannot be compared with one from another day.
	Definitions string
}

// String renders a verdict as one line, for logs and test failures.
func (v Verdict) String() string {
	state := "clean"
	if v.Flagged {
		state = "flagged " + strings.Join(v.Threats, ",")
	}
	if v.Definitions != "" {
		return fmt.Sprintf("%s: %s (definitions %s)", v.Path, state, v.Definitions)
	}
	return fmt.Sprintf("%s: %s", v.Path, state)
}
