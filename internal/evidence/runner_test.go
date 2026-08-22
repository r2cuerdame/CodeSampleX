package evidence

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/scanner"

	nodeadapter "github.com/r2cuerdame/codesamplex/adapters/node"
)

func hasNode() bool {
	_, err := exec.LookPath("node")
	return err == nil
}

// passCmd returns a command that exits 0: node when available, otherwise
// a shell builtin.
func passCmd() []string {
	if hasNode() {
		return []string{"node", "--version"}
	}
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/c", "exit", "0"}
	}
	return []string{"sh", "-c", "exit 0"}
}

func TestRunPassthroughAndLocalLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)

	argv := passCmd()
	code, _, err := Run(context.Background(), argv, t.TempDir())
	if err != nil {
		t.Fatalf("Run(%v): %v", argv, err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(home, "logs", "last-run.log")); err != nil {
		t.Fatalf("last-run.log not written: %v", err)
	}

	// The fallback command is one no adapter classifies (contract C14:
	// unknown commands record USED only).
	res := &scanner.ScanResult{Adapters: []scanner.Adapter{nodeadapter.Adapter{}}}
	if p := res.Classify([]string{"cmd", "/c", "exit", "0"}); p.Known {
		t.Fatalf("cmd /c exit 0 classified as %v, want unknown", p)
	}
	if hasNode() {
		if p := res.Classify(argv); !p.Known {
			t.Fatalf("node command not classified: %v", p)
		}
	}
}

func TestRunExitCodePassthrough(t *testing.T) {
	t.Setenv("CSX_HOME", t.TempDir())
	var argv []string
	switch {
	case hasNode():
		argv = []string{"node", "-e", "process.exit(3)"}
	case runtime.GOOS == "windows":
		argv = []string{"cmd", "/c", "exit", "3"}
	default:
		argv = []string{"sh", "-c", "exit 3"}
	}
	code, _, err := Run(context.Background(), argv, t.TempDir())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
}

func TestRunCapturesStderrTail(t *testing.T) {
	if !hasNode() {
		t.Skip("node not installed")
	}
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)

	argv := []string{"node", "-e", `console.error("boom-line-1"); console.error("boom-line-2"); process.exit(2)`}
	code, output, err := Run(context.Background(), argv, t.TempDir())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(output.Stderr, "boom-line-1") || !strings.Contains(output.Stderr, "boom-line-2") {
		t.Fatalf("stderr tail missing lines: %q", output.Stderr)
	}
	raw, err := os.ReadFile(filepath.Join(home, "logs", "last-run.log"))
	if err != nil {
		t.Fatalf("read last-run.log: %v", err)
	}
	if !strings.Contains(string(raw), "boom-line-1") {
		t.Fatalf("raw log missing stderr: %q", raw)
	}
}

func TestRunCapturesStdoutAndStderrSeparately(t *testing.T) {
	if !hasNode() {
		t.Skip("node not installed")
	}
	t.Setenv("CSX_HOME", t.TempDir())
	// exitCode rather than exit(4), for the reason spelled out in
	// TestRunBoundsVeryLargeStreamsAndMarksTruncation: process.exit() drops
	// queued asynchronous pipe writes. Two short lines almost always win that
	// race, which makes it a flake that waits for a loaded machine rather than
	// a bug anyone would find.
	argv := []string{"node", "-e", `console.log("ordinary-output"); console.error("actual-error"); process.exitCode=4`}
	code, output, err := Run(context.Background(), argv, t.TempDir())
	if err != nil || code != 4 {
		t.Fatalf("Run: code=%d err=%v", code, err)
	}
	if !strings.Contains(output.Stdout, "ordinary-output") || strings.Contains(output.Stdout, "actual-error") {
		t.Errorf("stdout = %q", output.Stdout)
	}
	if !strings.Contains(output.Stderr, "actual-error") || strings.Contains(output.Stderr, "ordinary-output") {
		t.Errorf("stderr = %q", output.Stderr)
	}
}

func TestRunBoundsVeryLargeStreamsAndMarksTruncation(t *testing.T) {
	if !hasNode() {
		t.Skip("node not installed")
	}
	t.Setenv("CSX_HOME", t.TempDir())
	// process.exitCode, NOT process.exit(5). On POSIX a child's stdout and
	// stderr are PIPES here (exec builds them because the writers are not
	// *os.File), and node's writes to a pipe are asynchronous: process.exit()
	// discards whatever is still queued. A megabyte does not survive that, and
	// how much does is a race with whoever drains the pipe — this test read a
	// tail ending at line 139 on one machine and line 40 on a CI runner, both
	// green on Windows, where pipe writes are synchronous and all 500 lines
	// always arrive. Setting exitCode lets the loop drain and still exits 5.
	argv := []string{"node", "-e", `for(let i=0;i<500;i++){console.log("out-"+i+"-"+"x".repeat(2000));console.error("err-"+i+"-"+"y".repeat(2000))};process.exitCode=5`}
	code, output, err := Run(context.Background(), argv, t.TempDir())
	if err != nil || code != 5 {
		t.Fatalf("Run: code=%d err=%v", code, err)
	}
	// Say which of the two failures this is. A flag that is false while the
	// tail sits at the cap is csx losing the truncation; a flag that is false
	// with a short tail means the child never delivered the megabyte, which is
	// what process.exit() used to do here and is not this package's bug.
	for _, s := range []struct {
		name      string
		tail      string
		truncated bool
	}{{"stdout", output.Stdout, output.StdoutTruncated}, {"stderr", output.Stderr, output.StderrTruncated}} {
		if s.truncated {
			continue
		}
		if len(s.tail) < streamTailBytes/2 {
			t.Fatalf("%s: the child delivered only %d bytes of ~1MB, so nothing was capped; "+
				"the capture is honest and the stream never arrived", s.name, len(s.tail))
		}
		t.Fatalf("%s: %d bytes captured against a %d cap, yet truncated = false",
			s.name, len(s.tail), streamTailBytes)
	}
	if len(output.Stdout) > streamTailBytes || len(output.Stderr) > streamTailBytes {
		t.Fatalf("bounded tails grew too large: stdout=%d stderr=%d", len(output.Stdout), len(output.Stderr))
	}
	if !strings.Contains(output.Stdout, "out-499-") || !strings.Contains(output.Stderr, "err-499-") {
		t.Fatal("the final diagnostics were not retained")
	}
}

func TestRunCommandNotFound(t *testing.T) {
	t.Setenv("CSX_HOME", t.TempDir())
	_, _, err := Run(context.Background(), []string{"definitely-not-a-real-command-xyz"}, t.TempDir())
	if err == nil {
		t.Fatal("Run of a missing command returned nil error")
	}
}

func TestLineRingBounds(t *testing.T) {
	r := newLineRing(200)
	for i := 1; i <= 250; i++ {
		fmt.Fprintf(r, "line-%d\n", i)
	}
	lines := strings.Split(r.Tail(), "\n")
	if len(lines) != 200 {
		t.Fatalf("ring holds %d lines, want 200", len(lines))
	}
	if lines[0] != "line-51" || lines[199] != "line-250" {
		t.Fatalf("ring window wrong: first=%q last=%q", lines[0], lines[199])
	}
	if !r.Truncated() {
		t.Error("dropping old lines did not mark the tail truncated")
	}

	// Partial final line is included.
	r2 := newLineRing(3)
	r2.Write([]byte("a\nb\npartial"))
	if got := r2.Tail(); got != "a\nb\npartial" {
		t.Fatalf("Tail() = %q", got)
	}
}

// bigLine returns one ~2KB output line, the shape the large-stream tests use.
func bigLine(prefix string, i int, fill byte) []byte {
	return []byte(fmt.Sprintf("%s-%d-%s\n", prefix, i, strings.Repeat(string(fill), 2000)))
}

// A stream over the byte cap must report itself truncated whether or not the
// tail was rendered first. The flag used to be a side effect of Tail(), so
// asking in the other order answered false — and a caller that only wants the
// flag is exactly the caller a false negative misleads.
func TestLineRingByteCapTruncationIsOrderIndependent(t *testing.T) {
	for _, tailFirst := range []bool{true, false} {
		r := newLineRing(tailLines)
		for i := 0; i < 500; i++ {
			r.Write(bigLine("out", i, 'x'))
		}
		if tailFirst {
			_ = r.Tail()
		}
		if !r.Truncated() {
			t.Errorf("tailFirst=%v: byte cap exceeded but Truncated() = false", tailFirst)
		}
		if len(r.buf) > streamTailBytes {
			t.Errorf("tailFirst=%v: buffer grew to %d, cap is %d", tailFirst, len(r.buf), streamTailBytes)
		}
	}
}

// Overflowing only the line cap — well under the byte cap — must also be
// visible from Truncated() alone.
func TestLineRingLineCapOnlyMarksTruncated(t *testing.T) {
	r := newLineRing(tailLines)
	for i := 1; i <= tailLines+50; i++ {
		fmt.Fprintf(r, "line-%d\n", i)
	}
	if len(r.buf) >= streamTailBytes {
		t.Fatalf("fixture exceeded the byte cap (%d bytes); it must isolate the line cap", len(r.buf))
	}
	if !r.Truncated() {
		t.Error("line cap exceeded but Truncated() = false before Tail()")
	}
	lines := strings.Split(r.Tail(), "\n")
	if len(lines) != tailLines || lines[len(lines)-1] != fmt.Sprintf("line-%d", tailLines+50) {
		t.Errorf("tail = %d lines ending %q", len(lines), lines[len(lines)-1])
	}
	if !r.Truncated() {
		t.Error("Truncated() = false after Tail()")
	}
}

// Under both caps nothing was dropped, and the flag must not claim otherwise
// — a false positive would have every ordinary run advertise a partial log.
func TestLineRingUnderCapsIsNotTruncated(t *testing.T) {
	r := newLineRing(tailLines)
	for i := 1; i <= tailLines; i++ {
		fmt.Fprintf(r, "line-%d\n", i)
	}
	if r.Truncated() {
		t.Error("Truncated() = true with nothing dropped")
	}
	if _ = r.Tail(); r.Truncated() {
		t.Error("Tail() turned an untruncated ring truncated")
	}

	// A single write that exactly fills an empty ring also loses nothing.
	exact := newLineRing(tailLines)
	exact.Write(make([]byte, streamTailBytes))
	if exact.Truncated() {
		t.Error("a write of exactly the byte cap reported truncation")
	}
}

// brokenWriter accepts a fixed number of chunks and then fails, standing in
// for a terminal or log that goes away mid-run.
type brokenWriter struct {
	accept int
	seen   int
	short  bool // return a short count with a nil error instead of an error
}

func (b *brokenWriter) Write(p []byte) (int, error) {
	b.seen++
	if b.seen > b.accept {
		if b.short {
			return len(p) / 2, nil
		}
		return 0, errors.New("passthrough is gone")
	}
	return len(p), nil
}

// A passthrough that dies mid-stream must not shorten the capture, and must
// not be able to report the shortened capture as complete.
func TestStreamTeeCaptureSurvivesPassthroughFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		short bool
	}{{"error", false}, {"short write", true}} {
		t.Run(tc.name, func(t *testing.T) {
			ring := newLineRing(tailLines)
			broken := &brokenWriter{accept: 2, short: tc.short}
			tee := newStreamTee(ring, broken)
			for i := 0; i < 500; i++ {
				n, err := tee.Write(bigLine("out", i, 'x'))
				if err != nil {
					t.Fatalf("write %d: tee reported %v; exec would end the copy here", i, err)
				}
				if want := len(bigLine("out", i, 'x')); n != want {
					t.Fatalf("write %d: n = %d, want %d", i, n, want)
				}
			}
			if !tee.PassthroughFailed() {
				t.Error("a failing passthrough went unnoticed")
			}
			if !ring.Truncated() {
				t.Error("capture over the byte cap reported truncated = false")
			}
			if !strings.Contains(ring.Tail(), "out-499-") {
				t.Error("capture stopped when the passthrough did")
			}
		})
	}
}

// The same thing through the real os/exec copier: a broken passthrough must
// leave both the capture and the exit status intact. Before this, the write
// error reached cmd.Wait() and Run turned a command that ran into one it
// reported as never started.
func TestStreamTeeBrokenPassthroughKeepsExecResult(t *testing.T) {
	if !hasNode() {
		t.Skip("node not installed")
	}
	ring := newLineRing(tailLines)
	tee := newStreamTee(ring, &brokenWriter{accept: 1})
	cmd := exec.Command("node", "-e",
		`for(let i=0;i<500;i++){console.log("out-"+i+"-"+"x".repeat(2000))};process.exitCode=7`)
	cmd.Dir = t.TempDir()
	cmd.Stdout = tee
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("cmd.Run() = %v, want the child's own exit error", err)
	}
	if ee.ExitCode() != 7 {
		t.Fatalf("exit code = %d, want 7", ee.ExitCode())
	}
	if !ring.Truncated() {
		t.Error("capture over the byte cap reported truncated = false")
	}
	if !strings.Contains(ring.Tail(), "out-499-") {
		t.Error("the final diagnostics were lost with the passthrough")
	}
}

// stdout and stderr stay independent: filling one must not mark the other.
func TestLineRingsAreIndependentPerStream(t *testing.T) {
	out, errRing := newLineRing(tailLines), newLineRing(tailLines)
	for i := 0; i < 500; i++ {
		out.Write(bigLine("out", i, 'x'))
	}
	errRing.Write([]byte("only-error-line\n"))
	if !out.Truncated() {
		t.Error("stdout ring over the byte cap reported truncated = false")
	}
	if errRing.Truncated() {
		t.Error("stderr ring was marked truncated by stdout's volume")
	}
	if strings.Contains(errRing.Tail(), "out-") || strings.Contains(out.Tail(), "only-error-line") {
		t.Error("the two rings leaked into each other")
	}
}

// The bounded capture and the local log answer different questions. The
// capture is what may be sanitized and uploaded, so it is capped; last-run.log
// never leaves the machine and must still hold the whole stream. Capping the
// capture must not quietly cap the log with it.
func TestRunLocalLogKeepsFullStreamWhileCaptureIsBounded(t *testing.T) {
	if !hasNode() {
		t.Skip("node not installed")
	}
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	argv := []string{"node", "-e",
		`for(let i=0;i<500;i++){console.error("err-"+i+"-"+"y".repeat(2000))};process.exitCode=0`}
	_, output, err := Run(context.Background(), argv, t.TempDir())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !output.StderrTruncated || len(output.Stderr) > streamTailBytes {
		t.Fatalf("capture unbounded: truncated=%v len=%d", output.StderrTruncated, len(output.Stderr))
	}
	raw, err := os.ReadFile(filepath.Join(home, "logs", "last-run.log"))
	if err != nil {
		t.Fatalf("read last-run.log: %v", err)
	}
	if len(raw) <= streamTailBytes {
		t.Fatalf("last-run.log is %d bytes; it was bounded along with the capture", len(raw))
	}
	for _, want := range []string{"err-0-", "err-499-"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("last-run.log is missing %q", want)
		}
	}
}

// A command that passed must stay one that passed when the terminal breaks.
//
// os/exec surfaces a copy error from Cmd.Wait only when the child exited 0 (a
// non-zero exit takes precedence), so the arrangement this replaced failed
// precisely when nothing was wrong with the command — and Run reads any
// non-ExitError as "the command never ran", dropping the record with it.
// The first half of this test pins that stdlib behaviour, since it is the
// whole reason the tee may not report errors; the second pins ours.
func TestStreamTeeBrokenPassthroughKeepsAZeroExitZero(t *testing.T) {
	if !hasNode() {
		t.Skip("node not installed")
	}
	const script = `for(let i=0;i<500;i++){console.log("out-"+i+"-"+"x".repeat(2000))};process.exitCode=0`

	old := exec.Command("node", "-e", script)
	old.Dir = t.TempDir()
	old.Stdout = io.MultiWriter(&brokenWriter{accept: 1}, newLineRing(tailLines))
	err := old.Run()
	if err == nil {
		t.Fatal("io.MultiWriter no longer propagates the passthrough error; " +
			"re-check whether streamTee still needs to swallow it")
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		t.Fatalf("expected a non-ExitError from the copier, got exit code %d", ee.ExitCode())
	}

	ring := newLineRing(tailLines)
	fixed := exec.Command("node", "-e", script)
	fixed.Dir = t.TempDir()
	fixed.Stdout = newStreamTee(ring, &brokenWriter{accept: 1})
	if err := fixed.Run(); err != nil {
		t.Fatalf("cmd.Run() = %v, want nil: the command exited 0", err)
	}
	if !ring.Truncated() || !strings.Contains(ring.Tail(), "out-499-") {
		t.Errorf("capture truncated=%v, has final line=%v",
			ring.Truncated(), strings.Contains(ring.Tail(), "out-499-"))
	}
}
