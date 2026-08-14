package samples

import (
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
	{KindAbsolutePath, regexp.MustCompile(`(?:^|[^\w.@])(/(?:home|Users|usr|var|etc|tmp|opt|mnt|srv|root|private)/[A-Za-z0-9._/-]+)`)},
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
	{KindAbsolutePath, regexp.MustCompile(`(?:^|[\s"'=:(\[,])(\\{2}(?:\\{2})?[A-Za-z0-9._-]+\\{1,2}[^\s"']+)`)},
	// Only secret-shaped environment names: a sample legitimately sets TZ,
	// NODE_ENV or PORT, and flagging those blocked honest contributions.
	{KindEnvAssignment, regexp.MustCompile(`process\.env\.\w*(?i:KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|AUTH|DSN|CONN)\w*\s*=\s*["'` + "`" + `]`)},
	{KindEnvAssignment, regexp.MustCompile(`os\.environ\[\s*["']\w*(?i:KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|AUTH|DSN|CONN)\w*["']\s*\]\s*=\s*["']`)},
}

var urlRe = regexp.MustCompile(`https?://[^\s"'<>()\[\]` + "`" + `]+`)

// credentialURLRe matches a URL carrying inline credentials
// (https://user:token@host/…). Those are a real leak wherever they appear,
// lockfile included.
var credentialURLRe = regexp.MustCompile(`https?://[^/\s:@]+:[^/\s@]+@`)

// lockfileNames are machine-generated dependency manifests. They describe
// public packages, are written by the package manager rather than by the
// contributor, and are exactly what a sample must ship to be reproducible.
var lockfileNames = map[string]bool{
	"package-lock.json": true, "npm-shrinkwrap.json": true,
	"pnpm-lock.yaml": true, "yarn.lock": true,
	"cargo.lock": true, "poetry.lock": true, "uv.lock": true,
	"go.sum": true, "gemfile.lock": true, "composer.lock": true,
	"requirements.txt": true,
	"pubspec.lock":     true, "mix.lock": true, "bun.lock": true,
	"deno.lock": true,
}

func isLockfile(file string) bool {
	return lockfileNames[strings.ToLower(filepath.Base(filepath.FromSlash(file)))]
}

const (
	maxScanFileBytes = 2 << 20
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
			if p != dir && forbiddenDir(d.Name()) {
				return filepath.SkipDir // BuildArtifact rejects these outright
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil || info.Size() > maxScanFileBytes {
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
				if credentialURLRe.MatchString(m) {
					out = append(out, Finding{File: file, Line: lineNo, Kind: KindURL, Excerpt: excerpt(m)})
				}
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
