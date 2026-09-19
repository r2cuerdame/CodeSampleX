package fixclaims

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Source is one package the collector reads release notes for: the CSX
// coordinate and the GitHub repository that publishes its releases.
type Source struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	// Repo is "owner/name" on github.com.
	Repo string `json:"repo"`
}

// Release is one GitHub release as the collector reads it. The fields are
// the subset of the Releases API the extractor needs.
type Release struct {
	Tag         string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	URL         string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
}

// Skipped is one release-note line the extractor read and did not turn
// into a candidate, with the reason, so extraction precision can be
// measured from what was left out as well as what was kept.
type Skipped struct {
	Release string `json:"release"`
	Line    string `json:"line"`
	Reason  string `json:"reason"`
}

// Limit keeps at most max candidates, taking one package at a time in
// turn, highest confidence first within each package. Round-robin is what
// keeps ten packages ten packages: measured on the Phase 0 sources (#444),
// undici alone produced 31 candidates and a confidence-only cut left three
// packages with nothing. It is how a Phase 0 run stays inside its candidate
// budget without the collector reading fewer releases.
func Limit(candidates []Candidate, max int) []Candidate {
	if max <= 0 || len(candidates) <= max {
		return candidates
	}
	rank := map[Confidence]int{ConfidenceHigh: 0, ConfidenceMedium: 1, ConfidenceLow: 2}
	var order []string
	groups := map[string][]Candidate{}
	for _, c := range candidates {
		key := c.Ecosystem + "/" + c.Name
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], c)
	}
	for _, key := range order {
		g := groups[key]
		sort.SliceStable(g, func(i, j int) bool { return rank[g[i].Confidence] < rank[g[j].Confidence] })
		groups[key] = g
	}
	var out []Candidate
	for len(out) < max {
		took := false
		for _, key := range order {
			if len(out) >= max {
				break
			}
			if g := groups[key]; len(g) > 0 {
				out = append(out, g[0])
				groups[key] = g[1:]
				took = true
			}
		}
		if !took {
			break
		}
	}
	return out
}

// Collection is the extractor's output for one source.
type Collection struct {
	Source     Source      `json:"source"`
	Releases   int         `json:"releases"`
	Candidates []Candidate `json:"candidates"`
	Skipped    []Skipped   `json:"skipped,omitempty"`
}

const (
	// maxCandidatesPerRelease keeps one enormous release from filling the
	// queue by itself; the lines kept are the first in note order, which is
	// where maintainers put what mattered.
	maxCandidatesPerRelease = 10
	maxReleaseBody          = 64 << 10
	collectorResponseLimit  = 4 << 20
)

var (
	fixLine      = regexp.MustCompile(`(?i)\b(fix(es|ed|ing)?|resolve[sd]?|prevent(s|ed)?|no longer|correct(s|ed|ly)?|regression|crash(es|ed)?|avoid(s|ed)?|repair(s|ed)?|handle[sd]?|stop(s|ped)?)\b`)
	bulletPrefix = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+`)
	mdLink       = regexp.MustCompile(`\[([^\]]*)\]\((https?://[^)\s]+)\)`)
	bareURL      = regexp.MustCompile(`https?://[^\s)>]+`)
	issueRef     = regexp.MustCompile(`(?:^|[\s(])#(\d+)\b`)
	byUserIn     = regexp.MustCompile(`(?i)\s+by\s+@[\w-]+\s+in\s+(?:https?://\S+|#\d+)\s*$`)
	codeSpan     = regexp.MustCompile("`([^`]{1,120})`")
	boldMark     = regexp.MustCompile(`\*\*|__`)
	// parenRefs is "(#508)" or "(#508, #509)" or "(https://...)" -- a
	// reference list in parentheses, removed whole so no bracket is left
	// behind in the claim text.
	parenRefs    = regexp.MustCompile(`\(\s*(?:#\d+|https?://[^)\s]+)(?:\s*,\s*(?:#\d+|https?://[^)\s]+))*\s*\)`)
	tagVersionRe = regexp.MustCompile(`(\d+\.\d+(?:\.\d+)*[\w.+-]*)$`)
	commitHash   = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	leadingHash  = regexp.MustCompile(`^[0-9a-f]{7,40}\s+`)
	envHintRe    = regexp.MustCompile(`(?i)\b(windows|win32|linux|macos|mac os|darwin|alpine|musl|arm64|aarch64|node(?:\.js)?\s*v?(\d+)|python\s*(3\.\d+)|bun|deno|jdk\s*(\d+)|java\s*(\d+))\b`)
)

// ExtractFromReleases is the pure half of the collector: given the
// releases of one source, newest first, it returns every executable
// bug-fix claim it can read as a candidate. The claimed-fixed release is
// the release the line appears in; the claimed-bad release is the previous
// non-prerelease release in the list, because a note names the fix and
// almost never the last release that still carried the bug.
//
// It is heuristic and it is meant to be: what it produces are candidates
// for the validator and the queue, never evidence. An AGY lane may refine
// them (agy.go); the validator treats both producers alike.
func ExtractFromReleases(src Source, releases []Release, limit int) Collection {
	col := Collection{Source: src}
	var stable []Release
	for _, r := range releases {
		if r.Draft || r.Prerelease {
			continue
		}
		if tagVersion(r.Tag, r.Name) == "" {
			continue
		}
		stable = append(stable, r)
	}
	// Newest first by publication, so the limit keeps the recent releases;
	// the claimed-bad release is then the highest release below the fixed
	// one in the same major line, by version rather than by date, because
	// maintenance lines interleave on the calendar (undici 6.x, 7.x and 8.x
	// all shipped within a week). A release with no lower sibling in its
	// line names no bad version and leaves it to the queue.
	sort.SliceStable(stable, func(i, j int) bool { return stable[i].PublishedAt.After(stable[j].PublishedAt) })
	if limit > 0 && len(stable) > limit {
		stable = stable[:limit]
	}
	col.Releases = len(stable)
	var versions []string
	for _, r := range stable {
		versions = append(versions, tagVersion(r.Tag, r.Name))
	}
	for _, r := range stable {
		fixed := tagVersion(r.Tag, r.Name)
		bad := previousInLine(versions, fixed)
		kept := 0
		body := r.Body
		if len(body) > maxReleaseBody {
			body = body[:maxReleaseBody]
		}
		for _, raw := range strings.Split(body, "\n") {
			line := strings.TrimSpace(raw)
			if line == "" || strings.HasPrefix(line, "#") || !fixLine.MatchString(line) {
				continue
			}
			if kept >= maxCandidatesPerRelease {
				col.Skipped = append(col.Skipped, Skipped{Release: r.Tag, Line: trimLine(line), Reason: "release-cap"})
				continue
			}
			cand, reason := candidateFromLine(src, r, line, fixed, bad)
			if reason != "" {
				col.Skipped = append(col.Skipped, Skipped{Release: r.Tag, Line: trimLine(line), Reason: reason})
				continue
			}
			if _, rejections := Validate(cand); len(rejections) > 0 {
				col.Skipped = append(col.Skipped, Skipped{Release: r.Tag, Line: trimLine(line), Reason: rejections[0].Code})
				continue
			}
			col.Candidates = append(col.Candidates, cand)
			kept++
		}
	}
	return col
}

func trimLine(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

func candidateFromLine(src Source, r Release, line, fixed, bad string) (Candidate, string) {
	text := bulletPrefix.ReplaceAllString(line, "")
	var refs []string
	repoBase := "https://github.com/" + src.Repo
	for _, m := range mdLink.FindAllStringSubmatch(text, -1) {
		refs = append(refs, m[2])
	}
	text = mdLink.ReplaceAllString(text, "$1")
	for _, m := range issueRef.FindAllStringSubmatch(text, -1) {
		refs = append(refs, repoBase+"/issues/"+m[1])
	}
	for _, u := range bareURL.FindAllString(text, -1) {
		refs = append(refs, u)
	}
	text = byUserIn.ReplaceAllString(text, "")
	text = parenRefs.ReplaceAllString(text, "")
	text = bareURL.ReplaceAllString(text, "")
	text = issueRef.ReplaceAllString(text, " ")
	var symbols []string
	for _, m := range codeSpan.FindAllStringSubmatch(text, -1) {
		if s := strings.TrimSpace(m[1]); symbolToken.MatchString(s) && !strings.ContainsAny(s, " ") && !commitHash.MatchString(s) {
			symbols = append(symbols, s)
		}
	}
	text = codeSpan.ReplaceAllString(text, "$1")
	text = boldMark.ReplaceAllString(text, "")
	// A leading commit hash ("413cce9a fix: ...") is provenance, not claim.
	text = leadingHash.ReplaceAllString(text, "")
	text = claimSpace.ReplaceAllString(text, " ")
	text = strings.TrimSpace(strings.Trim(text, " .:-"))
	if text == "" {
		return Candidate{}, "empty"
	}
	sourceType := SourceReleaseNote
	sourceURL := r.URL
	if sourceURL == "" {
		sourceURL = repoBase + "/releases/tag/" + url.PathEscape(r.Tag)
	}
	confidence := ConfidenceLow
	switch {
	case len(symbols) > 0 && len(refs) > 0:
		confidence = ConfidenceHigh
	case len(symbols) > 0 || len(refs) > 0:
		confidence = ConfidenceMedium
	}
	if len(refs) > maxReferences {
		refs = refs[:maxReferences]
	}
	if len(symbols) > maxSymbols {
		symbols = symbols[:maxSymbols]
	}
	c := Candidate{
		SchemaVersion:       CandidateSchemaVersion,
		Ecosystem:           src.Ecosystem,
		Name:                src.Name,
		ClaimedBadVersion:   bad,
		ClaimedFixedVersion: fixed,
		Claim:               text,
		SourceURL:           sourceURL,
		SourceType:          sourceType,
		References:          refs,
		Symbols:             symbols,
		EnvironmentHints:    environmentHints(text),
		Confidence:          confidence,
		ReleasedAt:          r.PublishedAt,
	}
	return c, ""
}

// previousInLine is the release a fix in v most plausibly repaired: the
// highest known release below v on the same minor line; failing that, for
// a patch release x.y.z, its own predecessor x.y.(z-1) even when the
// collector did not read it; failing that, the highest known release below
// v with the same major. "" when there is none.
//
// The middle rule exists because maintenance lines interleave: tokio
// 1.52.3 (May) carried #8062, and the highest lower release among the five
// read was 1.51.4 (July), which carries the same fix backported. Naming it
// the bad release produced a pair that could only come out
// CLAIM_NOT_REPRODUCED. The release a patch fixes is the patch before it.
func previousInLine(known []string, v string) string {
	if prev := previousKnown(known, v); prev != "" && minorOf(prev) == minorOf(v) {
		return prev
	}
	if prev := patchBefore(v); prev != "" {
		return prev
	}
	prev := previousKnown(known, v)
	if prev == "" || majorOf(prev) != majorOf(v) {
		return ""
	}
	return prev
}

// patchBefore returns x.y.(z-1) for a plain x.y.z with z > 0, else "".
func patchBefore(v string) string {
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return ""
	}
	for _, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return ""
		}
	}
	z, err := strconv.Atoi(parts[2])
	if err != nil || z <= 0 {
		return ""
	}
	prev := parts[0] + "." + parts[1] + "." + strconv.Itoa(z-1)
	if strings.HasPrefix(v, "v") {
		prev = "v" + prev
	}
	return prev
}

// minorOf is "x.y" of a version, or the whole version when it has no minor.
func minorOf(v string) string {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return v
	}
	return parts[0] + "." + parts[1]
}

func majorOf(v string) string {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, ".-+"); i >= 0 {
		return v[:i]
	}
	return v
}

// tagVersion reads a release version out of a tag: "v1.2.3", "1.2.3",
// "pkg@1.2.3" (monorepos), "release-1.2.3", "name-1.2.3". The release name
// is tried when the tag carries no version.
func tagVersion(tag, name string) string {
	for _, s := range []string{tag, name} {
		s = strings.TrimSpace(s)
		if i := strings.LastIndex(s, "@"); i >= 0 {
			s = s[i+1:]
		}
		if m := tagVersionRe.FindStringSubmatch(s); m != nil {
			return m[1]
		}
	}
	return ""
}

// environmentHints reads the OS and runtime tokens a line ties the bug to.
func environmentHints(text string) []string {
	var out []string
	for _, m := range envHintRe.FindAllStringSubmatch(text, -1) {
		word := strings.ToLower(m[1])
		switch {
		case strings.HasPrefix(word, "node"):
			if m[2] != "" {
				out = append(out, "node-"+m[2])
			} else {
				out = append(out, "node")
			}
		case strings.HasPrefix(word, "python"):
			out = append(out, "python-"+m[3])
		case strings.HasPrefix(word, "jdk"), strings.HasPrefix(word, "java"):
			v := m[4] + m[5]
			if v != "" {
				out = append(out, "java-"+v)
			} else {
				out = append(out, "java")
			}
		case word == "win32":
			out = append(out, "windows")
		case word == "mac os", word == "macos", word == "darwin":
			out = append(out, "macos")
		case word == "aarch64":
			out = append(out, "arm64")
		default:
			out = append(out, word)
		}
	}
	return dedupeSorted(out, true)
}

// Collector fetches releases from the GitHub REST API. Client and BaseURL
// are test seams; a nil Client uses a ten-second timeout and BaseURL
// defaults to https://api.github.com.
type Collector struct {
	Client  *http.Client
	BaseURL string
	// Token is an optional GitHub token for the rate limit; it is sent as a
	// bearer and never logged.
	Token string
}

// Collect reads the newest releases of one source and extracts candidates.
// releases bounds how many stable releases are read (the Phase 0 number is
// five).
func (c *Collector) Collect(ctx context.Context, src Source, releases int) (Collection, error) {
	if releases <= 0 {
		releases = 5
	}
	if !validRepo(src.Repo) {
		return Collection{}, fmt.Errorf("fixclaims: repo must be owner/name, got %q", src.Repo)
	}
	base := c.BaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	// Prereleases and drafts are read and dropped, so ask for more than the
	// stable count wanted; the API caps a page at 100.
	perPage := releases * 3
	if perPage > 100 {
		perPage = 100
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/repos/"+src.Repo+"/releases?per_page="+strconv.Itoa(perPage), nil)
	if err != nil {
		return Collection{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "codesamplex-fixclaims")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return Collection{}, fmt.Errorf("fixclaims: fetch releases: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, collectorResponseLimit))
	if err != nil {
		return Collection{}, fmt.Errorf("fixclaims: read releases: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Collection{}, fmt.Errorf("fixclaims: releases for %s: HTTP %d", src.Repo, resp.StatusCode)
	}
	var rels []Release
	if err := json.Unmarshal(body, &rels); err != nil {
		return Collection{}, fmt.Errorf("fixclaims: decode releases: %w", err)
	}
	return ExtractFromReleases(src, rels, releases), nil
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func validRepo(repo string) bool { return repoRe.MatchString(repo) }
