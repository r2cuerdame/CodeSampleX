package httpapi

// NO_WORK with nothing else in it is the answer that cost a farm four and a
// half hours.
//
// Three workers polled eighty times, every answer was 200 NO_WORK, and the
// board held 2,961 claimable coordinates the whole time. The handler puts
// candidates through six steps and any one of them can empty the list, but
// the answer named none of them, so both sides were reduced to reading the
// code and guessing. The operator's own report ended with "그 경로엔 로그가
// 없어서 안 보입니다" — and it was right.
//
// The funnel is counts and rule names. No package name, version, symbol or
// path travels in it. What it says is how many candidates survived each step,
// which is exactly what separates "the board is empty" from "this request is
// being refused" — the two situations that look identical from outside and
// need opposite fixes.
type authoringFunnel struct {
	// Wanted and Expansion are what the two candidate sources returned.
	Wanted    int `json:"wanted"`
	Expansion int `json:"expansion"`
	// WantedEligible and ExpansionEligible are what survived this request's
	// own constraints: sandbox capability, verifier OS, and the rules about
	// coordinates nothing can be written against.
	WantedEligible    int `json:"wantedEligible"`
	ExpansionEligible int `json:"expansionEligible"`
	// AfterDependency and AfterUnauthorable are what survived the two
	// registry-backed drops.
	AfterDependency   int `json:"afterDependency"`
	AfterUnauthorable int `json:"afterUnauthorable"`
	// Offered is what the claim was actually given to choose from.
	Offered int `json:"offered"`
}

// reason names the step that emptied the list, so the operator does not have
// to diff seven numbers to find the zero.
func (f authoringFunnel) reason() string {
	switch {
	case f.Wanted+f.Expansion == 0:
		return "no candidates"
	case f.WantedEligible+f.ExpansionEligible == 0:
		return "no candidate is eligible for this request"
	case f.AfterUnauthorable == 0:
		return "dropped as unauthorable"
	case f.Offered == 0:
		return "nothing left to offer"
	default:
		return "already claimed or already authored"
	}
}

// noWork is the body returned when there is nothing to hand out.
func (f authoringFunnel) noWork() map[string]any {
	return map[string]any{
		"status": "NO_WORK",
		"reason": f.reason(),
		"funnel": f,
	}
}
