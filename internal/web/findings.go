package web

import (
	"net/http"
	"sort"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// This file is the /findings page: the measured corrections the network has
// accumulated, each one pointing at the published sample whose contract
// proves it.
//
// Why the list is a Go literal rather than a query. A finding is a claim
// about the world written in English; nothing in the snapshot store holds
// one. What the store does hold is the sample, and the sample is what makes
// the claim checkable — so every entry here carries a SampleID and the page
// is worth exactly as much as those ids are. Each id below was resolved
// against the live network (POST /v1/search, matched on the case goal) and
// confirmed to answer 200 at GET /v1/samples/<id>. An entry whose sample
// could not be confirmed live does not belong in this slice.
//
// Language: the page chrome goes through i18n like every other page. The
// findings themselves stay in English on purpose — each one quotes an error
// string, an attribute name or a numeric result, and a translated
// paraphrase of a measurement is no longer the measurement. This is stated
// on the page rather than left for the reader to notice.

// finding is one measured correction of a widely held belief.
//
// Belief and Measured are one sentence each, deliberately: the pitch is the
// restraint. Source is set wherever the belief can be traced to an official
// document rather than to what the internet repeats, in either group, so a
// reader can check both halves. Carrying a source is not by itself enough
// to lead the page — see documentedFindings for the bar that is.
type finding struct {
	Ecosystem   string // network ecosystem chip: cargo | npm | pypi | …
	Subject     string // the pinned thing measured, with its version
	Believed    string
	Measured    string
	SampleID    string // content address of the published sample
	SourceURL   string // official document the belief comes from, "" if none
	SourceLabel string
}

// documentedFindings are the ones where the belief is printed in the
// project's own documentation or in the specification it implements, AND
// the measurement contradicts it. They lead because both halves are
// checkable: the quote is a link away and the measurement is a container
// run away.
//
// The bar for this slice is deliberately higher than "we quoted a
// document". Two entries that used to sit here were moved down to
// believedFindings after the documents were re-read:
//
//   - orjson's README says JSONEncodeError "is a subclass of TypeError".
//     It is TypeError, and issubclass(TypeError, TypeError) is true, so the
//     README is correct and nothing contradicts it. What is wrong is the
//     inference that the except clause is narrower.
//   - Node's crypto docs state the scrypt rule as "It is an error when
//     (approximately) 128 * N * r > maxmem". The word "approximately" is
//     the documentation's own, so an exact boundary 3072 bytes higher
//     refines that sentence rather than contradicting it.
//
// Both are still on the page, still carry their source link, and are worth
// as much as before. They are simply not doc contradictions.
var documentedFindings = []finding{
	{
		Ecosystem: "cargo",
		Subject:   "serde 1.0.229, serde_json 1.0.151",
		Believed: "serde's container-attribute documentation says deny_unknown_fields " +
			"“is not supported in combination with flatten, neither on the outer struct " +
			"nor on the flattened field”, which reads as a pair the derive will refuse.",
		Measured: "serde_derive implements the pair: rustc compiles a struct carrying both " +
			"with an empty stderr — no error, no warning — and the ordinary case works, " +
			"since a flattened struct is accepted and only genuine leftovers are rejected. " +
			"What does break is narrower than the note: a flattened map beside " +
			"deny_unknown_fields can never collect a key, because every key it was meant " +
			"to catch is reported unknown instead; and a struct carrying " +
			"deny_unknown_fields accepts, once flattened into another struct, the same key " +
			"it rejects when it stands alone.",
		SampleID:    "sha256:0c0a0329b6b2d901cce677b9664dd22b748c2a5249af00e202c0d2e12eab06f0",
		SourceURL:   "https://serde.rs/container-attrs.html#deny_unknown_fields",
		SourceLabel: "serde.rs container attributes",
	},
	{
		Ecosystem: "npm",
		Subject:   "jose 6.2.8, Node 22",
		Believed: "RFC 7518 §3.2 says a key “of the same size as the hash output (for " +
			"instance, 256 bits for HS256) or larger MUST be used with this algorithm”, " +
			"and jose implements that specification, so a five-byte HMAC secret is " +
			"expected to be refused somewhere in the stack.",
		Measured: "jose signs and verifies an HS256 token with a five-byte key and reports " +
			"nothing, and WebCrypto underneath imports the same 40-bit HMAC key just as " +
			"willingly, so nothing below the library catches a weak secret either.",
		SampleID:    "sha256:4fdfb16090032500adac0a6c3479120970ee468326a4e237aa65cf091c30cc7d",
		SourceURL:   "https://www.rfc-editor.org/rfc/rfc7518#section-3.2",
		SourceLabel: "RFC 7518 §3.2",
	},
}

// believedFindings are the ones where the belief is folklore, migration
// advice or a habit carried over from a neighbouring library.
var believedFindings = []finding{
	{
		Ecosystem: "npm",
		Subject:   "bcryptjs 3.0.3, Node 22",
		Believed:  "a password hash covers the whole password.",
		Measured: "bcrypt truncates at 72 bytes and neither hashSync nor compareSync " +
			"reports it: a second, different password sharing the first one's 72-byte " +
			"prefix verifies against that hash, and so does the bare prefix, while 71 " +
			"bytes does not; the limit is bytes, so 36 accented characters survive and 37 " +
			"lose their tail, and bcryptjs's own truncates() is a separate call you have " +
			"to make yourself.",
		SampleID: "sha256:bf227476fbe2d665af4c03c6862eecaa6a1760441669b86d3def01bcc85a4d35",
	},
	{
		Ecosystem: "npm",
		Subject:   "node:crypto scryptSync, Node 22",
		Believed: "the memory scrypt needs is 128 * N * r — the figure Node's own " +
			"documentation quotes, hedged as “It is an error when (approximately) " +
			"128 * N * r > maxmem” — so budgeting exactly that raises the cost safely.",
		Measured: "budgeting exactly 128 * N * r is rejected with " +
			"ERR_CRYPTO_INVALID_SCRYPT_PARAMS; the accepted minimum is exactly " +
			"128 * r * (N + p + 2), pinned to the byte at six parameter sets that vary " +
			"N, r and p independently — 3072 bytes above the quoted figure, at the " +
			"defaults and at N=32768 alike, and at N=32768 the quoted figure is exactly " +
			"the 32 MiB default maxmem, so the next cost step looks like it just fits " +
			"and does not.",
		SampleID:    "sha256:bf227476fbe2d665af4c03c6862eecaa6a1760441669b86d3def01bcc85a4d35",
		SourceURL:   "https://nodejs.org/api/crypto.html",
		SourceLabel: "nodejs.org crypto",
	},
	{
		Ecosystem: "pypi",
		Subject:   "polars 1.43.2, Python 3.12",
		Believed:  "df[mask] filters rows, the way it does in pandas.",
		Measured: "a boolean mask in brackets selects COLUMNS: against a frame whose column " +
			"count differs from the mask length it raises ValueError, and against a frame " +
			"with as many columns as the mask has rows — the shape of most test fixtures — " +
			"the length check passes and it returns the wrong columns with no error at all.",
		SampleID: "sha256:a238e626062c628c548646e46144b6aac21a07492cf10e1617eb4286c853ac05",
	},
	{
		Ecosystem: "composer",
		Subject:   "monolog/monolog 3.9.0, PHP 8",
		Believed: "Monolog 3 removed the integer level constants, so an upgrade means " +
			"replacing every Logger::WARNING.",
		Measured: "Logger::WARNING is still defined, still 300, still equal to " +
			"Level::Warning->value, and addRecord still accepts an int; what actually " +
			"breaks is is_array($record), because a record is now a LogRecord object that " +
			"keeps answering $record['message'] — so the migration reads as finished while " +
			"the array branches quietly stop being taken.",
		SampleID: "sha256:01f8118921bdd76c2acee316b2c81f508152d0fa45d432fa38ab667e764524e1",
	},
	{
		Ecosystem: "npm",
		Subject:   "lightningcss 1.33.0, npm 10, Alpine",
		Believed: "npm installs only the right native package on Alpine, because each " +
			"platform package declares the libc it was built for.",
		Measured: "npm ci installed BOTH linux-x64 variants, glibc and musl, because the " +
			"lockfile this npm 10 image wrote records os and cpu on every optional entry " +
			"and never libc — every darwin, win32 and arm64 package was correctly " +
			"skipped, and the unusable glibc build is a full second copy of the 10 MB " +
			"addon on disk. This is npm's lockfile writer, not lightningcss, and it is " +
			"fixed upstream: npm records libc from 11.11.0 on, so the repair is " +
			"regenerating the lockfile rather than upgrading the npm that reads it.",
		SampleID: "sha256:de0de7aea8370a5ddbb611169fcb1bf333cb27e4a8318c0be8d18fb57c45682e",
	},
	{
		Ecosystem: "npm",
		Subject:   "esbuild 0.25.12, Alpine",
		Believed:  "running esbuild on Alpine means installing its musl-specific build.",
		Measured: "there is no musl build to install: none of esbuild's optional platform " +
			"dependencies mentions musl, the linux-x64 package npm picks declares no libc " +
			"constraint, and its binary has no ELF interpreter at all — read from the " +
			"program headers on a musl image where node itself names musl's loader.",
		SampleID: "sha256:a0f58db3d8de78c03e494ff716388a0e06984c3dff37049d5dc94bcf7bcfe720",
	},
	{
		Ecosystem: "npm",
		Subject:   "jose 6.2.8, Node 22",
		Believed: "catching JWTClaimValidationFailed handles the claim checks jwtVerify " +
			"performs.",
		Measured: "JWTExpired is a sibling of JWTClaimValidationFailed rather than a " +
			"subclass, so every expired token falls past that clause into the generic " +
			"branch, while a not-yet-valid nbf — checked without being asked — is caught " +
			"by it.",
		SampleID: "sha256:4fdfb16090032500adac0a6c3479120970ee468326a4e237aa65cf091c30cc7d",
	},
	{
		Ecosystem: "npm",
		Subject:   "zod 4.4.3, Node 22",
		Believed:  "z.coerce.number() parses a numeric string, rejecting what is not a number.",
		Measured: "it is Number(input) followed by the number check, not a numeric parser, " +
			"so \"\", \"   \", null, false and [] are all accepted and arrive as 0 — an " +
			"empty form field or a null column silently becomes zero — while \"1e999\" is " +
			"rejected, because Number() overflows it to Infinity.",
		SampleID: "sha256:a382ae2a31a7ab492fdd32bd455c4460df064c7a5fc94698cef12db9624d30a2",
	},
	{
		Ecosystem: "npm",
		Subject:   "vitest 4.1.10, Node 22",
		Believed: "a forgotten await on expect(...).rejects makes the test pass while " +
			"asserting nothing.",
		Measured: "which half is true depends on the test function: in a sync one the " +
			"forgotten await still fails the test and carries the real rejection message, " +
			"and in an async one the assertion settles first, so the test is reported " +
			"passed and the failure becomes the run's single unhandled error — green test, " +
			"red run, and any tooling reading only test states calls it a pass.",
		SampleID: "sha256:6c28ffd780aef897dcac706acedb270bbeded523f0b0570d4755789de89cb4ab",
	},
	{
		Ecosystem: "npm",
		Subject:   "bun:sqlite, Bun 1.3.14",
		Believed:  "the options argument to new Database(path, options) overrides defaults.",
		Measured: "it replaces the open flags outright, so new Database(\":memory:\", {}) " +
			"throws SQLiteError SQLITE_MISUSE where new Database(\":memory:\") works, and " +
			"{ create: false } lands on the same zero flags — only a true access mode or a " +
			"strict/safeIntegers key puts the default back.",
		SampleID: "sha256:6aa79cb39a2ca09b9f9be36d65c6b5ee6b822c96560fc9dee9d80d1840830d1c",
	},
	{
		Ecosystem: "composer",
		Subject:   "guzzlehttp/guzzle 8.0.2, PHP 8",
		Believed: "if ($e->hasResponse()) { $e->getResponse(); } is how you read the " +
			"response off a Guzzle RequestException.",
		Measured: "on guzzle 8 neither method is declared on RequestException — getResponse " +
			"moved down to ResponseException with a non-nullable return type, and " +
			"hasResponse is declared on neither class — so the guzzle 7 idiom is a fatal " +
			"“Call to undefined method …::getResponse()”, not a deprecation.",
		SampleID: "sha256:66d105189c76d32ccb87b901f8712d7a4836f4457091fb85af42cf277a9c0f30",
	},
	{
		Ecosystem: "gem",
		Subject:   "json 2.9.1, Ruby 3",
		Believed:  "JSON.dump is JSON.generate under another name.",
		Measured: "JSON.dump defaults to allow_nan, so it writes {\"ratio\":NaN} — a " +
			"document JSON.parse refuses and only JSON.load will read back — while " +
			"JSON.generate refuses the same float outright; JSON.load carries the matching " +
			"asymmetry with allow_blank, returning nil for an empty string where " +
			"JSON.parse raises.",
		SampleID: "sha256:bec86395ce786b02c01ea89597d38d802ad97a4c997dc95aaa3f09e24aeeffe9",
	},
	{
		Ecosystem: "hex",
		Subject:   "Elixir's built-in JSON vs jason 1.4.4",
		Believed:  "dropping Jason for the JSON module in Elixir 1.18+ is a module rename.",
		Measured: "the two encode the same payload byte for byte, but the decode error is " +
			"JSON.DecodeError, so a rescue Jason.DecodeError clause compiles, still reads " +
			"correctly and catches nothing; JSON.decode/1 returns a bare reason tuple " +
			"rather than a struct, and JSON.decode/2 does not exist, so keys: :atoms has " +
			"nowhere to go.",
		SampleID: "sha256:ea6674cc172b56536f71eb007cd144607c953026a433abcf43d85c22299b8c83",
	},
	{
		Ecosystem: "pub",
		Subject:   "collection 1.19.1, Dart 3",
		Believed:  "two Lists holding the same values are equal.",
		Measured: "== on a List, Map or Set is identity, and package:test's equals matcher " +
			"deep-compares — so the assertion is green on exactly the values production " +
			"calls unequal; a const collection is canonicalized and does compare equal, " +
			"which is what makes the rule look inconsistent, and copying it loses the " +
			"equality again.",
		SampleID: "sha256:7227421d006842fac6a96b0d2190f4a93f7cbbd5a2ee8a34116cb7c2f7b20d1c",
	},
	{
		Ecosystem: "golang",
		Subject:   "shopspring/decimal 1.4.0, Go 1.26",
		Believed:  "a decimal type is exact, which is the reason to reach for one.",
		Measured: "Div is DivRound reading a mutable package-level global, " +
			"decimal.DivisionPrecision, which defaults to 16 — so (1/3)*3 is " +
			"0.9999999999999999 and not 1, any dependency in the process can move the " +
			"precision, and nothing at the call site says so; DivRound takes the precision " +
			"as an argument and QuoRem is the one that keeps the remainder.",
		SampleID: "sha256:84206d243236399467879a1efbd7f91542f9c90b1d044a06e8611694204998bd",
	},
	{
		Ecosystem: "golang",
		Subject:   "spf13/cobra 1.10.2, Go 1.26",
		Believed:  "a cobra command tree with no SetArgs runs with no arguments.",
		Measured: "with no SetArgs at all cobra parses os.Args[1:], so a command tree " +
			"driven from a test binary parses that binary's own arguments and fails on a " +
			"flag nobody wrote; the guard is c.args == nil, so SetArgs(nil) reopens the " +
			"same fallback and only an empty non-nil slice means “no arguments”. cobra's " +
			"one escape hatch keys on the program name being exactly cobra.test, which " +
			"saves its own tests and nobody else's.",
		SampleID: "sha256:40b43e1c51d66679bac8ef03d3e56f00b6f9bb2c8bc4f36791b2196260a8cce1",
	},
	{
		Ecosystem: "pypi",
		Subject:   "orjson 3.11.9, Python 3.12",
		Believed: "orjson's README says “JSONEncodeError is a subclass of TypeError”, " +
			"which reads as a narrower class, so except orjson.JSONEncodeError looks " +
			"tighter than except TypeError.",
		Measured: "orjson.JSONEncodeError is TypeError — the same object, so the README's " +
			"sentence holds only in the sense that every class is a subclass of itself — " +
			"and the two clauses are therefore identical: either one swallows every " +
			"TypeError raised in the block, not only the encoder's. JSONDecodeError, by " +
			"contrast, really is a distinct subclass of json.JSONDecodeError.",
		SampleID:    "sha256:84dc3de1142d9942eb7c819b260089b9174ebd77f0213fd16a72c9f0640eb35e",
		SourceURL:   "https://github.com/ijl/orjson#serialize",
		SourceLabel: "orjson README",
	},
	{
		Ecosystem: "pypi",
		Subject:   "attrs 26.1.0 vs dataclasses, CPython 3.12",
		Believed:  "@dataclass(slots=True) is the stdlib equivalent of an attrs slotted class.",
		Measured: "on CPython 3.12 the stdlib leaves the __class__ closure cell pointing at " +
			"the class it discarded, so zero-argument super() inside a slotted dataclass " +
			"raises TypeError at call time — not at definition — while the identical attrs " +
			"class works because attrs rebinds the cell; weakref.ref also raises on the " +
			"slotted dataclass and not on the attrs one.",
		SampleID: "sha256:db459e2a5619e5bc96877e0fc6aefd6229be3342cc28685624eacd9e170bf4b7",
	},
	{
		Ecosystem: "pypi",
		Subject:   "freezegun 1.5.5, Python 3.12",
		Believed:  "freeze_time moves the wall clock, so monotonic timers are unaffected.",
		Measured: "it patches time.monotonic and time.perf_counter to the same frozen wall " +
			"clock, so a duration measured across the boundary of the freeze comes out " +
			"about thirty years long, and moving the frozen clock backwards makes " +
			"time.monotonic() go backwards — a deadline written as monotonic() + timeout " +
			"is never reached.",
		SampleID: "sha256:e0a5abccbe96dde0a46b9b65aae94c3d5ffaa94f5eac58c72b82bec2141a0bfa",
	},
}

type findingsPage struct {
	basePage
	Documented []finding
	Believed   []finding
	Total      int
	Ecosystems []string
}

// SampleHref is the public page for the sample that proves this finding.
// The id is a content address, so the link is stable for as long as the
// sample is published.
func (f finding) SampleHref() string { return "/samples/" + f.SampleID }

// ShortID is the sample id without its hash-algorithm prefix, for a link
// label that does not swamp the sentence above it.
func (f finding) ShortID() string {
	id := f.SampleID
	for i := 0; i < len(id); i++ {
		if id[i] == ':' {
			id = id[i+1:]
			break
		}
	}
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func (s *site) findings(w http.ResponseWriter, r *http.Request) {
	lang := s.negotiate(w, r)
	b := s.page(r, lang, i18n.T(lang, "findings.title")+" — CodeSampleX",
		i18n.T(lang, "meta.findings"))

	seen := map[string]bool{}
	var ecos []string
	for _, list := range [][]finding{documentedFindings, believedFindings} {
		for _, f := range list {
			if !seen[f.Ecosystem] {
				seen[f.Ecosystem] = true
				ecos = append(ecos, f.Ecosystem)
			}
		}
	}
	sort.Strings(ecos)

	s.render(w, "findings", http.StatusOK, findingsPage{
		basePage:   b,
		Documented: documentedFindings,
		Believed:   believedFindings,
		Total:      len(documentedFindings) + len(believedFindings),
		Ecosystems: ecos,
	})
}
