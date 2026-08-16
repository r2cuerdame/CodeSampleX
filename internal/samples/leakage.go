package samples

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Finding is one potential leak in a sample tree. Findings never block
// sample creation, but they block publish until reviewed (goal.md §9.3).
type Finding struct {
	File    string `json:"file"` // slash-relative path inside the sample
	Line    int    `json:"line"` // 1-based
	Kind    string `json:"kind"`
	Excerpt string `json:"excerpt"`
}

// Finding kinds.
const (
	KindAWSKey        = "AWS_ACCESS_KEY"
	KindGitHubToken   = "GITHUB_TOKEN"
	KindAPIKey        = "API_KEY"
	KindGoogleKey     = "GOOGLE_API_KEY"
	KindPrivateKey    = "PRIVATE_KEY"
	KindEmail         = "EMAIL"
	KindAbsolutePath  = "ABSOLUTE_PATH"
	KindURL           = "URL"
	KindEnvAssignment = "ENV_ASSIGNMENT"
	KindProjectName   = "PROJECT_NAME"
	// KindUnscanned marks a file the scanner could not read or was too
	// large to read. It is a finding rather than a silence on purpose: the
	// publish gate refuses on findings, and "we did not look" must not be
	// reported as "there is nothing there".
	KindUnscanned = "UNSCANNED"
)

// ScanOptions parameterizes project-identifying checks. Both names come
// from the contributing project (its directory name and the repository
// name of its git remote); a sample mentioning either leaks provenance.
type ScanOptions struct {
	ProjectDirName    string
	GitRemoteName     string
	ExtraAllowedHosts []string // appended to the URL allowlist
}

// allowedURLHosts may appear in samples: public registries plus
// documentation-safe hosts (plan C13).
var allowedURLHosts = []string{
	"registry.npmjs.org",
	"pypi.org",
	"files.pythonhosted.org",
	"crates.io",
	"static.crates.io",
	"proxy.golang.org",
	// The canonical public registry of every wired ecosystem. These appear
	// in ordinary hand-written manifests, not only in lockfiles: a Gemfile
	// opens with `source "https://rubygems.org"` and a composer.json may
	// name repo.packagist.org. Treating those as leaks rejected every Ruby,
	// Dart, PHP and Elixir sample at publish time — the check was blocking
	// whole ecosystems rather than protecting anyone.
	"rubygems.org",
	"index.rubygems.org",
	"pub.dev",
	"pub.dartlang.org",
	"packagist.org",
	"getcomposer.org",
	"hex.pm",
	"deno.land",
	"jsr.io",
	"example.com",
	"example.org",
	"localhost",
	"127.0.0.1",
	"codesamplex.dev",
	// The fixed sentinel host test clients invent for a request that never
	// leaves the process. Starlette's TestClient and Django's test client
	// both use it, so it appears in every FastAPI test ever written and
	// identifies nothing.
	"testserver",
	// Funding and metadata hosts that package managers write into
	// lockfiles. They identify the library's maintainers, never the
	// contributor, and a lockfile is machine-generated public data.
	"github.com",
	"opencollective.com",
	"tidelift.com",
	"paypal.me",
	"patreon.com",
	"ko-fi.com",
	"feross.org",
	"spdx.org",
	"json.schemastore.org",
	// Where a library's own release notes live.
	//
	// A sample that proves a bugfix has to say where the fix was
	// ANNOUNCED, or the claim is just an assertion again — the whole point
	// of that axis is "the notes say this was fixed in x.y.z, and here is
	// the contract that ran". The gate was refusing the citation, so a
	// dateutil sample citing dateutil's own changelog could not be
	// published. These are the publishing platforms every ecosystem's docs
	// and changelogs sit on: they identify the library, never the
	// contributor.
	"readthedocs.io",
	"docs.rs",
	"pkg.go.dev",
	"go.dev",
	"crates.io",
	"docs.python.org",
	"peps.python.org",
	"nodejs.org",
	"developer.mozilla.org",
	"rust-lang.github.io",
	"doc.rust-lang.org",
	"hexdocs.pm",
	"api.dart.dev",
	"php.net",
	"ruby-doc.org",
	"gitlab.com",
	"sourceforge.net",
	"rfc-editor.org",
	"ietf.org",
	"w3.org",
	"unicode.org",
	// A specification identifier is not an address. $schema carries
	// "http://json-schema.org/draft-07/schema#" in every JSON Schema ever
	// generated, and nothing ever fetches it — it names a draft, the way an
	// SPDX id names a licence. It refused a sample for publishing the schema
	// its own tool emitted.
	"json-schema.org",
	"schema.org",
	"www.w3.org",
	"docs.oasis-open.org",
}

type leakPattern struct {
	kind string
	re   *regexp.Regexp
}

var leakPatterns = []leakPattern{
	{KindAWSKey, regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{KindGitHubToken, regexp.MustCompile(`\bghp_[A-Za-z0-9]{36}\b`)},
	{KindGitHubToken, regexp.MustCompile(`\bgithub_pat_\w+`)},
	{KindAPIKey, regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`)},
	{KindGoogleKey, regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`)},
	{KindPrivateKey, regexp.MustCompile(`-----BEGIN.*PRIVATE KEY`)},
	{KindEmail, regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)},
	// Windows absolute paths (drive-letter rooted).
	{KindAbsolutePath, regexp.MustCompile(`\b[A-Za-z]:[\\/][^\s"'` + "`" + `]+`)},
	// Unix absolute paths rooted at user/system dirs (keeps route strings
	// like "/api/users" out of the findings).
	//
	// /usr and /etc are deliberately NOT here. Those trees are owned by the
	// distribution and are byte-identical on every machine running it, so a
	// sample naming /usr/lib/x86_64-linux-gnu/libc.so.6 is describing the
	// platform, not its author — and a sample about musl versus glibc has
	// to name them. Everything left can carry a person, a project or an
	// employer in its next segment, which is what this check is for.
	{KindAbsolutePath, regexp.MustCompile(`(?:^|[^\w.@])(/(?:home|Users|var|tmp|opt|mnt|srv|root|private)/[A-Za-z0-9._/-]+)`)},
	// UNC shares. The leading pair must follow a delimiter, because a
	// backslash-separated identifier inside JSON looks exactly like one once
	// the escaping doubles it: composer.lock stores the PHP class
	// Monolog\Log\Logger as "Monolog\\Log\\Logger", and the middle of that
	// string matched. A real UNC path always begins a value, so requiring a
	// delimiter in front costs nothing and drops the whole false-positive
	// class — it was rejecting a sample whose only crime was PHP namespaces.
	// The optional second pair covers the same path once JSON has escaped
	// it: four backslashes can only come from escaping a real UNC prefix,
	// never from a namespace separator, which is always exactly two.
	// The trailing segment is restricted to what a path segment is actually
	// made of, because "anything that is not whitespace" also describes a
	// regular expression. A sample about the regex crate contains
	// r"(\\N\{[^}]+})|([{}])", whose middle reads as two backslashes, a
	// name, a backslash and then junk — and the publish gate refused it
	// with no override flag. A false positive here is a contributor turned
	// away for writing exactly the sample the network asked for; a leak
	// inside a path containing regex metacharacters is the rarer thing by
	// a wide margin.
	// The segment after the separator must BEGIN with a path character, or
	// the engine simply backtracks: with a backslash allowed anywhere in
	// the trailing class, \\N\{ matches by letting the class eat the second
	// backslash of the pair it just skipped.
	{KindAbsolutePath, regexp.MustCompile(`(?:^|[\s"'=:(\[,])(\\{2}(?:\\{2})?[A-Za-z0-9._-]+\\{1,2}[A-Za-z0-9._-][A-Za-z0-9._\\/-]*)`)},
	// Only secret-shaped environment names: a sample legitimately sets TZ,
	// NODE_ENV or PORT, and flagging those blocked honest contributions.
	//
	// And only when something is actually ASSIGNED. A sample about dotenv
	// has to demonstrate what an empty value does, so it writes
	// process.env.EMPTY_TOKEN = "" -- which carries no secret by
	// construction, and was refused at publish with no override anyway. The
	// quote pair must have something between it.
	{KindEnvAssignment, regexp.MustCompile(`process\.env\.\w*(?i:KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|AUTH|DSN|CONN)\w*\s*=\s*["'` + "`" + `][^"'` + "`" + `\s]`)},
	{KindEnvAssignment, regexp.MustCompile(`os\.environ\[\s*["']\w*(?i:KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|AUTH|DSN|CONN)\w*["']\s*\]\s*=\s*["'][^"'\s]`)},
}

var urlRe = regexp.MustCompile(`https?://[^\s"'<>()\[\]` + "`" + `]+`)

// userinfoURLRe matches any URL carrying userinfo — https://SOMETHING@host
// — with or without a colon inside it.
//
// The previous pattern required the colon, so it caught user:token@host and
// missed every single-token form: https://ghp_xxxx@github.com/ (a GitHub
// PAT), https://npm_xxxx@registry.npmjs.org/ (a registry token), and
// https://dXNlcjpwYXNz@host/ — base64 of "user:password", which is the
// same secret with the colon hidden inside the encoding. Those are the
// shapes a token actually takes; user:pass in a URL is the rare one.
//
// The character class excludes "/", so it cannot run past the host and a
// scoped npm path like registry.npmjs.org/@scope/pkg is not userinfo.
var userinfoURLRe = regexp.MustCompile(`https?://([^/\s@]+)@`)

// harmlessUserinfo are userinfo values that are conventions rather than
// secrets. git@ appears in every lockfile that resolves a git dependency,
// and flagging it would reject honest samples to catch nothing.
var harmlessUserinfo = map[string]bool{
	"git": true, "anonymous": true, "ftp": true,
}

// credentialURL reports whether a URL carries userinfo that could be a
// secret. Anything with a colon is user:password and always counts; a
// single token counts unless it is one of the known conventions, because
// a token is exactly what a single opaque userinfo value usually is.
func credentialURL(u string) bool {
	m := userinfoURLRe.FindStringSubmatch(u)
	if m == nil {
		return false
	}
	info := m[1]
	if strings.Contains(info, ":") {
		return true
	}
	return !harmlessUserinfo[strings.ToLower(info)]
}

// lockfileNames are machine-generated dependency manifests. They describe
// public packages, are written by the package manager rather than by the
// contributor, and are exactly what a sample must ship to be reproducible.
var lockfileNames = map[string]bool{
	"package-lock.json": true, "npm-shrinkwrap.json": true,
	"pnpm-lock.yaml": true, "yarn.lock": true,
	"cargo.lock": true, "poetry.lock": true, "uv.lock": true,
	"go.sum": true, "gemfile.lock": true, "composer.lock": true,
	// requirements.txt is NOT here. The exemption's whole justification is
	// that these files are written by the package manager rather than by
	// the contributor — and a requirements.txt is hand-authored. It is also
	// exactly where a private index gets named:
	//   --index-url https://pypi.internal.acmecorp.io/simple
	// went through unflagged, publishing an employer's internal package
	// host, while the byte-identical line in install.sh was caught. The
	// public indexes are already on the host allowlist, so an honest file
	// still passes.
	"pubspec.lock": true, "mix.lock": true, "bun.lock": true,
	"deno.lock": true,
}

func isLockfile(file string) bool {
	return lockfileNames[strings.ToLower(filepath.Base(filepath.FromSlash(file)))]
}

const (
	// maxScanFileBytes bounds one file the scanner reads. It must be at
	// least as large as anything an artifact can carry, or a file can be
	// published without ever being looked at.
	//
	// It was 2MB while Unpack allows 8MB, so every file in between went
	// out unscanned -- and unscanned meant SILENT: `csx sample create`
	// printed "Leakage findings: 0", the publish gate found nothing to
	// refuse while telling the user "There is no override flag", and a
	// 3MB fixture with an AWS key in the middle of it was uploaded.
	maxScanFileBytes = maxUnpackedBytes
	maxExcerptLen    = 80
)

// Scan walks the sample tree at dir and returns every potential leak.
// It is advisory at creation time and blocking at publish time.
func Scan(dir string, opts ScanOptions) ([]Finding, error) {
	nameRes := projectNamePatterns(opts)
	var findings []Finding

	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != dir && (forbiddenDir(d.Name()) || generatedDir(d.Name())) {
				return filepath.SkipDir // never reaches the artifact
			}
			if rel, rerr := filepath.Rel(dir, p); rerr == nil && generatedRootDir(filepath.ToSlash(rel)) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel0, rerr0 := filepath.Rel(dir, p)
		if rerr0 != nil {
			return rerr0
		}
		relSlash := filepath.ToSlash(rel0)

		info, ierr := d.Info()
		if ierr != nil {
			findings = append(findings, Finding{
				File: relSlash, Line: 1, Kind: KindUnscanned,
				Excerpt: "could not be read, so it was never checked",
			})
			return nil
		}
		if info.Size() > maxScanFileBytes {
			findings = append(findings, Finding{
				File: relSlash, Line: 1, Kind: KindUnscanned,
				Excerpt: fmt.Sprintf("%d bytes: too large to check", info.Size()),
			})
			return nil
		}
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if strings.ContainsRune(string(raw), 0) {
			return nil // binary; the artifact builder rejects it anyway
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		findings = append(findings, scanContent(filepath.ToSlash(rel), string(raw), opts, nameRes)...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return findings, nil
}

func scanContent(file, content string, opts ScanOptions, nameRes []*regexp.Regexp) []Finding {
	var out []Finding
	lock := isLockfile(file)
	for i, line := range strings.Split(content, "\n") {
		lineNo := i + 1
		for _, lp := range leakPatterns {
			for _, m := range lp.re.FindAllString(line, -1) {
				if lp.kind == KindEmail && reservedEmail(m) {
					continue // RFC 2606 address: documentation, not a person
				}
				// The same reasoning that exempts lockfile URLs exempts the
				// addresses beside them: composer.lock, Gemfile.lock and
				// pubspec.lock copy each package's authors block verbatim
				// from the registry. Those identify the LIBRARY's
				// maintainers — published on Packagist and rubygems for
				// anyone to read — and cannot contain anything the
				// contributor wrote. Flagging them blocked every PHP sample
				// outright: one composer.lock carries 38 of them.
				// Credentials and keys are still caught here, in lockfiles
				// too, because those a contributor genuinely can leak.
				if lp.kind == KindEmail && lock {
					continue
				}
				out = append(out, Finding{File: file, Line: lineNo, Kind: lp.kind, Excerpt: excerpt(m)})
			}
		}
		for _, m := range urlRe.FindAllString(line, -1) {
			// A lockfile is machine-generated public metadata about public
			// packages: its URLs are registry and funding links chosen by
			// each maintainer, so no host allowlist can ever cover them and
			// none of them can carry anything the contributor wrote.
			// Credentials embedded in one still matter, and are caught below.
			if lock {
				if credentialURL(m) {
					out = append(out, Finding{File: file, Line: lineNo, Kind: KindURL, Excerpt: excerpt(m)})
				}
				continue
			}
			// Credentials first, and regardless of the host. The allowlist
			// answers "is it normal to link to this place", which is a
			// different question from "is there a secret in this string" --
			// so https://npm_token@registry.npmjs.org/ passed, because the
			// host is exactly the one a sample is expected to reference.
			// The lockfile branch above already checks this; source files,
			// where a contributor actually types things, did not.
			if credentialURL(m) {
				out = append(out, Finding{File: file, Line: lineNo, Kind: KindURL, Excerpt: excerpt(m)})
				continue
			}
			if !urlAllowed(m, opts.ExtraAllowedHosts) {
				out = append(out, Finding{File: file, Line: lineNo, Kind: KindURL, Excerpt: excerpt(m)})
			}
		}
		for _, re := range nameRes {
			if m := re.FindString(line); m != "" {
				out = append(out, Finding{File: file, Line: lineNo, Kind: KindProjectName, Excerpt: excerpt(m)})
			}
		}
	}
	return out
}

func projectNamePatterns(opts ScanOptions) []*regexp.Regexp {
	var res []*regexp.Regexp
	for _, name := range []string{opts.ProjectDirName, opts.GitRemoteName} {
		if len(name) < 3 {
			continue // too short to match meaningfully
		}
		res = append(res, regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(name)+`\b`))
	}
	return res
}

// templateExprRe matches an interpolation placeholder that a URL literal
// may carry: `http://127.0.0.1:${port}/x`, "http://%s/x", f"http://{host}/x".
var templateExprRe = regexp.MustCompile(`\$\{[^}]*\}|\{[A-Za-z_][\w.]*\}|%[sdv]`)

// reservedNames are the RFC 2606 / RFC 6761 names set aside for
// documentation and tests. A host or address under them identifies
// nobody by construction, and flagging them blocked otherwise-clean
// samples — an example URL is exactly what a good sample contains.
var reservedNames = []string{
	"example.com", "example.org", "example.net", "example.edu",
	"test", "invalid", "localhost", "example", "local",
}

// isReservedName reports whether a host (or an email's domain part) is a
// reserved documentation name, including any subdomain of one.
func isReservedName(host string) bool {
	host = strings.ToLower(strings.Trim(host, "."))
	for _, d := range reservedNames {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

func reservedEmail(addr string) bool {
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return false
	}
	return isReservedName(addr[at+1:])
}

// urlHostRe pulls the host out of a URL literal directly. url.Parse rejects
// a templated port ("http://127.0.0.1:${port}/x"), and a parse failure used
// to make an allowlisted host look like a leak.
var urlHostRe = regexp.MustCompile(`^https?://(?:[^/@]*@)?([^/:?#\s]+)`)

func urlAllowed(raw string, extra []string) bool {
	raw = strings.TrimRight(raw, ".,;:!?")
	var host string
	if m := urlHostRe.FindStringSubmatch(raw); m != nil {
		host = strings.ToLower(m[1])
	} else if u, err := url.Parse(raw); err == nil {
		host = strings.ToLower(u.Hostname())
	} else {
		return false
	}
	// A hostname cannot contain a backslash, so one here is escaping from a
	// pattern literal: a test asserting on a URL writes %r{https://api\.
	// example\.com/items} or /https:\/\/api\.example\.com/, and the escaped
	// host matched nothing in the allowlist. Unescape rather than skip, so a
	// host that is NOT allowlisted is still caught when written as a regex.
	// A backslash before a DOT is a regex escape and comes out; a
	// backslash before anything else ENDS the host, because it is a
	// string escape that ran on: "API_URL=https://api.example.com\n"
	// yielded api.example.com\n, and removing the backslash gave
	// api.example.comn -- not an allowlisted host, so a sample whose URL
	// pointed at example.com was refused at publish with no override. The
	// gate has to be wrong in the direction of asking, never of blocking.
	host = strings.ReplaceAll(host, `\.`, ".")
	if i := strings.IndexByte(host, '\\'); i >= 0 {
		host = host[:i]
	}
	// A hostname cannot contain '$' either, so one here starts an unbraced
	// interpolation that ran on past the host: Dart, shell, PHP and Ruby all
	// write "http://example.com$path". Cutting there recovers the real host.
	// If nothing is left the '$' began the host, as in "http://$host/x", and
	// that is genuinely not a known host — the check below still refuses it.
	if i := strings.IndexByte(host, '$'); i >= 0 {
		host = host[:i]
	}
	// A placeholder inside the host itself is not a known host.
	if templateExprRe.MatchString(host) {
		return false
	}
	if isReservedName(host) {
		return true
	}
	for _, a := range allowedURLHosts {
		if host == a || strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	for _, a := range extra {
		a = strings.ToLower(a)
		if host == a || strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	return false
}

func excerpt(m string) string {
	m = strings.TrimSpace(m)
	if len(m) > maxExcerptLen {
		return m[:maxExcerptLen] + "…"
	}
	return m
}
