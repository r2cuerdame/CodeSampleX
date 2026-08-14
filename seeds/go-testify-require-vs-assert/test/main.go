package main

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strings"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codesamplex.dev/sample/gotestify/src"
)

func main() {
	// The split, checked rather than described. A t with only Errorf is an
	// assert.TestingT and is not a require.TestingT, and that one missing
	// method is the entire difference between the two packages.
	var errorfOnly any = probe.ErrorfOnly{}
	_, okAssert := errorfOnly.(assert.TestingT)
	_, okRequire := errorfOnly.(require.TestingT)
	check(okAssert, "Errorf alone should satisfy assert.TestingT")
	check(!okRequire, "Errorf alone must not satisfy require.TestingT")

	// assert on a failed NotNil. One failure is recorded, FailNow is never
	// called, false comes back, and then the next statement runs and
	// dereferences the nil pointer. Nothing about assert stops a test.
	// This body runs on the main goroutine with a Recorder armed to Goexit,
	// so an assert that did reach FailNow would kill the process outright
	// ("no goroutines (main called runtime.Goexit) - deadlock") instead of
	// being counted below.
	rec := &probe.Recorder{GoexitOnFailNow: true}
	var out probe.Outcome
	returned := probe.AssertStyle(rec, &out)
	check(!returned, "assert.NotNil must return false on failure")
	check(len(rec.Errors) == 1, "expected 1 recorded failure, got %d", len(rec.Errors))
	check(rec.FailNows == 0, "assert must never call FailNow, got %d", rec.FailNows)
	check(out.NextLineRan, "the statement after a failed assert still runs")
	check(out.Panicked != nil, "the dereference two lines later must panic")
	_, isRuntimeErr := out.Panicked.(runtime.Error)
	check(isRuntimeErr, "expected a runtime.Error, got %T", out.Panicked)
	check(strings.Contains(fmt.Sprint(out.Panicked), "nil pointer dereference"),
		"expected a nil pointer dereference, got %v", out.Panicked)

	// require on the identical body. The failure is recorded, FailNow is
	// called exactly once, and the body is abandoned where it stood: the
	// dereference is never reached, so there is no panic to debug. This is
	// the whole reason require exists.
	rec = &probe.Recorder{GoexitOnFailNow: true}
	out = probe.Outcome{}
	completed := probe.Run(func() { probe.RequireStyle(rec, &out) })
	check(!completed, "require.NotNil must not return to its caller on failure")
	check(len(rec.Errors) == 1, "require records the same single failure, got %d", len(rec.Errors))
	check(rec.FailNows == 1, "expected exactly one FailNow, got %d", rec.FailNows)
	check(!out.NextLineRan, "the statement after a failed require must not run")
	check(out.Panicked == nil, "nothing should panic under require: %v", out.Panicked)
	check(out.DeferRan, "Goexit still runs deferred calls, so cleanup is safe")

	// The trap that appears the moment you supply your own TestingT — a mock
	// t, a table helper, a fixture type. FailNow's contract is testing.T's:
	// end the goroutine. A FailNow that returns puts require back on the
	// assert code path, and the nil dereference returns with it.
	rec = &probe.Recorder{GoexitOnFailNow: false}
	out = probe.Outcome{}
	completed = probe.Run(func() { probe.RequireStyle(rec, &out) })
	check(completed, "a FailNow that returns lets the body run to the end")
	check(rec.FailNows == 1, "FailNow was still called once, got %d", rec.FailNows)
	check(out.NextLineRan, "with a no-op FailNow, require continues like assert")
	check(out.Panicked != nil, "and the nil dereference is back")

	// Equal is reflect.DeepEqual underneath, so it compares types as well as
	// values: 1 and int64(1) are not equal. The message prints both types,
	// which is the failure people read as a testify bug when the two values
	// look identical. EqualValues converts first and passes.
	rec = &probe.Recorder{}
	check(!assert.Equal(rec, 1, int64(1)), "assert.Equal(1, int64(1)) must fail")
	msg := rec.Last()
	check(strings.Contains(msg, "int64(1)") && strings.Contains(msg, "int(1)"),
		"the message should name both types, got %q", msg)
	before := len(rec.Errors)
	check(assert.EqualValues(rec, 1, int64(1)), "EqualValues converts and passes")
	check(len(rec.Errors) == before, "a passing assertion records nothing")

	// ObjectsAreEqual is the predicate under Equal, callable with no t at
	// all. Slices reach reflect.DeepEqual, so order counts and a nil slice is
	// not an empty slice.
	check(assert.ObjectsAreEqual([]int{1, 2}, []int{1, 2}), "equal slices")
	check(!assert.ObjectsAreEqual([]int{1, 2}, []int{2, 1}), "slice order counts")
	check(!assert.ObjectsAreEqual([]int(nil), []int{}), "a nil slice is not an empty slice")

	// []byte does not reach reflect at all — ObjectsAreEqual type-asserts it
	// out and finishes with bytes.Equal. The expectation to correct here is
	// that the shortcut therefore inherits bytes.Equal's answers, because
	// bytes.Equal calls a nil slice and an empty slice equal. It does not:
	// the branch tests both operands for nil before delegating, so []byte
	// ends up agreeing with every other slice type. Measured, not assumed.
	check(bytes.Equal([]byte(nil), []byte{}), "bytes.Equal calls nil equal to empty")
	check(!assert.ObjectsAreEqual([]byte(nil), []byte{}),
		"ObjectsAreEqual guards nil before delegating, so it says not equal")
	check(!reflect.DeepEqual([]byte(nil), []byte{}), "which is the reflect answer")
	check(assert.ObjectsAreEqual([]byte("ab"), []byte("ab")), "content comparison still works")

	// The asymmetry the shortcut does leave behind: a type assertion needs
	// exact type identity, so a named type over []byte never takes the fast
	// path. With []byte expected, a non-[]byte actual is rejected outright
	// without being compared; with the named type expected, reflect.DeepEqual
	// rejects it on type. Same answer, two different reasons, and neither one
	// ever looks at the bytes — which the third line pins down, since the
	// named type does compare fine against itself once the types agree.
	type digest []byte
	check(!assert.ObjectsAreEqual([]byte("ab"), digest("ab")), "[]byte vs named type")
	check(!assert.ObjectsAreEqual(digest("ab"), []byte("ab")), "and the reverse")
	check(assert.ObjectsAreEqual(digest("ab"), digest("ab")), "the type is what fails, not the content")

	// ElementsMatch is the order-insensitive comparison. It is not a set
	// comparison: it counts multiplicity, so a duplicate that moved is fine
	// and a duplicate that changed is not.
	rec = &probe.Recorder{}
	check(assert.ElementsMatch(rec, []int{1, 2, 2}, []int{2, 2, 1}), "order is ignored")
	check(len(rec.Errors) == 0, "a passing ElementsMatch records nothing")
	check(!assert.Equal(rec, []int{1, 2, 2}, []int{2, 2, 1}), "Equal does not ignore order")
	check(!assert.ElementsMatch(rec, []int{1, 2, 2}, []int{1, 1, 2}),
		"ElementsMatch counts multiplicity")

	fmt.Println("contract ok")
}

func check(ok bool, format string, args ...any) {
	if !ok {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		os.Exit(1)
	}
}
