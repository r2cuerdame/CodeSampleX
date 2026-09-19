package fixclaims

// CandidateState is the bounded projection of one queued candidate the
// Phase 0 measurement reads: what it is in, how it got there, and what it
// cost. No claim text, no provenance -- those are not measurements.
type CandidateState struct {
	Status           Status
	PairOutcome      PairOutcome
	Attempts         int
	Exhausted        bool
	ReproducerSource ReproducerSource
	RunCount         int
	FarmSeconds      int64
}

// Metrics are the Phase 0 numbers the issue asks to know before scaling.
// Rates are over the denominators the issue names, and each denominator
// is carried so a reader can see a 1/1 for what it is.
type Metrics struct {
	// Ingested is what producers sent; Rejected is what the validator
	// refused; Accepted is what entered the queue. Extraction precision is
	// Accepted / Ingested, and it is a producer-side number the server
	// counts at the door.
	Ingested int64 `json:"ingested"`
	Rejected int64 `json:"rejected"`
	Accepted int64 `json:"accepted"`

	Attempted      int64 `json:"attempted"`
	WithReproducer int64 `json:"withReproducer"`
	ReusedSample   int64 `json:"reusedSample"`
	Reproduced     int64 `json:"reproduced"`
	FixConfirmed   int64 `json:"fixConfirmed"`
	Partial        int64 `json:"partial"`
	NotReproduced  int64 `json:"notReproduced"`
	NotFixed       int64 `json:"notFixed"`
	Regressed      int64 `json:"regressed"`
	Ambiguous      int64 `json:"ambiguous"`
	Unrunnable     int64 `json:"unrunnable"`
	Exhausted      int64 `json:"exhausted"`
	Runs           int64 `json:"runs"`
	FarmSeconds    int64 `json:"farmSeconds"`

	ByStatus map[Status]int64 `json:"byStatus"`

	// Derived rates, 0..1, each with the denominator named in its comment.
	ExtractionPrecision float64 `json:"extractionPrecision"` // Accepted / Ingested
	ReproducerRate      float64 `json:"reproducerRate"`      // WithReproducer / Attempted
	ReproducedRate      float64 `json:"reproducedRate"`      // Reproduced / WithReproducer
	ConfirmedRate       float64 `json:"confirmedRate"`       // FixConfirmed / Reproduced
	IncorrectClaimRate  float64 `json:"incorrectClaimRate"`  // (Partial + NotFixed) / Reproduced
	ReuseRate           float64 `json:"reuseRate"`           // ReusedSample / WithReproducer
	// FarmSecondsPerVerifiedFix is the cost metric: every Farm second spent
	// in the lane, over the verified fixes it produced.
	FarmSecondsPerVerifiedFix float64 `json:"farmSecondsPerVerifiedFix"`
}

// Measure folds candidate states into the Phase 0 metrics. Ingested and
// Rejected are counted at the ingest door and passed in.
func Measure(states []CandidateState, ingested, rejected int64) Metrics {
	m := Metrics{Ingested: ingested, Rejected: rejected, Accepted: int64(len(states)), ByStatus: map[Status]int64{}}
	for _, s := range Statuses() {
		m.ByStatus[s] = 0
	}
	for _, st := range states {
		m.ByStatus[st.Status]++
		m.Runs += int64(st.RunCount)
		m.FarmSeconds += st.FarmSeconds
		if st.Attempts > 0 {
			m.Attempted++
		}
		if st.Exhausted {
			m.Exhausted++
		}
		if st.ReproducerSource != "" {
			m.WithReproducer++
			if st.ReproducerSource == ReproducerExistingSample {
				m.ReusedSample++
			}
		}
		switch st.Status {
		case StatusReproducedBug:
			m.Reproduced++
			if st.PairOutcome == PairReproducedNotFixed {
				m.NotFixed++
			}
		case StatusVerifiedFix:
			m.Reproduced++
			m.FixConfirmed++
		case StatusPartialFix:
			m.Reproduced++
			m.Partial++
		case StatusRegressed:
			m.Reproduced++
			m.Regressed++
		case StatusClaimNotReproduced:
			m.NotReproduced++
		}
		switch st.PairOutcome {
		case PairAmbiguous:
			m.Ambiguous++
		case PairBadUnrunnable, PairFixedUnrunnable:
			m.Unrunnable++
		}
	}
	m.ExtractionPrecision = ratio(m.Accepted, m.Ingested)
	m.ReproducerRate = ratio(m.WithReproducer, m.Attempted)
	m.ReproducedRate = ratio(m.Reproduced, m.WithReproducer)
	m.ConfirmedRate = ratio(m.FixConfirmed, m.Reproduced)
	m.IncorrectClaimRate = ratio(m.Partial+m.NotFixed, m.Reproduced)
	m.ReuseRate = ratio(m.ReusedSample, m.WithReproducer)
	m.FarmSecondsPerVerifiedFix = ratio(m.FarmSeconds, m.FixConfirmed)
	return m
}

func ratio(n, d int64) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}
