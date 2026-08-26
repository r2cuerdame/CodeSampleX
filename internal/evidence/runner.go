package evidence

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"context"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/sanitizer"
)

// The captured copy is bounded independently for stdout and stderr. The child
// still inherits both streams, so this limit affects only the structured
// result and sanitizer input, never what the user sees while it runs.
const (
	tailLines       = 200
	streamTailBytes = 256 << 10
)

// CommandOutput is the local copy of the command's two output streams. They
// stay separate so a recommendation can never replace or masquerade as the
// command's own diagnostics.
//
// StdoutTruncated / StderrTruncated are load-bearing: false is a claim that
// the tail IS the whole stream, and callers (MCP `run_observed_command` among
// them) show the capture unqualified when it is false. A false negative here
// presents a clipped log as a complete one.
type CommandOutput struct {
	Stdout          string
	Stderr          string
	StdoutTruncated bool
	StderrTruncated bool
	Termination     domain.FailureTermination
}

// FailureDiagnostics is the stream a failed command should be diagnosed and
// fingerprinted from.
//
// It was stderr, unconditionally, and half the toolchains an agent runs do
// not report there. tsc writes its diagnostics to stdout and exits non-zero
// with an empty stderr; so do go test, pytest, and most npm scripts.
// Sanitizing that empty stream still produced a fingerprint — the hash of a
// blank template — so a broken typecheck was RECORDED under, and SEARCHED
// for by, the hash of nothing at all. Nothing has ever failed that way, so
// the fingerprint matched nothing, and the free-text query left over was the
// word "fingerprint" and 64 hex digits. The engine answered it with whatever
// was nearest, which is how a failing TypeScript build came back with a Dart
// sample about package:crypto, graded COMPATIBLE, over a stdout the tool was
// holding the whole time.
//
// The rule, in order:
//
//  1. The stream naming an error code wins, stderr first. A code is what a
//     stored failure is keyed by, and a runner's own boilerplate ("npm error
//     Lifecycle script failed") names none while the compiler underneath it
//     names TS2352.
//  2. With no code anywhere, stderr if it said anything — so every command
//     that already reported there keeps the exact text, and the exact
//     fingerprint, it had before.
//  3. Otherwise stdout.
//
// A command that printed nothing at all diagnoses nothing, and says so by
// returning "". That is not a gap to paper over with a hash: an empty answer
// is what stops a question nobody can answer from being asked.
func (o CommandOutput) FailureDiagnostics() string {
	if sanitizer.ErrorCode(o.Stderr) != "" {
		return o.Stderr
	}
	if sanitizer.ErrorCode(o.Stdout) != "" {
		return o.Stdout
	}
	if strings.TrimSpace(o.Stderr) != "" {
		return o.Stderr
	}
	return o.Stdout
}

// Run spawns argv[0] with the remaining args in dir, inheriting stdio so
// the wrapped command behaves exactly as if run directly. stdout and stderr
// are additionally teed into separate bounded in-memory tails (capture only;
// the caller sanitizes stderr before anything is stored in an uploadable row).
// stderr is also appended raw to $CSX_HOME/logs/last-run.log, which never
// leaves the machine.
//
// The child's exit code passes through: a non-zero child exit is NOT an
// error here (exitCode carries it, err stays nil). err is reserved for
// failures to run at all (command not found, ...).
func Run(ctx context.Context, argv []string, dir string) (exitCode int, output CommandOutput, err error) {
	if len(argv) == 0 {
		return -1, CommandOutput{}, errors.New("evidence: empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var timeoutMillis int64
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			timeoutMillis = remaining.Milliseconds()
		}
	}
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	stdoutRing := newLineRing(tailLines)
	stderrRing := newLineRing(tailLines)
	cmd.Stdout = newStreamTee(stdoutRing, os.Stdout)

	stderrThrough := []io.Writer{os.Stderr}
	if f := openRunLog(); f != nil {
		defer f.Close()
		stderrThrough = append(stderrThrough, f)
	}
	cmd.Stderr = newStreamTee(stderrRing, stderrThrough...)

	runErr := cmd.Run()
	output = CommandOutput{
		Stdout:          stdoutRing.Tail(),
		Stderr:          stderrRing.Tail(),
		StdoutTruncated: stdoutRing.Truncated(),
		StderrTruncated: stderrRing.Truncated(),
	}
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				output.Termination = domain.FailureTermination{Kind: domain.TerminationTimeout, TimeoutMillis: timeoutMillis}
				return ee.ExitCode(), output, nil
			}
			if signal := processSignal(ee.ProcessState); signal != "" {
				output.Termination = domain.FailureTermination{Kind: domain.TerminationSignal, Signal: signal}
				return ee.ExitCode(), output, nil
			}
			code := ee.ExitCode()
			output.Termination = domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &code}
			return code, output, nil
		}
		output.Termination = domain.FailureTermination{Kind: domain.TerminationProcessStartFailed}
		return -1, output, runErr
	}
	return 0, output, nil
}

// openRunLog opens $CSX_HOME/logs/last-run.log truncated for this run.
// Local diagnostics only; any failure just disables the log.
func openRunLog() *os.File {
	home, err := config.Home()
	if err != nil {
		return nil
	}
	logs := filepath.Join(home, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(logs, "last-run.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	return f
}

// streamTee copies one of the child's streams into a bounded capture and,
// best effort, on to the writers the user actually sees. One tee per stream,
// written only by that stream's exec copier goroutine, so it needs no lock.
//
// It replaces io.MultiWriter, whose two behaviours are both wrong here:
//
//  1. MultiWriter writes in order and returns at the first error, so with the
//     terminal ahead of the capture, a terminal that stops accepting output
//     stops the capture too — quietly, mid-stream.
//
//  2. os/exec's copier ends the copy at the first write error. A copy that
//     ends early leaves a capture that is short but no longer LOOKS short:
//     the ring can only account for what it was handed, so it goes on
//     reporting a clipped tail as the complete stream.
//
//     Cmd.Wait then reports that write error, but only when the child exited
//     0 — a non-zero exit takes precedence, so it wins the race exactly when
//     nothing is wrong with the command. Run reads any non-ExitError as "the
//     command never ran", so a build that passed gets reported as one that
//     never started, and nothing is recorded for it.
//
// So the capture is written first and unconditionally, and Write never
// reports an error. A broken passthrough degrades what the user sees; it must
// not be able to degrade the record of what happened.
type streamTee struct {
	capture *lineRing
	through []io.Writer
	dead    []bool
	failed  bool
}

func newStreamTee(capture *lineRing, through ...io.Writer) *streamTee {
	return &streamTee{capture: capture, through: through, dead: make([]bool, len(through))}
}

func (t *streamTee) Write(p []byte) (int, error) {
	t.capture.Write(p) //nolint:errcheck // lineRing cannot fail
	for i, w := range t.through {
		if t.dead[i] {
			continue
		}
		n, err := w.Write(p)
		if err != nil || n != len(p) {
			// Once a writer has refused a chunk, retrying it for every
			// remaining chunk of a megabyte-scale stream buys nothing.
			t.dead[i] = true
			t.failed = true
		}
	}
	return len(p), nil
}

// PassthroughFailed reports whether a user-facing writer stopped accepting
// output during the run. The capture is unaffected by it; this is only about
// what reached the terminal or the local log.
func (t *streamTee) PassthroughFailed() bool { return t.failed }

// lineRing keeps a byte-bounded tail and trims it to the last max lines when
// rendered. Each instance has one writer (one exec stream copier goroutine).
type lineRing struct {
	max       int
	maxBytes  int
	buf       []byte
	truncated bool
}

func newLineRing(max int) *lineRing { return &lineRing{max: max, maxBytes: streamTailBytes} }

func (r *lineRing) Write(p []byte) (int, error) {
	n := len(p)
	if n >= r.maxBytes {
		// Only the last maxBytes of p survive. Anything already buffered is
		// dropped with it — but a single write that exactly fills an empty
		// ring loses nothing, and must not claim it did.
		if len(r.buf) > 0 || n > r.maxBytes {
			r.truncated = true
		}
		r.buf = append(r.buf[:0], p[n-r.maxBytes:]...)
		return n, nil
	}
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.maxBytes {
		drop := len(r.buf) - r.maxBytes
		copy(r.buf, r.buf[drop:])
		r.buf = r.buf[:r.maxBytes]
		r.truncated = true
	}
	return n, nil
}

// window returns the last max lines held, and whether older lines had to be
// dropped to get there.
func (r *lineRing) window() ([]string, bool) {
	text := strings.TrimSuffix(string(r.buf), "\n")
	lines := strings.Split(text, "\n")
	dropped := len(lines) > r.max
	if dropped {
		lines = lines[len(lines)-r.max:]
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = strings.TrimSuffix(line, "\r")
	}
	return out, dropped
}

// Tail renders the buffered lines, including a trailing partial line. It is a
// pure read. The line cap it applies used to set the truncation flag as a side
// effect, which left the flag correct only for callers that happened to render
// the tail before asking — an ordering nothing in the type enforced.
func (r *lineRing) Tail() string {
	lines, _ := r.window()
	return strings.Join(lines, "\n")
}

// Truncated reports whether anything the child wrote is missing from Tail():
// dropped at write time by the byte cap, or trimmed now by the line cap.
// false is a promise that the tail is the whole stream.
func (r *lineRing) Truncated() bool {
	if r.truncated {
		return true
	}
	_, dropped := r.window()
	return dropped
}
