// Package probe drives testify without a test binary.
//
// assert and require share one implementation and differ by a single line:
// when the check comes back false, assert returns that false to the caller
// and require calls t.FailNow(). Everything people get wrong about testify
// follows from that line. A test written with assert keeps executing after a
// failure, so a failed assert.NotNil is followed two statements later by a
// nil pointer dereference, and the thing the developer has to debug is a
// runtime crash rather than the assertion message that explains it.
//
// Both halves of the split are reachable from an ordinary program because
// the interfaces testify asks for are tiny. assert.TestingT is Errorf alone;
// require.TestingT adds FailNow. Recorder implements both, so this sample
// watches what testify does to a t instead of describing it.
//
// The part that is easy to get wrong once you write your own TestingT:
// FailNow's contract is testing.T's, which is runtime.Goexit — end this
// goroutine, run its deferred calls, never return. A FailNow that merely
// returns silently downgrades every require in the suite to an assert, and
// the nil dereference comes back. Recorder can behave either way and the
// contract measures both.
package probe

import (
	"fmt"
	"runtime"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Recorder is a TestingT that keeps what testify told it rather than failing
// a test. GoexitOnFailNow chooses between testing.T's real FailNow and the
// no-op version people write by accident.
type Recorder struct {
	Errors          []string
	FailNows        int
	GoexitOnFailNow bool
}

func (r *Recorder) Errorf(format string, args ...any) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
}

func (r *Recorder) FailNow() {
	r.FailNows++
	if r.GoexitOnFailNow {
		runtime.Goexit()
	}
}

// Last is the most recent failure message, which is where testify explains
// itself.
func (r *Recorder) Last() string {
	if len(r.Errors) == 0 {
		return ""
	}
	return r.Errors[len(r.Errors)-1]
}

// ErrorfOnly has Errorf and nothing else: enough for assert, not enough for
// require. The contract checks that with a type assertion because a missing
// FailNow is otherwise reported as a compile error whose message does not
// explain which of the two packages you were allowed to use.
type ErrorfOnly struct{}

func (ErrorfOnly) Errorf(string, ...any) {}

// Run executes body on its own goroutine and reports whether body reached
// its last statement. The goroutine is not decoration. runtime.Goexit
// unwinds only the goroutine that calls it, and calling it on the main
// goroutine leaves the process with nothing runnable, so a second goroutine
// is the only place FailNow can be observed landing without ending the run.
func Run(body func()) (completed bool) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		body()
		completed = true
	}()
	<-done
	return
}

// Outcome records how far a test body got before testify stopped it.
// Panicked is filled from recover(), which returns nil under Goexit because
// Goexit is not a panic — that difference is what separates "require ended
// the body" from "the body crashed".
type Outcome struct {
	NextLineRan bool
	DeferRan    bool
	Panicked    any
	Name        string
}

// User and Lookup are the ordinary shape of the bug: a finder that returns
// nil for "not found", and a caller that checks the pointer before using it.
type User struct{ Name string }

func Lookup(id string) *User {
	if id == "ada" {
		return &User{Name: "Ada Lovelace"}
	}
	return nil
}

// AssertStyle is a test body written with assert, run against a user that
// does not exist. It reports what assert.NotNil handed back and how far the
// body then got.
func AssertStyle(t assert.TestingT, out *Outcome) (returned bool) {
	defer func() {
		out.DeferRan = true
		out.Panicked = recover()
	}()
	u := Lookup("nobody")
	returned = assert.NotNil(t, u, "user must exist")
	out.NextLineRan = true
	out.Name = u.Name
	return
}

// RequireStyle is the same body with require. It returns nothing useful
// because require.NotNil does not always come back, so progress is reported
// through out.
func RequireStyle(t require.TestingT, out *Outcome) {
	defer func() {
		out.DeferRan = true
		out.Panicked = recover()
	}()
	u := Lookup("nobody")
	require.NotNil(t, u, "user must exist")
	out.NextLineRan = true
	out.Name = u.Name
}
