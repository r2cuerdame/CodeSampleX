package evidence

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"context"

	"github.com/r2cuerdame/codesamplex/internal/config"
)

// tailLines bounds the stderr ring buffer (contract C14 step 3: only the
// last 200 lines are ever considered for sanitization).
const tailLines = 200

// Run spawns argv[0] with the remaining args in dir, inheriting stdio so
// the wrapped command behaves exactly as if run directly. stderr is
// additionally teed into a bounded in-memory ring (returned as
// stderrTail, capture only — the caller sanitizes before anything is
// stored in an uploadable row) and appended raw to
// $CSX_HOME/logs/last-run.log, which never leaves the machine.
//
// The child's exit code passes through: a non-zero child exit is NOT an
// error here (exitCode carries it, err stays nil). err is reserved for
// failures to run at all (command not found, ...).
func Run(ctx context.Context, argv []string, dir string) (exitCode int, stderrTail string, err error) {
	if len(argv) == 0 {
		return -1, "", errors.New("evidence: empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout

	ring := newLineRing(tailLines)
	writers := []io.Writer{os.Stderr, nofail{ring}}
	if f := openRunLog(); f != nil {
		defer f.Close()
		writers = append(writers, nofail{f})
	}
	cmd.Stderr = io.MultiWriter(writers...)

	runErr := cmd.Run()
	tail := ring.Tail()
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			// Child ran and failed (or was killed): pass its code through.
			return ee.ExitCode(), tail, nil
		}
		return -1, tail, runErr
	}
	return 0, tail, nil
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

// nofail swallows write errors so a full disk or closed log can never
// break the stderr passthrough inside io.MultiWriter.
type nofail struct{ w io.Writer }

func (n nofail) Write(p []byte) (int, error) {
	n.w.Write(p) //nolint:errcheck // best-effort tee by design
	return len(p), nil
}

// lineRing keeps the last max complete lines plus any unterminated
// partial line. Single-writer use (exec's stderr copier goroutine).
type lineRing struct {
	max     int
	lines   []string
	partial []byte
}

func newLineRing(max int) *lineRing { return &lineRing{max: max} }

func (r *lineRing) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			r.push(strings.TrimRight(string(r.partial), "\r"))
			r.partial = r.partial[:0]
		} else {
			r.partial = append(r.partial, b)
		}
	}
	return len(p), nil
}

func (r *lineRing) push(line string) {
	if len(r.lines) == r.max {
		copy(r.lines, r.lines[1:])
		r.lines[len(r.lines)-1] = line
		return
	}
	r.lines = append(r.lines, line)
}

// Tail renders the buffered lines, including a trailing partial line.
func (r *lineRing) Tail() string {
	lines := r.lines
	if len(r.partial) > 0 {
		if len(lines) == r.max {
			lines = lines[1:]
		}
		lines = append(append([]string(nil), lines...), strings.TrimRight(string(r.partial), "\r"))
	}
	return strings.Join(lines, "\n")
}
