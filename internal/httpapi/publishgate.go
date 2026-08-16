package httpapi

import (
	"net/http"
	"strings"
)

// This file decides WHO may put a sample into the network.
//
// The network's value is that a sample was independently generated and then
// executed under a contract. That claim survives exactly as long as nobody
// can hand it code of unknown origin. Accepting donated code costs things
// that cannot be undone once they are in the corpus:
//
//   - the licence of a snippet nobody can trace,
//   - a company's internal code pasted in by someone who did not think,
//   - a sample whose provenance is "found on the internet",
//
// and every one of those spreads, because the whole point of this network
// is that other people's models copy from it. Retracting a wrong sample is
// possible; retracting a licence problem after a thousand agents have read
// it is not. This is the §3.8 rule applied to contribution rather than to
// search: a wrong sample is worse than a missing one.
//
// What stays open is the half that actually needs a crowd. A sample does
// not get better because more people wrote it — it gets noisier. Evidence
// does: it needs other machines, other libc versions, other runtimes, and
// there is no way to buy that except from people who have them. So the
// split is by ACTION, not by identity, and §8.6 holds — evidence and search
// remain anonymous with no account anywhere near them. Only this one write
// path asks who you are.
//
// Open to anyone, unchanged:
//   - usage evidence and verification receipts (anonymous)
//   - search, shards, snapshots, every read
//   - the wanted board, which records what the network was asked for and
//     could not answer

// publishingOpen reports whether anonymous sample upload is allowed.
//
// "seeded" is the default and the shipped policy. "open" exists for local
// development and end-to-end runs, and follows the same shape as
// PublicCheck's "strict" | "trust" so there is one idiom for "this
// deployment is not the public one".
func (a *api) publishingOpen() bool {
	return strings.EqualFold(strings.TrimSpace(a.d.Cfg.Publishing), "open")
}

// requireSeeder refuses sample upload from a caller the server cannot
// attribute it to.
//
// It resolves the token itself rather than reading what limitPublish
// already looked up: the two middlewares answer different questions — one
// picks a budget, this one decides admission — and a gate that depends on
// another middleware having run is a gate that opens when the route is
// rewired. Publishing is a rare call, so the second lookup costs nothing
// worth trading correctness for.
func (a *api) requireSeeder(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.publishingOpen() {
			h(w, r)
			return
		}
		tok := bearerToken(r)
		if tok == "" {
			writeErr(w, http.StatusForbidden, publishClosedMessage)
			return
		}
		id, ok, err := a.d.Store.IdentityByAPIToken(r.Context(), sha256Hex(tok))
		if err != nil {
			// A store that cannot answer must not be read as "allowed".
			// Failing open here would make an outage the way in.
			writeErr(w, http.StatusServiceUnavailable,
				"cannot verify the publishing token right now; retry shortly")
			return
		}
		if !ok || id.Login == "" {
			writeErr(w, http.StatusForbidden, publishClosedMessage)
			return
		}
		h(w, r)
	}
}

// publishClosedMessage is the whole policy in the place a person actually
// meets it. A refusal that only says "forbidden" teaches nothing, and the
// three things that ARE open are the point of saying it at all.
const publishClosedMessage = "sample upload is not open. " +
	"Every official sample is generated in a clean room and verified by " +
	"running its contract, so the network does not accept code whose origin " +
	"it cannot establish. What is open to everyone, with no account: " +
	"contributing usage evidence and verification receipts (anonymous), " +
	"reporting a bug or a patch worth a regression case, and asking for a " +
	"sample — every search that ends in NO_SAFE_MATCH is already recorded as " +
	"a request. See https://codesamplex.dev/contribute"
