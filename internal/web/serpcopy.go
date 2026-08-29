package web

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// The search-result surface of a sample page.
//
// Measured on 2026-08-27 from the Google Search Console export: 187
// /samples/sha256:* pages had 1,546 impressions and 0 clicks, 157 of them
// ranking inside the first ten results, and those 157 carried 1,393 of the
// impressions. Google was already showing these pages to people; nobody
// clicked one.
//
// The reason is visible in what the page put in front of them. The title was
// built from the sample's goal, and 32 of 56 sampled production manifests
// carry a goal a machine wrote for the authoring worker to start from —
// "verify pkg:npm/browserslist@4.28.7", "verify @babel/core in
// pkg:npm/%40babel/core@7.27.4". So the live title read
//
//	verify pkg:npm/browserslist@4.28.7 · browserslist 4.28.7 — CodeSampleX
//
// which names the package identifier twice, in a scheme with a percent
// escape in it, and answers no question anybody typed. The searches that
// reached these pages were ordinary package lookups — "nanoid npm", "eslint
// 9.39.5", "nanoid 3.3.17" — and the result offered them an internal purl.
//
// So the copy is rebuilt here, from the facts the manifest and the receipts
// actually establish: which package and release, which API, whether a
// contract ran, and where it ran. Nothing in this file may claim more than
// those establish — in particular "verified" is said only when a receipt
// records a contract that passed, the same rule levelBadge applies to the
// badge on the page.

// serpCopy is what a sample page shows a search engine and the reader who
// arrives from one.
type serpCopy struct {
	// Title is the <title>: "{name} {version}: {subject} — {label} | CodeSampleX".
	Title string
	// Headline is the <h1>, the same sentence without the brand suffix and
	// without the length budget a title has.
	Headline string
	// Description is the meta description and the page's own lead paragraph,
	// so the snippet-first sentence is a sentence the reader also sees.
	Description string
	// Slug is the sample's stable, human-readable URL segment.
	Slug string
	// Subject is what the sample is about, without the release in front of
	// it. The breadcrumb's last step uses it, so the trail and the title
	// name the page with the same words.
	Subject string
	// Verified is whether a receipt records a contract that passed. It
	// decides the label, and it is the only thing that may.
	Verified bool
}

// serpInput is everything the copy is allowed to be built from.
type serpInput struct {
	SampleID  string
	Ecosystem string
	Name      string
	Version   string
	Goal      string
	Symbols   []string
	// Contract is the manifest's assertion list. Only the first line is used
	// and only when a contract actually ran: the lines say what was asserted,
	// and on a sample nothing ran for, that is a plan rather than a result.
	Contract []string
	// RunEnvironment is where the passing contract ran — the receipt's
	// environment, not the author's declared one.
	RunEnvironment string
	Verified       bool
}

// titleBudget bounds the part of a title that has to survive truncation.
//
// Google renders roughly 600 pixels of title, about sixty characters at the
// widths it uses. The brand suffix is meant to be the part that falls off,
// so the package, the release and the subject are kept inside this budget
// and everything after " — " is expendable.
const titleBudget = 60

// descriptionBudget bounds the meta description. Google shows about 155-160
// characters of it; past that the sentence is cut mid-word by the crawler
// rather than on a boundary chosen here.
const descriptionBudget = 158

// machineGoalRe matches the goal `csx sample-worker next` prints for an
// authoring agent to start from (internal/cli/sample_worker.go): "verify
// <package>", or "verify <symbol> in <package>". Agents are expected to
// replace it with the question the sample answers and often do not, so it
// reaches the corpus as the sample's permanent goal — and a published
// sample is immutable, so every one already published still carries it.
var machineGoalRe = regexp.MustCompile(`(?i)^\s*verify\s+(?:(.+?)\s+in\s+)?(pkg:\S+)\s*$`)

// purlTokenRe finds a package URL anywhere in free text.
var purlTokenRe = regexp.MustCompile(`pkg:\S+`)

// goalSubject reports the searchable subject a human goal carries, and
// whether the goal was machine-written.
//
// A machine goal contributes no prose at all: what it holds besides the purl
// is the symbol, which the manifest states more completely in Symbols.
func goalSubject(goal string) (subject string, machine bool) {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return "", false
	}
	if m := machineGoalRe.FindStringSubmatch(goal); m != nil {
		return apiLabel(m[1]), true
	}
	// Not the machine shape, but a hand-written goal may still quote a purl
	// ("Handle X in pkg:npm/y@1.2.3"). The purl is an identifier, and the
	// page states the package and release separately, so it is dropped
	// rather than shown twice.
	cleaned := strings.TrimSpace(purlTokenRe.ReplaceAllString(goal, ""))
	cleaned = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(cleaned), " in"))
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	return cleaned, false
}

// apiLabel shortens one symbol to the part a person recognizes.
//
// Published symbols are fully qualified — "github.com/jackc/pgx/v5/pgconn.
// ParseConfig", "@modelcontextprotocol/sdk.LATEST_PROTOCOL_VERSION",
// `Symfony\Component\Console\Application`. The module path in front is
// identity; the searchable name is what follows the last path separator,
// and the dot qualifier immediately before it is kept because
// "pgconn.ParseConfig" is what the documentation calls it.
func apiLabel(symbol string) string {
	s := strings.TrimSpace(symbol)
	if i := strings.LastIndexAny(s, `/\`); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

// symbolSubjectLimit bounds how many APIs a title names. Past three the
// list stops being read and starts eating the release out of the budget.
const symbolSubjectLimit = 3

// dropPackageQualifier removes the package's own name from the front of an
// API label.
//
// Published symbols are qualified with it — browserslist names
// "browserslist.parseConfig" and "browserslist.coverage" — and the title
// says the package already, one word to the left. Repeating it there costs
// twenty-six characters of a sixty-character budget to say nothing, and
// turns "browserslist 4.28.7: parseConfig, coverage" into a line that
// truncates before it names the second API.
//
// A label that IS the package name says nothing at all and comes back empty;
// the caller keeps the unqualified list when every label drops out that way.
func dropPackageQualifier(name, label string) string {
	pkg := apiLabel(name)
	if pkg == "" || label == "" {
		return label
	}
	if strings.EqualFold(label, pkg) {
		return ""
	}
	if len(label) > len(pkg)+1 && label[len(pkg)] == '.' &&
		strings.EqualFold(label[:len(pkg)], pkg) {
		return label[len(pkg)+1:]
	}
	return label
}

// symbolSubject is the API list a sample answers for, deduplicated and
// bounded, as it should read beside the package name.
func symbolSubject(name string, symbols []string) string {
	labels := make([]string, 0, len(symbols))
	seen := map[string]bool{}
	for _, sym := range symbols {
		label := apiLabel(sym)
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true
		labels = append(labels, label)
	}
	unqualified := make([]string, 0, len(labels))
	for _, label := range labels {
		if short := dropPackageQualifier(name, label); short != "" {
			unqualified = append(unqualified, short)
		}
	}
	// Every symbol was the package's own name. Nothing is gained by dropping
	// the list entirely, so the qualified form stands.
	if len(unqualified) > 0 {
		labels = unqualified
	}
	if len(labels) > symbolSubjectLimit {
		labels = labels[:symbolSubjectLimit]
	}
	return strings.Join(labels, ", ")
}

// sampleSubject is what this sample is about, in the words a person would
// search for: the author's own goal when there is one, and otherwise the
// APIs the manifest names.
func sampleSubject(name, goal string, symbols []string) string {
	subject, machine := goalSubject(goal)
	if !machine && subject != "" {
		return subject
	}
	// A machine goal. The manifest's symbol list is the fuller statement of
	// the same thing — tslib's goal names no symbol at all while its
	// manifest names __assign, __rest and __spreadArray — so it wins, and
	// the goal's own symbol is the fallback when the list is empty.
	if apis := symbolSubject(name, symbols); apis != "" {
		return apis
	}
	return dropPackageQualifier(name, subject)
}

// releaseLabel is "browserslist 4.28.7", the coordinate people type.
func releaseLabel(name, version string) string {
	switch {
	case name == "":
		return ""
	case version == "":
		return name
	}
	return name + " " + version
}

// serpHeadline is the sentence the title and the <h1> are both built from.
func serpHeadline(name, version, subject string) string {
	release := releaseLabel(name, version)
	switch {
	case release == "":
		return subject
	case subject == "":
		return release
	}
	return release + ": " + subject
}

// buildSerpCopy assembles the whole search surface of one sample.
func buildSerpCopy(lang string, in serpInput) serpCopy {
	subject := sampleSubject(in.Name, in.Goal, in.Symbols)
	headline := serpHeadline(in.Name, in.Version, subject)

	label := i18n.T(lang, "serp.label_source")
	if in.Verified {
		label = i18n.T(lang, "serp.label_verified")
	}

	out := serpCopy{
		Headline: headline,
		Subject:  subject,
		Verified: in.Verified,
		Slug:     sampleSlug(in.SampleID, subject),
	}
	if headline != "" {
		out.Title = truncateOnBoundary(headline, titleBudget) +
			" — " + label + " | CodeSampleX"
	} else {
		// Nothing about the sample is nameable — no package, no goal, no
		// symbol. The content address is all that is left, and it is stated
		// as identity rather than dressed up as a subject.
		out.Title = i18n.T(lang, "sample.title") + " " + shortHash(in.SampleID) + " — CodeSampleX"
		out.Headline = ""
	}
	out.Description = serpDescription(lang, label, in, subject)
	return out
}

// serpDescription writes the snippet: what this is, for which release, which
// APIs, and — when a contract actually ran — where it ran and what it
// established. The first line of the contract is the strongest sentence the
// page owns, because it is a claim that was executed rather than written.
func serpDescription(lang, label string, in serpInput, subject string) string {
	release := releaseLabel(in.Name, in.Version)
	var head string
	switch {
	case release != "" && in.Ecosystem != "":
		head = i18n.T(lang, "serp.desc_for", label, in.Ecosystem+" "+release)
	case release != "":
		head = i18n.T(lang, "serp.desc_for", label, release)
	default:
		head = label
	}
	if subject != "" && subject != release {
		head += ": " + subject
	}
	head = strings.TrimRight(head, ".") + "."

	var tail string
	if in.Verified {
		if in.RunEnvironment != "" {
			tail = i18n.T(lang, "serp.desc_ran_on", in.RunEnvironment)
		} else {
			tail = i18n.T(lang, "serp.desc_ran")
		}
		if line := firstContractLine(in.Contract); line != "" {
			tail += " " + line
		}
	} else {
		// No receipt records a contract that passed, so the contract lines
		// are assertions nothing has executed. Saying so is the whole
		// description; quoting them here would read as a result.
		tail = i18n.T(lang, "serp.desc_source_only")
	}
	return truncateOnBoundary(strings.TrimSpace(head+" "+tail), descriptionBudget)
}

// firstContractLine is the leading assertion, normalized to one line.
func firstContractLine(contract []string) string {
	for _, line := range contract {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			return line
		}
	}
	return ""
}

// truncateOnBoundary cuts s to at most n characters, on a word boundary,
// appending an ellipsis when anything was dropped. It counts runes: the
// goal text is authored English but the surrounding chrome is translated,
// and cutting a multi-byte character in half produces a broken title rather
// than a short one.
func truncateOnBoundary(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	cut := runes[:n]
	// Back up to the last separator so the cut lands between words. Without
	// a separator in range the hard cut stands: one long token is better
	// truncated than dropped entirely.
	for i := len(cut) - 1; i > n/2; i-- {
		if unicode.IsSpace(cut[i]) || cut[i] == ',' || cut[i] == ';' {
			cut = cut[:i]
			break
		}
	}
	return strings.TrimRight(strings.TrimRightFunc(string(cut), unicode.IsSpace), ",;:·-") + "…"
}

// ---------------------------------------------------------------------------
// Human-readable sample URLs.

// sampleSlugMaxLen bounds the readable half of a slug.
const sampleSlugMaxLen = 60

// sampleDigestLen is how much of the content address the slug carries.
//
// The slug has to be unique among the samples of one release and it has to
// be STABLE: a sample published tomorrow must not be able to take an URL
// that has already been indexed. Deriving it from the subject alone gives
// neither — two samples for the same release can answer for the same API,
// and resolving that collision by "whoever was first keeps the clean slug"
// makes an existing page's canonical URL a function of what is published
// after it. Carrying a piece of the sample's own content address makes the
// slug a function of the sample and nothing else. Eight hex characters is
// four billion values inside one release coordinate.
const sampleDigestLen = 8

var slugSepRe = regexp.MustCompile(`[^a-z0-9]+`)

// sampleSlug is the stable human-readable segment of a sample's URL.
func sampleSlug(sampleID, subject string) string {
	digest := slugDigest(sampleID)
	if digest == "" {
		return ""
	}
	readable := slugify(subject)
	if readable == "" {
		return digest
	}
	if len([]rune(readable)) > sampleSlugMaxLen {
		readable = string([]rune(readable)[:sampleSlugMaxLen])
		if i := strings.LastIndexByte(readable, '-'); i > 0 {
			readable = readable[:i]
		}
		readable = strings.Trim(readable, "-")
	}
	if readable == "" {
		return digest
	}
	return readable + "-" + digest
}

// slugDigest is the hexadecimal tail of a content address, shortened.
// Anything that is not a hash returns "", and the sample then keeps the
// content-addressed URL as its canonical one rather than being advertised
// at an address that cannot be resolved back.
func slugDigest(sampleID string) string {
	hex := sampleID
	if i := strings.LastIndexByte(hex, ':'); i >= 0 {
		hex = hex[i+1:]
	}
	hex = strings.ToLower(hex)
	if len(hex) < sampleDigestLen {
		return ""
	}
	for _, r := range hex[:sampleDigestLen] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return ""
		}
	}
	return hex[:sampleDigestLen]
}

// slugify lowercases and reduces to [a-z0-9-]. Symbol punctuation ("__assign",
// "pgconn.ParseConfig") becomes separators rather than being escaped, so the
// URL stays typeable.
func slugify(s string) string {
	return strings.Trim(slugSepRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// sampleSegment is the literal that marks a sample under a release path.
const sampleSegment = "samples"

// semanticSampleHref is the human-readable canonical path of a sample:
// /npm/browserslist/4.28.7/samples/parseconfig-coverage-5a2468d2.
//
// It returns "" when the sample names no release this site routes — no
// package, no version, an ecosystem with no explorer — and the sample then
// keeps its content-addressed URL as canonical. The result is checked by
// splitting it back apart with the router's own splitter, because a URL
// this function emits goes into the sitemap and into rel=canonical: an
// address the router cannot resolve back to the same coordinate would be
// advertised to crawlers as a page that does not exist.
func semanticSampleHref(ecosystem, name, version, slug string) string {
	if ecosystem == "" || name == "" || version == "" || slug == "" {
		return ""
	}
	if !knownEcosystems[ecosystem] {
		return ""
	}
	if strings.ContainsAny(version, "/") || strings.ContainsAny(slug, "/") {
		return ""
	}
	rest := name + "/" + version + "/" + sampleSegment + "/" + slug
	gotName, gotVersion, tail, ok := splitPackageRest(ecosystem, rest)
	if !ok || gotName != name || gotVersion != version {
		return ""
	}
	if len(tail) != 2 || tail[0] != sampleSegment || tail[1] != slug {
		return ""
	}
	return "/" + ecosystem + "/" + escapePathSegments(name) + "/" +
		escapePathSegments(version) + "/" + sampleSegment + "/" + slug
}

// sampleRelease is the release a sample is filed under: the first package
// in its manifest that names a version.
func sampleRelease(purls []string) (ecosystem, name, version string) {
	for _, raw := range purls {
		p, err := domain.ParsePURL(raw)
		if err != nil || p.Version == "" {
			continue
		}
		return p.Ecosystem, p.Name, p.Version
	}
	return "", "", ""
}
