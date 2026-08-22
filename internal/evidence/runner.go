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
type CommandOutput struct {
	Stdout          string
	Stderr          string
	StdoutTruncated bool
	StderrTruncated bool
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
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	stdoutRing := newLineRing(tailLines)
	stderrRing := newLineRing(tailLines)
	cmd.Stdout = io.MultiWriter(os.Stdout, nofail{stdoutRing})

	writers := []io.Writer{os.Stderr, nofail{stderrRing}}
	if f := openRunLog(); f != nil {
		defer f.Close()
		writers = append(writers, nofail{f})
	}
	cmd.Stderr = io.MultiWriter(writers...)

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
			// Child ran and failed (or was killed): pass its code through.
			return ee.ExitCode(), output, nil
		}
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

// nofail swallows write errors so a full disk or closed log can never
// break the stderr passthrough inside io.MultiWriter.
type nofail struct{ w io.Writer }

func (n nofail) Write(p []byte) (int, error) {
	n.w.Write(p) //nolint:errcheck // best-effort tee by design
	return len(p), nil
}

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
	if len(p) >= r.maxBytes {
		r.buf = append(r.buf[:0], p[len(p)-r.maxBytes:]...)
		r.truncated = true
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

// Tail renders the buffered lines, including a trailing partial line.
func (r *lineRing) Tail() string {
	text := strings.TrimSuffix(string(r.buf), "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > r.max {
		lines = lines[len(lines)-r.max:]
		r.truncated = true
	}
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return strings.Join(lines, "\n")
}

func (r *lineRing) Truncated() bool { return r.truncated }
