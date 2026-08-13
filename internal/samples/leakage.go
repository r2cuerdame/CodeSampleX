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
	"crates.io",
	"proxy.golang.org",
	"example.com",
	"localhost",
	"127.0.0.1",
	"codesamplex.dev",
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
	// UNC shares.
	{KindAbsolutePath, regexp.MustCompile(`\\\\[A-Za-z0-9._-]+\\[^\s"']+`)},
	{KindEnvAssignment, regexp.MustCompile(`process\.env\.[A-Za-z_]\w*\s*=\s*["'` + "`" + `]`)},
	{KindEnvAssignment, regexp.MustCompile(`os\.environ\[\s*["'][^"']+["']\s*\]\s*=\s*["']`)},
}

var urlRe = regexp.MustCompile(`https?://[^\s"'<>()\[\]` + "`" + `]+`)

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
	for i, line := range strings.Split(content, "\n") {
		lineNo := i + 1
		for _, lp := range leakPatterns {
			for _, m := range lp.re.FindAllString(line, -1) {
				out = append(out, Finding{File: file, Line: lineNo, Kind: lp.kind, Excerpt: excerpt(m)})
			}
		}
		for _, m := range urlRe.FindAllString(line, -1) {
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

func urlAllowed(raw string, extra []string) bool {
	raw = strings.TrimRight(raw, ".,;:!?")
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
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
