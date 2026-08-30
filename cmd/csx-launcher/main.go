package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/launcher"
	"github.com/r2cuerdame/codesamplex/internal/update"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--launcher-version" {
		fmt.Println("csx-launcher " + launcher.ProtocolVersion)
		return
	}
	self, err := os.Executable()
	if err != nil {
		fail(launcher.ReasonPointerUnreadable, err)
	}
	root := filepath.Dir(self)
	if len(os.Args) == 2 && os.Args[1] == repairFlag {
		os.Exit(repairMain(root))
	}
	res, err := launcher.Resolve(root)
	if err != nil {
		res, err = repairAndResolve(root, err)
	}
	if err != nil {
		fail(launcher.Reason(err), err)
	}
	reportRecovery(root, res)
	code, err := runResolution(res, self, root)
	if err != nil {
		var startFailure *childStartError
		if errors.As(err, &startFailure) {
			// Resolve hashes before CreateProcess. Defender or an ACL change can
			// still remove/block the file in that gap, so retry one recorded LKG
			// exactly once. A normal non-zero payload exit never reaches here.
			res, err = launcher.RecoverAfterStartFailure(root, res.Descriptor, startFailure)
			if err != nil {
				res, err = repairAndResolve(root, err)
			}
			if err != nil {
				fail(launcher.Reason(err), err)
			}
			reportRecovery(root, res)
			code, err = runResolution(res, self, root)
		}
	}
	if err != nil {
		fail(launcher.ReasonPayloadStartFailed, err)
	}
	finish(code)
}

func runResolution(res launcher.Resolution, self, root string) (int, error) {
	cmd := exec.Command(res.PayloadPath, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = launcherEnv(os.Environ(), self, root, res.Descriptor, launcher.ProtocolVersion)
	return runChild(cmd)
}

func reportRecovery(root string, res launcher.Resolution) {
	if !res.Recovered {
		return
	}
	// Diagnostics go to stderr and nowhere else. An MCP host reads this
	// process's stdout as JSON-RPC framing, so a recovery note written there
	// would corrupt the very session the recovery exists to save.
	fmt.Fprintf(os.Stderr, "csx launcher: recovered: %s: current payload %s is unusable; running last-known-good %s\n",
		res.FailedReason, res.FailedVersion, res.Descriptor.Version)
	if !res.Healed {
		fmt.Fprintf(os.Stderr, "csx launcher: active pointer was not repaired: %v\n", res.HealError)
	}
	// That line is the only trace a recovery leaves, it goes to a log nobody
	// reads, and once the pointer is repaired no later run repeats it -- so the
	// install looks healthy again and a released payload that was destroyed on
	// this machine becomes invisible. The record survives instead, and
	// `csx update status` reads it back. Failing to write it must not stop the
	// command the user asked for: this path is already the damaged one.
	if err := launcher.RecordRecovery(root, res, time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "csx launcher: recovery evidence was not recorded: %v\n", err)
	}
}

func finish(code int) {
	// ExitCode reports -1 for a process that was signalled or never exited.
	// Passing that to os.Exit launders a failure into a platform-dependent
	// code, so it becomes the launcher's own stable one.
	if code < 0 {
		fail(launcher.ReasonPayloadStartFailed, fmt.Errorf("payload did not report an exit status (%d)", code))
	}
	if code != 0 {
		os.Exit(code)
	}
}

// childStartError separates an actual exec/CreateProcess failure from launcher
// setup failures. Only the former can truthfully mark this payload unusable and
// trigger an LKG retry; a job-object failure, for example, must not rewrite the
// active pointer.
type childStartError struct{ err error }

func (e *childStartError) Error() string { return e.err.Error() }
func (e *childStartError) Unwrap() error { return e.err }

func launcherEnv(env []string, launcherPath, root string, d launcher.Descriptor, protocol string) []string {
	out := make([]string, 0, len(env)+6)
	for _, item := range env {
		upper := strings.ToUpper(item)
		if strings.HasPrefix(upper, "CSX_LAUNCHER_") || strings.HasPrefix(upper, "CSX_ACTIVE_") || strings.HasPrefix(upper, "CSX_PAYLOAD_VERSION=") {
			continue
		}
		out = append(out, item)
	}
	return append(out, "CSX_LAUNCHER_PATH="+launcherPath, "CSX_LAUNCHER_ROOT="+root, "CSX_LAUNCHER_VERSION="+protocol,
		"CSX_PAYLOAD_VERSION="+d.Version, "CSX_ACTIVE_SEQUENCE="+strconv.FormatUint(d.Sequence, 10), "CSX_ACTIVE_SHA256="+d.SHA256)
}

// fail is the only way this launcher stops without having run a payload, and it
// always stops non-zero. A caller that cannot execute csx must never be able to
// read that as the command having succeeded -- on MCP stdio the whole failure
// is otherwise an empty stdout and a clean exit, which a host reports as a
// server that closed rather than one that could not start. The reason code
// leads the line so that message stays greppable across platforms.
func fail(reason string, err error) {
	fmt.Fprintf(os.Stderr, "csx launcher: %s: %v\n", reason, err)
	os.Exit(126)
}

// repairFlag is the explicit operator repair. It exists because the automatic
// path deliberately backs off after a failure, and because an operator looking
// at a dead install needs one command that says what it did rather than a
// retry loop that may or may not be in its cooldown.
const repairFlag = "--repair-payload"

// repairBudget bounds the whole repair, network included. An MCP host is
// waiting on this process; a repair that cannot finish inside it is reported as
// a failure the operator can act on, which is still better than a hang.
const repairBudget = 2 * time.Minute

// repairEnvOptOut disables the automatic repair entirely. Anything that wants
// a broken install to stay broken -- an air-gapped machine that should fail
// fast, a test that must not reach the network -- sets it.
const repairEnvOptOut = "CSX_LAUNCHER_NO_REPAIR"

// repairAttempted keeps the repair to one attempt per process, the same bound
// RecoverAfterStartFailure puts on the last-known-good retry.
var repairAttempted bool

// repairAndResolve is the launcher's last resort: the pointer is intact, but
// every payload it records is gone from this machine, so there is nothing left
// here to fall back to. It refetches those exact payloads from the official
// release path, holding them to the SHA-256 the pointer already recorded, and
// then resolves again.
//
// It returns the original resolve failure untouched when it cannot help, so a
// caller reading stderr still gets the same stable reason code first. A repair
// that silently converted "no payload" into some other error would take the one
// greppable fact an MCP host has and replace it with a story about the network.
func repairAndResolve(root string, resolveErr error) (launcher.Resolution, error) {
	if err := repairable(resolveErr); err != nil {
		return launcher.Resolution{}, fmt.Errorf("%w; automatic repair skipped: %v", resolveErr, err)
	}
	repairAttempted = true
	ctx, cancel := context.WithTimeout(context.Background(), repairBudget)
	defer cancel()
	// A payload that failed to START hashes correctly, so the repair has to be
	// told that is what happened; otherwise it looks at the bytes, finds them
	// fine, and declines the repair the launcher just classified as possible.
	report, err := update.RehydrateInstall(ctx, root, update.RehydrateOptions{
		StartFailed: launcher.Reason(resolveErr) == launcher.ReasonPayloadStartFailed,
	})
	if err != nil {
		return launcher.Resolution{}, fmt.Errorf("%w; automatic repair from the official release failed: %v", resolveErr, err)
	}
	// stderr only, for the same reason every other launcher diagnostic is:
	// stdout is an MCP host's JSON-RPC framing.
	fmt.Fprintf(os.Stderr, "csx launcher: repaired: %s had no verified fallback left; %s\n",
		report.ExhaustedVersion, refetchSummary(report))
	if report.FallbackVersion == "" {
		fmt.Fprintln(os.Stderr, "csx launcher: this install has no local fallback payload; the next lost payload needs the network again")
	}
	res, err := launcher.Resolve(root)
	if err != nil {
		return launcher.Resolution{}, fmt.Errorf("%w; the repaired payload did not resolve: %v", resolveErr, err)
	}
	return res, nil
}

// repairable decides whether refetching can address this failure at all. Only
// a payload whose bytes are wrong or gone is repairable from the release: a
// pointer that will not parse has no recorded digest to hold a download to, and
// a filesystem that refused to answer is not something a download fixes.
func repairable(resolveErr error) error {
	if repairAttempted {
		return errors.New("already attempted once in this process")
	}
	if os.Getenv(repairEnvOptOut) != "" {
		return errors.New("disabled by " + repairEnvOptOut)
	}
	switch launcher.Reason(resolveErr) {
	case launcher.ReasonPayloadMissing, launcher.ReasonPayloadCorrupt,
		launcher.ReasonPayloadNotRegular, launcher.ReasonPayloadStartFailed:
		return nil
	default:
		return fmt.Errorf("%s is not a refetchable failure", launcher.Reason(resolveErr))
	}
}

// repairMain is `csx.exe --repair-payload`: the same repair, asked for on
// purpose. It ignores the cooldown and reports on stdout, because here a person
// is the one reading.
func repairMain(root string) int {
	ctx, cancel := context.WithTimeout(context.Background(), repairBudget)
	defer cancel()
	report, err := update.RehydrateInstall(ctx, root, update.RehydrateOptions{Force: true})
	if err != nil {
		// No reason code here: the reason codes describe why a payload could
		// not be run, and this is a repair that could not be completed. Naming
		// one of them would put a fact on stderr that was never measured.
		fmt.Fprintf(os.Stderr, "csx launcher: payload repair failed: %v\n", err)
		return 1
	}
	if report.ExhaustedVersion == "" {
		fmt.Printf("nothing to repair: the current payload verifies; %s\n", refetchSummary(report))
	} else {
		fmt.Printf("repaired %s: %s\n", report.ExhaustedVersion, refetchSummary(report))
	}
	if len(report.AlreadyVerified) > 0 {
		fmt.Printf("already verified on disk: %s\n", strings.Join(report.AlreadyVerified, ", "))
	}
	if report.FallbackVersion == "" {
		fmt.Println("no local fallback payload: this pointer records no verified previous version")
	} else {
		fmt.Printf("verified fallback: %s\n", report.FallbackVersion)
	}
	return 0
}

// refetchSummary keeps an empty restore set readable. A repair can legitimately
// restore nothing -- an updater may have published a working payload while this
// process was deciding to act -- and "refetched  from the official release" is
// the kind of line that sends an operator looking for a bug that is not there.
func refetchSummary(report update.RehydrateReport) string {
	if len(report.Restored) == 0 {
		return "refetched nothing: every recorded payload was already verified on disk"
	}
	return "refetched " + strings.Join(report.Restored, ", ") + " from the official release"
}
