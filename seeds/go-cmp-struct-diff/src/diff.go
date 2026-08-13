// Package sample compares structs with go-cmp.
package sample

import (
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// Peer has an unexported field on purpose: cmp PANICS on those by default
// rather than silently comparing them, which is the behaviour that surprises
// people coming from reflect.DeepEqual. The panic is deliberate — comparing
// another package's private state is usually a bug in the test.
type Peer struct {
	ID       string
	Port     int
	Seen     time.Time
	internal string
}

func NewPeer(id string, port int, seen time.Time, internal string) Peer {
	return Peer{ID: id, Port: port, Seen: seen, internal: internal}
}

// Diff reports the difference, ignoring the timestamp and allowing the
// unexported field to be compared.
func Diff(a, b Peer) string {
	return cmp.Diff(a, b,
		cmp.AllowUnexported(Peer{}),
		cmpopts.IgnoreFields(Peer{}, "Seen"),
	)
}

// DiffStrict compares everything, including unexported state.
func DiffStrict(a, b Peer) string {
	return cmp.Diff(a, b, cmp.AllowUnexported(Peer{}))
}

// DiffDefault uses no options at all, which panics on unexported fields.
func DiffDefault(a, b Peer) (out string, panicked bool) {
	defer func() {
		if recover() != nil {
			panicked = true
		}
	}()
	return cmp.Diff(a, b), false
}
