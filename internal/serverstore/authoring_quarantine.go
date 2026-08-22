package serverstore

import (
	"sort"
	"strings"
	"time"
)

// The authoring attempt ledger: a bounded memory of what happened the last
// times a coordinate was handed to a sample writer, and the rule that stops
// offering one that keeps producing nothing.
//
// It exists because enumerating impossible package shapes by hand always lags
// the ecosystem. Three were found by hand — the Gradle plugin marker, the
// pom-only BOM or parent, the per-platform npm native binary — and each cost a
// worker hours before anybody noticed. The last one is measured:
// org.jetbrains.kotlin.plugin.serialization.gradle.plugin took an authoring
// slot on a 24-hour lease, was handed back to the same live session 22 times
// across four hours, and every restart was handed it again. Reclaim released
// only claims whose SESSION had died, and that session never died.
//
// What generalises is not the shape of the package. It is the attempt: a
// coordinate that keeps being handed out and keeps producing nothing.
//
// Nothing here deletes. A coordinate is WITHHELD, with the reason, the
// evidence and the age beside it, and either an operator or a timer puts it
// back. That is deliberate: a false exclusion that is permanent and silent
// loses real authoring work forever, and this ledger's whole job is to be
// wrong out loud.

// AuthoringOutcome classifies one authoring attempt. The three failure
// classes are not interchangeable, and treating them as one is how a bad
// afternoon at a registry turns into a permanent exclusion.
type AuthoringOutcome string

const (
	// AuthoringHandedOut opens an attempt. It is recorded the moment a
	// coordinate is given to a writer, because the only thing the server can
	// observe unaided is that it gave the work out and got nothing back.
	AuthoringHandedOut AuthoringOutcome = "HANDED_OUT"
	// AuthoringAuthored is a sample actually written against the coordinate.
	// It answers the question and clears every counter that withholds work.
	AuthoringAuthored AuthoringOutcome = "AUTHORED"
	// AuthoringInfrastructure is the worker's own machine failing — no Docker
	// daemon, no disk, no route to this server. It measured nothing about the
	// coordinate and must not count against it.
	AuthoringInfrastructure AuthoringOutcome = "INFRASTRUCTURE"
	// AuthoringTransient is a registry or toolchain that would not answer. A
	// registry that will not answer has not said no.
	AuthoringTransient AuthoringOutcome = "TRANSIENT"
	// AuthoringNoCallableSymbol is the strong one: the writer looked and
	// measured that no symbol or project a contract could call exists here —
	// a pom with no jar, a marker artifact, a lone .node binary the parent
	// package selects internally.
	AuthoringNoCallableSymbol AuthoringOutcome = "NO_CALLABLE_SYMBOL"
	// AuthoringNoOutput is a writer that gave up without saying why. It is
	// already implied by the handout it closes and is accepted so a worker
	// can hand its slot back immediately instead of sitting on the lease.
	AuthoringNoOutput AuthoringOutcome = "NO_OUTPUT"
)

// ValidAuthoringOutcome reports whether a worker may report this outcome.
// HANDED_OUT and AUTHORED are the server's own bookkeeping and are not
// accepted from a client.
func ValidAuthoringOutcome(outcome AuthoringOutcome) bool {
	switch outcome {
	case AuthoringInfrastructure, AuthoringTransient, AuthoringNoCallableSymbol, AuthoringNoOutput:
		return true
	}
	return false
}

// The thresholds. Every one of them is a proposal measured against the
// incidents above rather than a preference, and every one of them is safe to
// be wrong about in the withholding direction because withholding is visible
// and reversible.
const (
	// AuthoringMaxSessionHandouts is how many times ONE writer may be handed
	// the same coordinate before it is moved on to different work. This is
	// the bound the 22-attempt incident needed: the lease was 24 hours, the
	// session stayed alive, and nothing else would ever have released it.
	//
	// Three, because a writer that has looked at a coordinate three times
	// across three separate stretches of work has told us what it knows, and
	// because three is small enough that the loss when we are wrong is one
	// worker-hour rather than four.
	AuthoringMaxSessionHandouts = 3

	// AuthoringNoOutputQuarantine is how many attempts may produce nothing
	// before the coordinate stops being offered at all.
	//
	// Six, which is exactly two writers' worth: with the per-writer bound
	// above, six unexcused attempts cannot be reached by one machine. That is
	// the point — one writer failing is one writer's opinion, and the network
	// only withholds work on the evidence of two independent ones.
	AuthoringNoOutputQuarantine = 6

	// AuthoringNoSymbolQuarantine is how many DISTINCT writers must measure
	// that nothing callable exists before the coordinate is withheld.
	//
	// Two, for the same reason and at a far lower count: this outcome is a
	// measurement of the artifact rather than a report about the attempt, so
	// it does not need six tries to be believed — but one writer's report is
	// still one writer's opinion.
	AuthoringNoSymbolQuarantine = 2

	// AuthoringExcusedAttempts is how many attempts on one coordinate may be
	// refunded as somebody else's fault before refunding stops.
	//
	// Excusing has to be bounded or a writer stuck in a loop reporting the
	// same excuse holds the network's attention forever. Four is more than
	// any real outage needs on a single coordinate and far fewer than a loop
	// produces.
	AuthoringExcusedAttempts = 4

	// AuthoringHistoryDepth bounds the stored evidence per coordinate. The
	// ticket asked for a BOUNDED history: an operator needs the last few
	// attempts to judge a withholding, and nobody needs the two hundredth.
	AuthoringHistoryDepth = 10

	// authoringMaxTrackedSessions bounds the per-writer handout map. A
	// coordinate cannot reach this many writers before the no-output
	// threshold withholds it, so the cap only ever catches a pathological
	// row; a writer past the cap simply is not bounded individually, which
	// fails towards handing out work rather than withholding it.
	authoringMaxTrackedSessions = 64

	// authoringMaxDetailBytes bounds the worker-supplied note. It is prose
	// from a client and is rendered to an operator.
	authoringMaxDetailBytes = 240
)

// AuthoringAttemptDebounce is how much time must pass before the same
// coordinate in the same writer's hands counts as a second attempt.
//
// Polling is not attempting. `csx sample-worker next` is a one-shot command an
// agent runs, and an agent that runs it twice in a minute has not tried twice;
// counting it as two would withhold a coordinate nobody ever worked on. Five
// minutes is shorter than any real authoring attempt and longer than any poll
// loop.
const AuthoringAttemptDebounce = 5 * time.Minute

// AuthoringQuarantineCooldown is how long a withholding INFERRED from repeated
// no output lasts before it lapses on its own.
//
// A withholding that never lapses is a deletion with better manners. Repeated
// no output is an inference about attempts, not a measurement of the artifact,
// and the thing it most plausibly reflects — a registry, a toolchain or an
// image that was broken for a while — heals. Thirty days is long enough that
// the coordinate is not immediately back in the queue and short enough that
// nobody has to remember it.
const AuthoringQuarantineCooldown = 30 * 24 * time.Hour

// The withholding reasons. They are counted as keys in the operations panel,
// so they are fixed strings rather than assembled prose.
const (
	AuthoringReasonNoCallableSymbol = "no callable symbol: independent writers measured that nothing here can be called"
	AuthoringReasonNoOutput         = "repeated no output: handed out and produced nothing publishable"
)

// AuthoringAttempt is one entry of the bounded per-coordinate history.
// SessionID is the internal writer session, which is operator-private state
// and never leaves the admin surface.
type AuthoringAttempt struct {
	At        time.Time        `json:"at"`
	Kind      string           `json:"kind"`
	SessionID string           `json:"sessionId"`
	Outcome   AuthoringOutcome `json:"outcome"`
	Detail    string           `json:"detail,omitempty"`
}

// AuthoringAttemptState is everything remembered about one coordinate: why
// work was withheld, what the evidence was, and how old it is.
type AuthoringAttemptState struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Symbol    string `json:"symbol"`
	// Kind is the work kind of the most recent handout. Withholding is keyed
	// on the coordinate rather than on the kind because a package with no
	// callable symbol has none whichever queue picked it, and splitting the
	// key would let the same hopeless coordinate restart its count under
	// another kind. The kind stays on every history entry, so the record
	// still distinguishes them.
	Kind     string `json:"kind"`
	Attempts int    `json:"attempts"`
	// NoOutput counts attempts that produced nothing publishable and were not
	// excused, since the last attempt that did produce something.
	NoOutput int `json:"noOutput"`
	Authored int `json:"authored"`
	Excused  int `json:"excused"`
	// SessionsMeasuringImpossible is how many DISTINCT writers reported that
	// nothing callable can exist here.
	SessionsMeasuringImpossible int       `json:"sessionsMeasuringImpossible"`
	FirstAttemptAt              time.Time `json:"firstAttemptAt"`
	LastAttemptAt               time.Time `json:"lastAttemptAt"`
	QuarantinedAt               time.Time `json:"quarantinedAt,omitempty"`
	QuarantineReason            string    `json:"quarantineReason,omitempty"`
	// ReopensAt is when the withholding lapses by itself. Zero means it does
	// not: a measured impossibility does not heal, so only an operator lifts
	// that one.
	ReopensAt time.Time          `json:"reopensAt,omitempty"`
	History   []AuthoringAttempt `json:"history,omitempty"`
}

// Withheld reports whether this coordinate is being kept off the board right
// now. It is deliberately pure: the picker and the operations panel both ask
// exactly this question of exactly this state, which is the only way the two
// can be made to agree.
func (s AuthoringAttemptState) Withheld(now time.Time) bool {
	if s.QuarantinedAt.IsZero() {
		return false
	}
	if s.ReopensAt.IsZero() {
		return true
	}
	return now.Before(s.ReopensAt)
}

// authoringLedger is the mutable state behind one coordinate plus the working
// sets that are bookkeeping rather than evidence. Both stores keep exactly
// this and apply exactly the transitions below, so there is one implementation
// of the rules and no way for the Fake and PostgreSQL to drift.
type authoringLedger struct {
	AuthoringAttemptState
	// SessionHandouts is how many unexcused handouts each writer has had.
	SessionHandouts map[string]int `json:"sessionHandouts,omitempty"`
	// NoSymbolBy is the set of writers that measured this impossible.
	NoSymbolBy map[string]bool `json:"noSymbolBy,omitempty"`
}

func newAuthoringLedger(ecosystem, name, version, symbol string) *authoringLedger {
	return &authoringLedger{
		AuthoringAttemptState: AuthoringAttemptState{
			Ecosystem: ecosystem, Name: name, Version: version, Symbol: symbol,
		},
		SessionHandouts: map[string]int{},
		NoSymbolBy:      map[string]bool{},
	}
}

func (l *authoringLedger) ensure() {
	if l.SessionHandouts == nil {
		l.SessionHandouts = map[string]int{}
	}
	if l.NoSymbolBy == nil {
		l.NoSymbolBy = map[string]bool{}
	}
}

// barred reports whether this coordinate may be offered to this writer.
//
// Two different bounds meet here on purpose. The withholding is about the
// coordinate and applies to everybody; the handout count is about this writer
// and applies only to it, because a writer that cannot author something is not
// evidence that nobody can.
func (l *authoringLedger) barred(sessionID string, now time.Time) bool {
	if l.Withheld(now) {
		return true
	}
	l.ensure()
	return l.SessionHandouts[sessionID] >= AuthoringMaxSessionHandouts
}

// handout opens an attempt.
func (l *authoringLedger) handout(kind, sessionID string, now time.Time) {
	l.ensure()
	// A lapsed withholding is a second chance, not a suspended sentence: the
	// counters that produced it start again from zero.
	if !l.QuarantinedAt.IsZero() && !l.Withheld(now) {
		l.clearGates()
	}
	l.Attempts++
	l.NoOutput++
	if _, tracked := l.SessionHandouts[sessionID]; tracked || len(l.SessionHandouts) < authoringMaxTrackedSessions {
		l.SessionHandouts[sessionID]++
	}
	if l.FirstAttemptAt.IsZero() {
		l.FirstAttemptAt = now
	}
	l.LastAttemptAt = now
	if kind != "" {
		l.Kind = kind
	}
	l.push(AuthoringAttempt{At: now, Kind: l.Kind, SessionID: sessionID, Outcome: AuthoringHandedOut})
	l.evaluate(now)
}

// report closes an attempt with the writer's own classification.
func (l *authoringLedger) report(sessionID string, outcome AuthoringOutcome, detail string, now time.Time) {
	l.ensure()
	l.push(AuthoringAttempt{At: now, Kind: l.Kind, SessionID: sessionID,
		Outcome: outcome, Detail: clampAuthoringDetail(detail)})
	switch outcome {
	case AuthoringInfrastructure, AuthoringTransient:
		// The attempt measured nothing about the coordinate, so it is refunded
		// — to the coordinate AND to the writer, because a writer whose Docker
		// daemon died has not spent one of its three looks.
		if l.Excused < AuthoringExcusedAttempts {
			l.Excused++
			if l.NoOutput > 0 {
				l.NoOutput--
			}
			if l.SessionHandouts[sessionID] > 0 {
				l.SessionHandouts[sessionID]--
			}
		}
	case AuthoringNoCallableSymbol:
		l.NoSymbolBy[sessionID] = true
		l.SessionsMeasuringImpossible = len(l.NoSymbolBy)
		// This writer has said its piece about this coordinate. Offering it
		// again would only collect the same answer.
		l.SessionHandouts[sessionID] = AuthoringMaxSessionHandouts
	}
	l.evaluate(now)
}

// authored records that the coordinate produced a sample. The counters that
// withhold work reset; the history does not, because it is the audit trail.
func (l *authoringLedger) authored(sessionID string, now time.Time) {
	l.ensure()
	l.Authored++
	l.push(AuthoringAttempt{At: now, Kind: l.Kind, SessionID: sessionID, Outcome: AuthoringAuthored})
	l.clearGates()
}

// reopen lifts a withholding. It returns false when nothing was withheld so an
// operator clicking twice sees "nothing to do" rather than a failure.
func (l *authoringLedger) reopen(now time.Time) bool {
	if !l.Withheld(now) {
		return false
	}
	l.ensure()
	l.clearGates()
	return true
}

// clearGates resets everything that can withhold work and keeps everything
// that records what happened.
func (l *authoringLedger) clearGates() {
	l.NoOutput = 0
	l.Excused = 0
	l.SessionsMeasuringImpossible = 0
	l.SessionHandouts = map[string]int{}
	l.NoSymbolBy = map[string]bool{}
	l.QuarantinedAt = time.Time{}
	l.QuarantineReason = ""
	l.ReopensAt = time.Time{}
}

// evaluate applies the thresholds. A coordinate already withheld is left
// alone: the first reason is the true one, and overwriting it would lose why
// the work actually left the board.
//
// now is when the withholding happened, which is not the same as the last
// handout: a writer that measures a coordinate impossible reports it after
// working on it, and the age an operator reads has to be the age of the
// decision.
func (l *authoringLedger) evaluate(now time.Time) {
	if !l.QuarantinedAt.IsZero() {
		return
	}
	switch {
	case len(l.NoSymbolBy) >= AuthoringNoSymbolQuarantine:
		l.QuarantinedAt = now
		l.QuarantineReason = AuthoringReasonNoCallableSymbol
		// An artifact does not grow a jar later, so this one does not lapse.
		l.ReopensAt = time.Time{}
	case l.NoOutput >= AuthoringNoOutputQuarantine:
		l.QuarantinedAt = now
		l.QuarantineReason = AuthoringReasonNoOutput
		l.ReopensAt = now.Add(AuthoringQuarantineCooldown)
	}
}

func (l *authoringLedger) push(entry AuthoringAttempt) {
	l.History = append(l.History, entry)
	if len(l.History) > AuthoringHistoryDepth {
		l.History = append([]AuthoringAttempt(nil), l.History[len(l.History)-AuthoringHistoryDepth:]...)
	}
}

// state returns a copy safe to hand outside the store's lock.
func (l *authoringLedger) state() AuthoringAttemptState {
	out := l.AuthoringAttemptState
	out.History = append([]AuthoringAttempt(nil), l.History...)
	return out
}

func clampAuthoringDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if len(detail) > authoringMaxDetailBytes {
		return detail[:authoringMaxDetailBytes]
	}
	return detail
}

// sortAuthoringQuarantine puts the newest withholding first, so an operator
// opening the panel sees what just left the board.
func sortAuthoringQuarantine(rows []AuthoringAttemptState) {
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].QuarantinedAt.Equal(rows[j].QuarantinedAt) {
			return rows[i].QuarantinedAt.After(rows[j].QuarantinedAt)
		}
		if rows[i].Ecosystem != rows[j].Ecosystem {
			return rows[i].Ecosystem < rows[j].Ecosystem
		}
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		if rows[i].Version != rows[j].Version {
			return rows[i].Version < rows[j].Version
		}
		return rows[i].Symbol < rows[j].Symbol
	})
}
