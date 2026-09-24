package serverstore

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// The authoring budget report (#149): what one coordinate actually costs the
// farm, measured from the attempt ledger rather than argued from the
// thresholds.
//
// This report supplied the production measurements used to replace the old
// session-count cost proxy with a peer-aware evidence boundary and a bounded
// per-coordinate slot budget.
// This report is the measurement the tuning has to start from: it replays the
// stored history, reconstructs every episode (the handouts between two
// AUTHORED events), and prices the budget options against the successes they
// would have lost.
//
// It is read-only and pure. It never writes a ledger, and it states what it
// cannot see: history is bounded to AuthoringHistoryDepth entries, and an
// attempt the ledger never saw closed has an unknown slot cost that is
// reported as an upper bound rather than guessed.

// AuthoringBudgetRow is one authoring_attempts row as dumped read-only. Ledger
// is the stored JSONB document.
type AuthoringBudgetRow struct {
	Ecosystem string          `json:"ecosystem"`
	Name      string          `json:"name"`
	Version   string          `json:"version"`
	Symbol    string          `json:"symbol"`
	Ledger    json.RawMessage `json:"ledger"`
}

// AuthoringBudgetSession is one authoring_sessions row: the writer session
// and the machine it ran on. Peer is ComputerName, falling back to the label
// with its "-slotN" suffix removed for sessions issued before the computer
// name was recorded.
type AuthoringBudgetSession struct {
	SessionID    string `json:"sessionId"`
	Label        string `json:"label"`
	ComputerName string `json:"computerName"`
}

// AuthoringBudgetOptions are the farm facts the replay needs. They are
// inputs, not constants, because they are properties of the farm's writer
// command (agy --print-timeout) rather than of this server.
type AuthoringBudgetOptions struct {
	// PrintTimeout bounds one writer iteration; an attempt with no observed
	// close is charged at most this much slot time.
	PrintTimeout time.Duration
	// TimeoutLike is the in-slot duration from which an attempt that ended
	// without a sample is counted as having hit the writer timeout.
	TimeoutLike time.Duration
	// SlotBudgets are the per-episode cumulative slot-minute caps to price.
	SlotBudgets []time.Duration
	// InitialTimeouts are the shorter first-attempt timeouts to price, each
	// with one bounded escalation to PrintTimeout.
	InitialTimeouts []time.Duration
	// Top is how many of the most expensive episodes to list.
	Top int
}

// DefaultAuthoringBudgetOptions are the production farm's values when #149
// was opened: agy --print-timeout 50m.
func DefaultAuthoringBudgetOptions() AuthoringBudgetOptions {
	return AuthoringBudgetOptions{
		PrintTimeout:    50 * time.Minute,
		TimeoutLike:     45 * time.Minute,
		SlotBudgets:     []time.Duration{30 * time.Minute, 60 * time.Minute, 90 * time.Minute, 120 * time.Minute, 180 * time.Minute},
		InitialTimeouts: []time.Duration{10 * time.Minute, 15 * time.Minute, 20 * time.Minute, 30 * time.Minute},
		Top:             10,
	}
}

// How an attempt ended, as far as the ledger can tell.
const (
	budgetEndAuthored       = "AUTHORED"
	budgetEndSameSession    = "NEXT_HANDOUT_SAME_WRITER" // implicit no output, slot time observed
	budgetEndOtherSession   = "NEXT_HANDOUT_OTHER_WRITER"
	budgetEndOpen           = "OPEN"
	budgetEndReportedPrefix = "REPORTED_"
)

type budgetAttempt struct {
	at      time.Time
	session string
	peer    string
	slot    string
	kind    string
	end     string
	// known is the observed in-slot duration; upper additionally charges an
	// unobserved close at PrintTimeout.
	known, upper time.Duration
	observed     bool
	refunded     bool
}

type budgetEpisode struct {
	ecosystem, coordinate, axis, kind string
	attempts                          []budgetAttempt
	// missing is how many handouts precede the history window and belong to
	// this episode; -1 when the ledger cannot attribute them.
	missing   int
	succeeded bool
	withheld  bool
	reason    string
}

// AuthoringBudgetDistribution is a small order-statistics summary.
type AuthoringBudgetDistribution struct {
	N   int     `json:"n"`
	P50 float64 `json:"p50"`
	P90 float64 `json:"p90"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

func budgetDistribution(values []float64) AuthoringBudgetDistribution {
	if len(values) == 0 {
		return AuthoringBudgetDistribution{}
	}
	v := append([]float64(nil), values...)
	sort.Float64s(v)
	// Nearest rank: the smallest value with at least q of the sample at or
	// below it.
	at := func(q float64) float64 {
		i := int(math.Ceil(q*float64(len(v)))) - 1
		if i < 0 {
			i = 0
		}
		return round1(v[i])
	}
	return AuthoringBudgetDistribution{N: len(v), P50: at(0.5), P90: at(0.9), P99: at(0.99), Max: round1(v[len(v)-1])}
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }

// AuthoringBudgetGroup is the attempts-to-success picture for one slice.
type AuthoringBudgetGroup struct {
	Episodes  int `json:"episodes"`
	Succeeded int `json:"succeeded"`
	// Withheld episodes ended withheld; Open ones neither succeeded nor were
	// withheld inside the history window.
	Withheld int `json:"withheld"`
	Open     int `json:"open"`
	// AttemptsToSuccess histograms handouts up to and including the one that
	// authored, for episodes whose count is exact.
	AttemptsToSuccess map[int]int                 `json:"attemptsToSuccess"`
	AttemptsSummary   AuthoringBudgetDistribution `json:"attemptsSummary"`
	// SuccessSlotMinutes is the cumulative observed slot time of succeeded
	// episodes; FailedSlotMinutesUpper charges unobserved closes at the
	// print timeout.
	SuccessSlotMinutes     AuthoringBudgetDistribution `json:"successSlotMinutes"`
	FailedSlotMinutesUpper AuthoringBudgetDistribution `json:"failedSlotMinutesUpper"`
}

// AuthoringBudgetOption prices one alternative budget against history.
type AuthoringBudgetOption struct {
	Name string `json:"name"`
	// SuccessesLost is how many succeeded episodes the option would have
	// stopped before their authoring attempt.
	SuccessesLost int `json:"successesLost"`
	// SuccessesLostShare is SuccessesLost over all exactly-measured successes.
	SuccessesLostShare float64 `json:"successesLostShare"`
	// SuccessesLostBeyondCurrent excludes the successes today's thresholds
	// replayed over the same history would also have lost.
	SuccessesLostBeyondCurrent int `json:"successesLostBeyondCurrent"`
	// SlotMinutesSaved is the slot time the option would not have spent on
	// episodes that never authored (upper bound: unobserved closes charged at
	// the print timeout); SlotMinutesSavedObserved counts only attempts whose
	// close the ledger saw. Negative means the option costs slot time.
	SlotMinutesSaved         float64 `json:"slotMinutesSaved"`
	SlotMinutesSavedObserved float64 `json:"slotMinutesSavedObserved"`
	// MaxEpisodeSlotHours is the worst single episode's slot cost under the
	// option.
	MaxEpisodeSlotHours float64 `json:"maxEpisodeSlotHours"`
	Note                string  `json:"note,omitempty"`
	// LostExamples names up to three successes the option would have lost.
	LostExamples []string `json:"lostExamples,omitempty"`
}

// AuthoringBudgetExpensive is one of the costliest episodes.
type AuthoringBudgetExpensive struct {
	Ecosystem         string  `json:"ecosystem"`
	Coordinate        string  `json:"coordinate"`
	Axis              string  `json:"axis"`
	Kind              string  `json:"kind"`
	Attempts          int     `json:"attempts"`
	Writers           int     `json:"writers"`
	Peers             int     `json:"peers"`
	Outcome           string  `json:"outcome"`
	SlotMinutesKnown  float64 `json:"slotMinutesKnown"`
	SlotMinutesUpper  float64 `json:"slotMinutesUpper"`
	HistoryIncomplete bool    `json:"historyIncomplete,omitempty"`
}

// AuthoringBudgetReport is the whole measurement.
type AuthoringBudgetReport struct {
	Rows     int `json:"rows"`
	Episodes int `json:"episodes"`
	// TruncatedRows have more handouts than the bounded history holds.
	TruncatedRows int `json:"truncatedRows"`
	// InexactEpisodes could not be counted exactly because their start fell
	// outside the history window; they are excluded from AttemptsToSuccess.
	InexactEpisodes int `json:"inexactEpisodes"`

	Overall     AuthoringBudgetGroup            `json:"overall"`
	ByEcosystem map[string]AuthoringBudgetGroup `json:"byEcosystem"`
	ByKind      map[string]AuthoringBudgetGroup `json:"byKind"`
	ByAxis      map[string]AuthoringBudgetGroup `json:"byAxis"`

	// AttemptEnds counts how every attempt in the window ended.
	AttemptEnds map[string]int `json:"attemptEnds"`
	// AuthoredAttemptMinutes is the duration of the attempts that produced a
	// sample; the throughput every option must not hurt.
	AuthoredAttemptMinutes AuthoringBudgetDistribution `json:"authoredAttemptMinutes"`
	// AuthoredOver counts authored attempts longer than each threshold.
	AuthoredOver map[string]int `json:"authoredOver"`
	// TimeoutLikeAttempts ended without a sample after at least TimeoutLike
	// in the slot; TimeoutLikeThenSucceeded were followed in the same episode
	// by a success.
	TimeoutLikeAttempts      int `json:"timeoutLikeAttempts"`
	TimeoutLikeThenSucceeded int `json:"timeoutLikeThenSucceeded"`

	// Independence: distinct writers (sessions), machines (peers) and local
	// slots per episode, and which writer produced the success.
	WritersPerEpisode      map[int]int `json:"writersPerEpisode"`
	PeersPerEpisode        map[int]int `json:"peersPerEpisode"`
	SlotsPerEpisode        map[int]int `json:"slotsPerEpisode"`
	SuccessByWriterOrdinal map[int]int `json:"successByWriterOrdinal"`
	// SuccessAfterWriterExhausted counts successes that came after one writer
	// had already used AuthoringMaxSessionHandouts charged handouts.
	SuccessAfterWriterExhausted int `json:"successAfterWriterExhausted"`
	// MultiWriterEpisodesSinglePeer counts episodes where the "two
	// independent writers" were sessions of one machine.
	MultiWriterEpisodes           int            `json:"multiWriterEpisodes"`
	MultiWriterEpisodesSinglePeer int            `json:"multiWriterEpisodesSinglePeer"`
	Peers                         map[string]int `json:"peers"`
	WithheldByReason              map[string]int `json:"withheldByReason"`

	// Current is the maximum slot cost the current thresholds allow and the
	// worst one actually observed.
	CurrentMaxEpisodeSlotHoursUpper    float64 `json:"currentMaxEpisodeSlotHoursUpper"`
	CurrentMaxEpisodeSlotHoursKnown    float64 `json:"currentMaxEpisodeSlotHoursKnown"`
	CurrentPolicyDispatchBudgetMinutes float64 `json:"currentPolicyDispatchBudgetMinutes"`
	CurrentPolicyCeilingSlotHours      float64 `json:"currentPolicyCeilingSlotHours"`

	Options   []AuthoringBudgetOption    `json:"options"`
	Expensive []AuthoringBudgetExpensive `json:"expensive"`
}

// MeasureAuthoringBudget replays the ledger rows into the report.
func MeasureAuthoringBudget(rows []AuthoringBudgetRow, sessions []AuthoringBudgetSession, opts AuthoringBudgetOptions) (AuthoringBudgetReport, error) {
	if opts.PrintTimeout <= 0 {
		return AuthoringBudgetReport{}, fmt.Errorf("authoring budget: print timeout must be positive")
	}
	if opts.TimeoutLike <= 0 || opts.TimeoutLike > opts.PrintTimeout {
		opts.TimeoutLike = opts.PrintTimeout
	}
	peerOf := map[string]string{}
	slotOf := map[string]string{}
	for _, s := range sessions {
		peerOf[s.SessionID] = budgetPeer(s)
		slotOf[s.SessionID] = s.Label
	}
	var episodes []budgetEpisode
	rep := AuthoringBudgetReport{Rows: len(rows), Peers: map[string]int{}}
	for _, row := range rows {
		var l authoringLedger
		if err := json.Unmarshal(row.Ledger, &l); err != nil {
			return AuthoringBudgetReport{}, fmt.Errorf("authoring budget: %s/%s@%s: %w", row.Ecosystem, row.Name, row.Version, err)
		}
		eps, truncated := budgetEpisodes(row, &l, peerOf, slotOf, opts)
		if truncated {
			rep.TruncatedRows++
		}
		episodes = append(episodes, eps...)
	}
	for _, peer := range peerOf {
		rep.Peers[peer]++
	}
	rep.Episodes = len(episodes)
	summarizeAuthoringBudget(&rep, episodes, opts)
	return rep, nil
}

func budgetPeer(s AuthoringBudgetSession) string {
	peer := authoringPeerIdentity(s.SessionID, s.Label, s.ComputerName)
	if peer == "" {
		return "unknown"
	}
	return peer
}

// budgetEpisodes splits one coordinate's bounded history into per-axis
// episodes. An episode ends at AUTHORED; the last one ends wherever the
// history does.
func budgetEpisodes(row AuthoringBudgetRow, l *authoringLedger, peerOf, slotOf map[string]string, opts AuthoringBudgetOptions) ([]budgetEpisode, bool) {
	history := l.History
	handouts, authoredInHistory := 0, 0
	axes := map[string]bool{}
	for _, e := range history {
		axes[normalizeAuthoringAxis(e.Axis)] = true
		switch e.Outcome {
		case AuthoringHandedOut:
			handouts++
		case AuthoringAuthored:
			authoredInHistory++
		}
	}
	missing := l.Attempts - handouts
	if missing < 0 {
		missing = 0
	}
	truncated := missing > 0
	// The handouts that fell out of the window can be attributed to the first
	// episode in it only if no success fell out with them and only one axis
	// is in play.
	firstMissing := missing
	if truncated && (authoredInHistory < l.Authored || len(axes) > 1) {
		firstMissing = -1
	}
	coordinate := row.Name + "@" + row.Version
	if row.Symbol != "" {
		coordinate += "#" + row.Symbol
	}
	open := map[string]*budgetEpisode{}
	var out []budgetEpisode
	firstSeen := map[string]bool{}
	for i, e := range history {
		axis := normalizeAuthoringAxis(e.Axis)
		ep := open[axis]
		if e.Outcome == AuthoringHandedOut {
			if ep == nil {
				ep = &budgetEpisode{ecosystem: row.Ecosystem, coordinate: coordinate, axis: axis}
				if !firstSeen[axis] {
					firstSeen[axis] = true
					ep.missing = firstMissing
					if !truncated {
						ep.missing = 0
					}
				}
				open[axis] = ep
			}
			peer := peerOrUnknown(peerOf, e.SessionID)
			if peer == "unknown" && e.PeerID != "" {
				peer = e.PeerID
			}
			a := budgetAttempt{at: e.At, session: e.SessionID, peer: peer,
				slot: slotOf[e.SessionID], kind: e.Kind, end: budgetEndOpen}
			// The attempt closes at the next event in this coordinate's
			// history, whatever it is: handouts of one coordinate are
			// exclusive, so the next event is either this writer's answer or
			// the coordinate moving on without one.
			if i+1 < len(history) {
				next := history[i+1]
				gap := next.At.Sub(e.At)
				switch {
				case next.Outcome == AuthoringAuthored && next.SessionID == e.SessionID:
					a.end, a.observed = budgetEndAuthored, true
				case next.Outcome == AuthoringHandedOut && next.SessionID == e.SessionID:
					a.end, a.observed = budgetEndSameSession, true
				case next.Outcome == AuthoringHandedOut:
					a.end = budgetEndOtherSession
				case next.SessionID == e.SessionID:
					a.end, a.observed = budgetEndReportedPrefix+string(next.Outcome), true
					a.refunded = next.Outcome == AuthoringInfrastructure || next.Outcome == AuthoringTransient
				default:
					a.end = budgetEndReportedPrefix + string(next.Outcome)
				}
				if a.observed {
					a.known = minDuration(gap, opts.PrintTimeout)
					a.upper = a.known
				} else {
					a.upper = minDuration(gap, opts.PrintTimeout)
				}
			} else {
				a.upper = opts.PrintTimeout
			}
			ep.attempts = append(ep.attempts, a)
			if ep.kind == "" || e.Kind != "" {
				ep.kind = e.Kind
			}
			continue
		}
		if e.Outcome == AuthoringAuthored && ep == nil && !firstSeen[axis] {
			// The window opens on a success whose handouts all fell out of
			// it. The missing handouts belong to that episode, not to the
			// next one, and it cannot be counted exactly.
			firstSeen[axis] = true
			out = append(out, budgetEpisode{ecosystem: row.Ecosystem, coordinate: coordinate, axis: axis,
				kind: e.Kind, missing: -1, succeeded: true})
			continue
		}
		if e.Outcome == AuthoringAuthored && ep != nil {
			ep.succeeded = true
			if len(ep.attempts) > 0 && ep.attempts[len(ep.attempts)-1].kind != "" {
				ep.kind = ep.attempts[len(ep.attempts)-1].kind
			}
			out = append(out, *ep)
			delete(open, axis)
		}
	}
	for axis, ep := range open {
		gate := &l.authoringAxisLedger
		if normalizeAuthoringAxis(l.Axis) != axis {
			gate = l.Axes[axis]
		}
		if gate != nil && !gate.QuarantinedAt.IsZero() {
			ep.withheld = true
			ep.reason = gate.QuarantineReason
		}
		out = append(out, *ep)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].attempts) == 0 || len(out[j].attempts) == 0 {
			return len(out[i].attempts) > len(out[j].attempts)
		}
		return out[i].attempts[0].at.Before(out[j].attempts[0].at)
	})
	return out, truncated
}

func peerOrUnknown(peerOf map[string]string, session string) string {
	if p, ok := peerOf[session]; ok {
		return p
	}
	return "unknown"
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func (ep budgetEpisode) exact() bool { return ep.missing >= 0 }

func (ep budgetEpisode) attemptCount() int {
	if ep.missing < 0 {
		return len(ep.attempts)
	}
	return ep.missing + len(ep.attempts)
}

func (ep budgetEpisode) slotMinutes() (known, upper float64) {
	for _, a := range ep.attempts {
		known += a.known.Minutes()
		upper += a.upper.Minutes()
	}
	return known, upper
}

func (ep budgetEpisode) outcome() string {
	switch {
	case ep.succeeded:
		return "AUTHORED"
	case ep.withheld:
		return "WITHHELD"
	default:
		return "OPEN"
	}
}

type budgetGroupAcc struct {
	g              AuthoringBudgetGroup
	attempts       []float64
	successMinutes []float64
	failedMinutes  []float64
}

func (acc *budgetGroupAcc) add(ep budgetEpisode) {
	if acc.g.AttemptsToSuccess == nil {
		acc.g.AttemptsToSuccess = map[int]int{}
	}
	acc.g.Episodes++
	known, upper := ep.slotMinutes()
	switch {
	case ep.succeeded:
		acc.g.Succeeded++
		if ep.exact() {
			n := ep.attemptCount()
			acc.g.AttemptsToSuccess[n]++
			acc.attempts = append(acc.attempts, float64(n))
		}
		if len(ep.attempts) > 0 {
			acc.successMinutes = append(acc.successMinutes, known)
		}
	case ep.withheld:
		acc.g.Withheld++
		acc.failedMinutes = append(acc.failedMinutes, upper)
	default:
		acc.g.Open++
		acc.failedMinutes = append(acc.failedMinutes, upper)
	}
}

func (acc *budgetGroupAcc) done() AuthoringBudgetGroup {
	g := acc.g
	if g.AttemptsToSuccess == nil {
		g.AttemptsToSuccess = map[int]int{}
	}
	g.AttemptsSummary = budgetDistribution(acc.attempts)
	g.SuccessSlotMinutes = budgetDistribution(acc.successMinutes)
	g.FailedSlotMinutesUpper = budgetDistribution(acc.failedMinutes)
	return g
}

func summarizeAuthoringBudget(rep *AuthoringBudgetReport, episodes []budgetEpisode, opts AuthoringBudgetOptions) {
	var overall budgetGroupAcc
	byEco, byKind, byAxis := map[string]*budgetGroupAcc{}, map[string]*budgetGroupAcc{}, map[string]*budgetGroupAcc{}
	addTo := func(m map[string]*budgetGroupAcc, key string, ep budgetEpisode) {
		if key == "" {
			key = "UNKNOWN"
		}
		if m[key] == nil {
			m[key] = &budgetGroupAcc{}
		}
		m[key].add(ep)
	}
	rep.AttemptEnds = map[string]int{}
	rep.AuthoredOver = map[string]int{}
	rep.WritersPerEpisode, rep.PeersPerEpisode, rep.SlotsPerEpisode = map[int]int{}, map[int]int{}, map[int]int{}
	rep.SuccessByWriterOrdinal = map[int]int{}
	rep.WithheldByReason = map[string]int{}
	var authoredMinutes []float64
	exactSuccesses := 0
	for _, ep := range episodes {
		if !ep.exact() {
			rep.InexactEpisodes++
		}
		overall.add(ep)
		addTo(byEco, ep.ecosystem, ep)
		addTo(byKind, ep.kind, ep)
		addTo(byAxis, ep.axis, ep)
		if ep.withheld {
			rep.WithheldByReason[budgetReasonKey(ep.reason)]++
		}
		if ep.succeeded && ep.exact() {
			exactSuccesses++
		}
		writers, peers, slots := map[string]int{}, map[string]bool{}, map[string]bool{}
		var order []string
		timeoutSeen := false
		exhausted := false
		for i, a := range ep.attempts {
			rep.AttemptEnds[a.end]++
			if _, ok := writers[a.session]; !ok {
				order = append(order, a.session)
			}
			if !a.refunded {
				writers[a.session]++
			} else if _, ok := writers[a.session]; !ok {
				writers[a.session] = 0
			}
			peers[a.peer] = true
			if a.slot != "" {
				slots[a.slot] = true
			}
			if a.end == budgetEndAuthored {
				authoredMinutes = append(authoredMinutes, a.known.Minutes())
				for _, th := range []int{10, 15, 20, 30, 45} {
					if a.known > time.Duration(th)*time.Minute {
						rep.AuthoredOver[fmt.Sprintf(">%dm", th)]++
					}
				}
				if timeoutSeen {
					rep.TimeoutLikeThenSucceeded++
				}
				// Only a fully observed episode knows which writer came first.
				if ep.missing == 0 {
					if exhausted {
						rep.SuccessAfterWriterExhausted++
					}
					for ord, s := range order {
						if s == a.session {
							rep.SuccessByWriterOrdinal[ord+1]++
						}
					}
				}
			} else if budgetTimeoutLike(a, opts) {
				rep.TimeoutLikeAttempts++
				timeoutSeen = true
			}
			if i < len(ep.attempts)-1 || a.end != budgetEndAuthored {
				for _, n := range writers {
					if n >= AuthoringMaxSessionHandouts {
						exhausted = true
					}
				}
			}
		}
		// An episode whose handouts all fell out of the window names no
		// writer at all; counting it as "zero writers" would be invented.
		if len(ep.attempts) > 0 {
			rep.WritersPerEpisode[len(writers)]++
			rep.PeersPerEpisode[len(peers)]++
			rep.SlotsPerEpisode[len(slots)]++
		}
		if len(writers) >= 2 {
			rep.MultiWriterEpisodes++
			if len(peers) == 1 {
				rep.MultiWriterEpisodesSinglePeer++
			}
		}
		known, upper := ep.slotMinutes()
		if h := round1(known / 60); h > rep.CurrentMaxEpisodeSlotHoursKnown {
			rep.CurrentMaxEpisodeSlotHoursKnown = h
		}
		if h := round1(upper / 60); h > rep.CurrentMaxEpisodeSlotHoursUpper {
			rep.CurrentMaxEpisodeSlotHoursUpper = h
		}
	}
	rep.Overall = overall.done()
	rep.ByEcosystem = doneGroups(byEco)
	rep.ByKind = doneGroups(byKind)
	rep.ByAxis = doneGroups(byAxis)
	rep.AuthoredAttemptMinutes = budgetDistribution(authoredMinutes)
	// The ceiling the thresholds allow for unexcused no-output: six charged
	// handouts at the print timeout, plus the refunded ones that do not count
	// against the coordinate.
	rep.CurrentPolicyDispatchBudgetMinutes = AuthoringEpisodeDispatchBudget.Minutes()
	rep.CurrentPolicyCeilingSlotHours = round1(AuthoringEpisodeSlotLimit.Hours())
	rep.Options = priceAuthoringBudgetOptions(episodes, exactSuccesses, opts)
	rep.Expensive = expensiveEpisodes(episodes, opts.Top)
}

func budgetTimeoutLike(a budgetAttempt, opts AuthoringBudgetOptions) bool {
	if a.end == budgetEndAuthored || a.refunded {
		return false
	}
	return a.upper >= opts.TimeoutLike && (a.observed || a.end == budgetEndOtherSession || a.end == budgetEndOpen)
}

func budgetReasonKey(reason string) string {
	if i := strings.Index(reason, ":"); i > 0 {
		return reason[:i]
	}
	if reason == "" {
		return "unknown"
	}
	return reason
}

func doneGroups(m map[string]*budgetGroupAcc) map[string]AuthoringBudgetGroup {
	out := make(map[string]AuthoringBudgetGroup, len(m))
	for k, acc := range m {
		out[k] = acc.done()
	}
	return out
}

// priceAuthoringBudgetOptions replays each alternative over the episodes.
// A success is lost when the option would have stopped the episode before
// the attempt that authored; slot time is saved on episodes that never
// authored and would have been stopped earlier.
//
// The first option replays today's thresholds. History was produced partly
// under earlier rules (#364 refunded without a per-writer bound) and includes
// operator reopenings, so today's rule replayed over it "loses" some
// successes that really happened. That count is the replay's error bar, and
// every other option is also reported beyond it.
//
// Only Sample episodes are priced. The ledger records AUTHORED for a Sample
// draft alone; Evidence and Dependency answers complete elsewhere, so an
// "open" episode on those axes is not a failure the ledger can see, and
// charging it would invent savings.
func priceAuthoringBudgetOptions(all []budgetEpisode, exactSuccesses int, opts AuthoringBudgetOptions) []AuthoringBudgetOption {
	var episodes []budgetEpisode
	for _, ep := range all {
		if ep.axis == AuthoringAxisSample {
			episodes = append(episodes, ep)
		}
	}
	share := func(lost int) float64 {
		if exactSuccesses == 0 {
			return 0
		}
		return float64(int64(float64(lost)/float64(exactSuccesses)*10000+0.5)) / 10000
	}
	// stopRule prices a rule that stops an episode before attempt i begins
	// once stop says so, and returns the set of successes it would have lost.
	stopRule := func(name, note string, stop func(ep budgetEpisode, i int, spentUpper float64) bool) (AuthoringBudgetOption, map[int]bool) {
		opt := AuthoringBudgetOption{Name: name, Note: note}
		lost := map[int]bool{}
		maxHours := 0.0
		for idx, ep := range episodes {
			if !ep.exact() {
				continue
			}
			spent := 0.0
			stopped := -1
			for i, a := range ep.attempts {
				if stop(ep, i, spent) {
					stopped = i
					break
				}
				spent += a.upper.Minutes()
			}
			if stopped >= 0 {
				if ep.succeeded {
					lost[idx] = true
					if len(opt.LostExamples) < 3 {
						opt.LostExamples = append(opt.LostExamples, ep.ecosystem+":"+ep.coordinate)
					}
				} else {
					for _, a := range ep.attempts[stopped:] {
						opt.SlotMinutesSaved += a.upper.Minutes()
						opt.SlotMinutesSavedObserved += a.known.Minutes()
					}
				}
			}
			if h := spent / 60; h > maxHours {
				maxHours = h
			}
		}
		opt.SuccessesLost = len(lost)
		opt.SuccessesLostShare = share(opt.SuccessesLost)
		opt.SlotMinutesSaved = round1(opt.SlotMinutesSaved)
		opt.SlotMinutesSavedObserved = round1(opt.SlotMinutesSavedObserved)
		opt.MaxEpisodeSlotHours = round1(maxHours)
		return opt, lost
	}
	charged := func(ep budgetEpisode, upto int, weight func(budgetAttempt) int) int {
		n := ep.missing
		for _, a := range ep.attempts[:upto] {
			if !a.refunded {
				n += weight(a)
			}
		}
		return n
	}
	one := func(budgetAttempt) int { return 1 }

	baseline, baselineLost := stopRule("current: no-output quarantine at 6 charged handouts, 3 per session",
		"today's thresholds replayed over history produced partly under earlier rules and operator reopenings; its losses are the replay's error bar",
		func(ep budgetEpisode, i int, _ float64) bool {
			return charged(ep, i, one) >= AuthoringNoOutputQuarantine
		})
	out := []AuthoringBudgetOption{baseline}
	add := func(opt AuthoringBudgetOption, lost map[int]bool) {
		for idx := range lost {
			if !baselineLost[idx] {
				opt.SuccessesLostBeyondCurrent++
			}
		}
		out = append(out, opt)
	}

	add(stopRule("peer independence: 3 charged handouts per machine, park when every machine is exhausted",
		"on a single-machine farm this parks a coordinate after its third charged handout until a second machine exists",
		func(ep budgetEpisode, i int, _ float64) bool {
			perPeer := map[string]int{}
			if ep.missing > 0 && len(ep.attempts) > 0 {
				perPeer[ep.attempts[0].peer] += ep.missing
			}
			for _, a := range ep.attempts[:i] {
				if !a.refunded {
					perPeer[a.peer]++
				}
			}
			return perPeer[ep.attempts[i].peer] >= AuthoringMaxSessionHandouts
		}))

	for _, budget := range opts.SlotBudgets {
		limit := budget.Minutes()
		add(stopRule(fmt.Sprintf("slot budget: %d slot-minutes per episode", int(limit)),
			"cumulative slot time, unobserved closes charged at the print timeout",
			func(_ budgetEpisode, _ int, spent float64) bool { return spent >= limit }))
	}

	for _, timeout := range opts.InitialTimeouts {
		out = append(out, priceInitialTimeout(episodes, exactSuccesses, timeout, opts, share))
	}

	add(stopRule("timeout weighting: a timeout-like attempt counts 2 toward the no-output quarantine",
		"withholds sooner only where writers hit the timeout",
		func(ep budgetEpisode, i int, _ float64) bool {
			return charged(ep, i, func(a budgetAttempt) int {
				if budgetTimeoutLike(a, opts) {
					return 2
				}
				return 1
			}) >= AuthoringNoOutputQuarantine
		}))
	return out
}

// priceInitialTimeout prices a shorter first-attempt timeout with one
// bounded escalation: the first attempt of an episode stops at timeout, and
// every later attempt runs at the print timeout. No success is lost, because
// an authored first attempt cut at timeout is re-run at full length — it
// costs timeout minutes instead.
func priceInitialTimeout(episodes []budgetEpisode, exactSuccesses int, timeout time.Duration, opts AuthoringBudgetOptions, share func(int) float64) AuthoringBudgetOption {
	opt := AuthoringBudgetOption{
		Name: fmt.Sprintf("initial timeout %dm, one escalation to %dm", int(timeout.Minutes()), int(opts.PrintTimeout.Minutes())),
		Note: "saves the cut part of long first attempts that authored nothing; pays the cut run again for first attempts that authored after it",
	}
	maxHours := 0.0
	for _, ep := range episodes {
		if ep.missing != 0 || len(ep.attempts) == 0 {
			continue
		}
		first := ep.attempts[0]
		_, upper := ep.slotMinutes()
		total := upper
		if first.upper > timeout {
			if first.end == budgetEndAuthored {
				opt.SlotMinutesSaved -= timeout.Minutes()
				opt.SlotMinutesSavedObserved -= timeout.Minutes()
				total += timeout.Minutes()
			} else {
				opt.SlotMinutesSaved += (first.upper - timeout).Minutes()
				if first.observed {
					opt.SlotMinutesSavedObserved += (first.known - timeout).Minutes()
				}
				total -= (first.upper - timeout).Minutes()
			}
		}
		if h := total / 60; h > maxHours {
			maxHours = h
		}
	}
	opt.SlotMinutesSaved = round1(opt.SlotMinutesSaved)
	opt.SlotMinutesSavedObserved = round1(opt.SlotMinutesSavedObserved)
	opt.MaxEpisodeSlotHours = round1(maxHours)
	opt.SuccessesLostShare = share(0)
	return opt
}

func expensiveEpisodes(episodes []budgetEpisode, top int) []AuthoringBudgetExpensive {
	if top <= 0 {
		return nil
	}
	rows := make([]AuthoringBudgetExpensive, 0, len(episodes))
	for _, ep := range episodes {
		known, upper := ep.slotMinutes()
		writers, peers := map[string]bool{}, map[string]bool{}
		for _, a := range ep.attempts {
			writers[a.session], peers[a.peer] = true, true
		}
		rows = append(rows, AuthoringBudgetExpensive{
			Ecosystem: ep.ecosystem, Coordinate: ep.coordinate, Axis: ep.axis, Kind: ep.kind,
			Attempts: ep.attemptCount(), Writers: len(writers), Peers: len(peers), Outcome: ep.outcome(),
			SlotMinutesKnown: round1(known), SlotMinutesUpper: round1(upper), HistoryIncomplete: !ep.exact() || ep.missing > 0,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].SlotMinutesUpper != rows[j].SlotMinutesUpper {
			return rows[i].SlotMinutesUpper > rows[j].SlotMinutesUpper
		}
		return rows[i].Coordinate < rows[j].Coordinate
	})
	if len(rows) > top {
		rows = rows[:top]
	}
	return rows
}
