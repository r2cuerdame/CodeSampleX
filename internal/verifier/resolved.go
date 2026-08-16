package verifier

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// This file answers one question a receipt could not answer before: which
// version actually ran.
//
// A receipt said "this sample passed in this environment" and took the
// version from the manifest — a claim the verification never checked. The
// manifest is written by the sample's author, and §3.8 is that a wrong HIT
// is worse than a MISS, so a version nobody confirmed is exactly the shape
// of claim this project exists to refuse. It was also why there was
// nowhere to put version-axis data: the same contract run against three
// releases produced three receipts that all said the same thing.
//
// After resolve, the workspace holds the answer. Every ecosystem writes
// what it actually installed somewhere inside /work — a lockfile it
// generated, or the installed distribution metadata — and /work is the
// sample directory on this machine. Reading it costs nothing and turns a
// claim into evidence.
//
// Silence is honest here. A lockfile that cannot be read yields nothing
// rather than a guess, and an empty list means "not established", never
// "matches the manifest".

// resolvedPackages reports the purls the resolve stage actually produced,
// for the packages this sample declares.
func resolvedPackages(dir string, m domain.SampleManifest) []string {
	want := map[string]domain.PURL{}
	for _, raw := range m.Packages {
		if p, err := domain.ParsePURL(raw); err == nil {
			want[p.Ecosystem+"/"+p.Name] = p
		}
	}
	if len(want) == 0 {
		return nil
	}

	found := map[string]string{} // eco/name → version
	for key, p := range want {
		if v := resolvedVersion(dir, p); v != "" {
			found[key] = v
		}
	}
	out := make([]string, 0, len(found))
	for key, v := range found {
		p := want[key]
		p.Version = v
		out = append(out, p.String())
	}
	sort.Strings(out)
	return out
}

// resolvedVersion reads one package's installed version out of whatever
// the ecosystem's resolve step left behind.
func resolvedVersion(dir string, p domain.PURL) string {
	switch p.Ecosystem {
	case "npm":
		return npmResolved(dir, p.Name)
	case "cargo":
		return tomlLockResolved(dir, "Cargo.lock", p.Name)
	case "gem":
		return gemfileLockResolved(dir, p.Name)
	case "composer":
		return composerLockResolved(dir, p.Name)
	case "hex":
		return mixLockResolved(dir, p.Name)
	case "pub":
		return pubspecLockResolved(dir, p.Name)
	case "golang":
		return goModResolved(dir, p.Name)
	case "pypi":
		return distInfoResolved(dir, p.Name)
	}
	return ""
}

func readFile(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return string(b)
}

// npmResolved reads package-lock.json, preferring the top-level install of
// the package over a nested copy some dependency pulled in.
func npmResolved(dir, name string) string {
	body := readFile(dir, "package-lock.json")
	if body == "" {
		return ""
	}
	var lock struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	if json.Unmarshal([]byte(body), &lock) != nil {
		return ""
	}
	if e, ok := lock.Packages["node_modules/"+name]; ok {
		return e.Version
	}
	return ""
}

var reCargoLock = regexp.MustCompile(`(?m)^name = "%s"\n^version = "([^"]+)"`)

func tomlLockResolved(dir, file, name string) string {
	body := readFile(dir, file)
	if body == "" {
		return ""
	}
	re := regexp.MustCompile(`(?m)^name = "` + regexp.QuoteMeta(name) + `"\n^version = "([^"]+)"`)
	if m := re.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

func gemfileLockResolved(dir, name string) string {
	body := readFile(dir, "Gemfile.lock")
	if body == "" {
		return ""
	}
	// Bundler indents a resolved gem by four spaces under GEM/specs.
	re := regexp.MustCompile(`(?m)^\s{4}` + regexp.QuoteMeta(name) + ` \(([^)]+)\)`)
	if m := re.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

func composerLockResolved(dir, name string) string {
	body := readFile(dir, "composer.lock")
	if body == "" {
		return ""
	}
	var lock struct {
		Packages []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"packages"`
	}
	if json.Unmarshal([]byte(body), &lock) != nil {
		return ""
	}
	for _, e := range lock.Packages {
		if strings.EqualFold(e.Name, name) {
			// composer writes tags as "v1.2.3"; the purl carries the number.
			return strings.TrimPrefix(e.Version, "v")
		}
	}
	return ""
}

func mixLockResolved(dir, name string) string {
	body := readFile(dir, "mix.lock")
	if body == "" {
		return ""
	}
	re := regexp.MustCompile(`"` + regexp.QuoteMeta(name) + `":\s*\{:hex,\s*:[a-z0-9_]+,\s*"([^"]+)"`)
	if m := re.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

func pubspecLockResolved(dir, name string) string {
	body := readFile(dir, "pubspec.lock")
	if body == "" {
		return ""
	}
	re := regexp.MustCompile(`(?m)^\s{2}` + regexp.QuoteMeta(name) + `:\n(?:.*\n)*?\s+version:\s*"([^"]+)"`)
	if m := re.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

// goModResolved reads the require line. `go mod download` does not rewrite
// go.mod, so the file is the pin — but a replace directive overrides it,
// and the replace target is what compiled.
func goModResolved(dir, name string) string {
	body := readFile(dir, "go.mod")
	if body == "" {
		return ""
	}
	reRep := regexp.MustCompile(`(?m)^\s*replace\s+` + regexp.QuoteMeta(name) +
		`(?:\s+v[^\s]+)?\s*=>\s*[^\s]+\s+(v[^\s]+)`)
	if m := reRep.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	reReq := regexp.MustCompile(`(?m)^\s*(?:require\s+)?` + regexp.QuoteMeta(name) + `\s+(v[^\s]+)`)
	if m := reReq.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

// distInfoResolved reads what pip actually installed. Python has no
// lockfile by default, and requirements.txt is a request rather than a
// result — the .dist-info directory pip writes is the result.
func distInfoResolved(dir, name string) string {
	base := filepath.Join(dir, ".csx-vendor", "py")
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	// PyPI normalizes a name by lowercasing and folding -_. into one form.
	norm := func(s string) string {
		s = strings.ToLower(s)
		return strings.NewReplacer("-", "_", ".", "_").Replace(s)
	}
	target := norm(name)
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".dist-info") {
			continue
		}
		stem := strings.TrimSuffix(n, ".dist-info")
		i := strings.LastIndex(stem, "-")
		if i <= 0 {
			continue
		}
		if norm(stem[:i]) == target {
			return stem[i+1:]
		}
	}
	return ""
}
