package fixclaims

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Repin is what makes "the same reproducer on both sides of the boundary"
// literal. A reproducer directory is written once, pinned at one release;
// each probe copies it and changes exactly two things: the release of the
// candidate's package, and -- when the probe names one -- the runtime
// version the manifest declares. Nothing else in the directory is
// touched, so a FAIL on one side and a PASS on the other differ only in
// the package release.
//
// It rewrites csx.json (every purl of the candidate's package in
// packages and case.packages) and the ecosystem's own declaration:
//
//	npm      package.json dependencies / devDependencies
//	pypi     requirements.txt  name==version
//	cargo    Cargo.toml  name = "..." or name = { version = "...", ... }
//	golang   go.mod  require lines
//
// Lockfiles are not rewritten here; Relock regenerates them with the
// ecosystem's own tool, because a hand-edited lock is a lie about what
// resolved.
func Repin(dir string, c Candidate, version string, env Environment) error {
	c = c.Normalized()
	version = domain.CanonicalVersion(c.Ecosystem, strings.TrimSpace(version))
	if version == "" {
		return errors.New("fixclaims: repin needs a version")
	}
	if err := repinManifest(dir, c, version, env); err != nil {
		return err
	}
	switch c.Ecosystem {
	case "npm":
		return repinPackageJSON(filepath.Join(dir, "package.json"), c.Name, version)
	case "pypi":
		return repinRequirements(filepath.Join(dir, "requirements.txt"), c.Name, version)
	case "cargo":
		return repinCargoToml(filepath.Join(dir, "Cargo.toml"), c.Name, version)
	case "golang":
		return repinGoMod(filepath.Join(dir, "go.mod"), c.Name, version)
	}
	return fmt.Errorf("fixclaims: repin does not know ecosystem %q", c.Ecosystem)
}

func repinManifest(dir string, c Candidate, version string, env Environment) error {
	path := filepath.Join(dir, "csx.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("fixclaims: repin: %w", err)
	}
	var m domain.SampleManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("fixclaims: repin: csx.json: %w", err)
	}
	hits := 0
	rewrite := func(list []string) []string {
		out := make([]string, 0, len(list))
		for _, raw := range list {
			p, err := domain.ParsePURL(raw)
			if err == nil && p.Ecosystem == c.Ecosystem && strings.EqualFold(p.Name, c.Name) {
				p.Version = version
				raw = p.String()
				hits++
			}
			out = append(out, raw)
		}
		return out
	}
	m.Packages = rewrite(m.Packages)
	m.Case.Packages = rewrite(m.Case.Packages)
	if hits == 0 {
		return fmt.Errorf("fixclaims: repin: csx.json does not name %s", c.Package().String())
	}
	if env.RuntimeVersion != "" {
		m.Environment.RuntimeVersion = env.RuntimeVersion
		if m.Environment.LanguageVersion != "" {
			m.Environment.LanguageVersion = env.RuntimeVersion
		}
	}
	// The case id is derived from the case and recomputed on create; it is
	// cleared here so a stale one never travels.
	m.Case.CaseID = ""
	out := append(domain.MustCanonicalJSON(m), '\n')
	return os.WriteFile(path, out, 0o644)
}

func repinPackageJSON(path, name, version string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("fixclaims: repin: %w", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("fixclaims: repin: package.json: %w", err)
	}
	hits := 0
	for _, section := range []string{"dependencies", "devDependencies", "optionalDependencies"} {
		rawDeps, ok := doc[section]
		if !ok {
			continue
		}
		var deps map[string]string
		if err := json.Unmarshal(rawDeps, &deps); err != nil {
			return fmt.Errorf("fixclaims: repin: package.json %s: %w", section, err)
		}
		if _, ok := deps[name]; !ok {
			continue
		}
		deps[name] = version
		hits++
		encoded, err := json.Marshal(deps)
		if err != nil {
			return err
		}
		doc[section] = encoded
	}
	if hits == 0 {
		return fmt.Errorf("fixclaims: repin: package.json does not depend on %s", name)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// pypiName normalizes a distribution name the way PEP 503 does, so
// "Pydantic_Core" and "pydantic-core" pin the same requirement line.
func pypiName(s string) string {
	return strings.ToLower(regexp.MustCompile(`[-_.]+`).ReplaceAllString(strings.TrimSpace(s), "-"))
}

var requirementLine = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)(\[[^\]]*\])?\s*==\s*\S+(.*)$`)

func repinRequirements(path, name, version string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("fixclaims: repin: %w", err)
	}
	lines := strings.Split(string(raw), "\n")
	hits := 0
	for i, line := range lines {
		m := requirementLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || pypiName(m[1]) != pypiName(name) {
			continue
		}
		lines[i] = m[1] + m[2] + "==" + version + m[3]
		hits++
	}
	if hits == 0 {
		return fmt.Errorf("fixclaims: repin: requirements.txt does not pin %s", name)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

func repinCargoToml(path, name, version string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("fixclaims: repin: %w", err)
	}
	// Both spellings of a dependency line, with the exact-version operator
	// so cargo cannot pick a neighbour: `tokio = "1.52.3"` and
	// `tokio = { version = "1.52.3", features = [...] }`, the latter possibly
	// spread over several lines.
	quoted := regexp.QuoteMeta(name)
	simple := regexp.MustCompile(`(?m)^(\s*` + quoted + `\s*=\s*)"[^"]*"`)
	table := regexp.MustCompile(`(?s)(^|\n)(\s*` + quoted + `\s*=\s*\{[^}]*?version\s*=\s*)"[^"]*"`)
	text := string(raw)
	hits := 0
	text = simple.ReplaceAllStringFunc(text, func(s string) string {
		hits++
		return simple.ReplaceAllString(s, `${1}"=`+version+`"`)
	})
	text = table.ReplaceAllStringFunc(text, func(s string) string {
		hits++
		return table.ReplaceAllString(s, `${1}${2}"=`+version+`"`)
	})
	if hits == 0 {
		return fmt.Errorf("fixclaims: repin: Cargo.toml does not depend on %s", name)
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

func repinGoMod(path, module, version string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("fixclaims: repin: %w", err)
	}
	re := regexp.MustCompile(`(?m)^(\s*(?:require\s+)?` + regexp.QuoteMeta(module) + `\s+)v?\S+`)
	hits := 0
	text := re.ReplaceAllStringFunc(string(raw), func(s string) string {
		hits++
		return re.ReplaceAllString(s, "${1}"+version)
	})
	if hits == 0 {
		return fmt.Errorf("fixclaims: repin: go.mod does not require %s", module)
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

// LockCommand is the ecosystem tool that regenerates the lockfile after a
// repin, run in the reproducer directory with the network. It is the one
// step of a probe that consults a registry, and it is the same step the
// verifier's resolve stage performs again offline from the result. pypi
// has no lockfile: requirements.txt is the pin.
func LockCommand(ecosystem string) []string {
	switch ecosystem {
	case "npm":
		return []string{"npm", "install", "--package-lock-only", "--ignore-scripts", "--no-audit", "--no-fund", "--loglevel=error"}
	case "cargo":
		return []string{"cargo", "generate-lockfile", "--quiet"}
	case "golang":
		return []string{"go", "mod", "tidy"}
	}
	return nil
}

// execLock runs one lock command; tests replace it.
var execLock = func(ctx context.Context, dir string, argv []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

// Relock regenerates the lockfile for a repinned reproducer. cargo cannot
// re-resolve on top of a lock that pins the old release, so the stale lock
// is removed first; npm and go rewrite theirs in place.
func Relock(ctx context.Context, dir, ecosystem string) error {
	argv := LockCommand(ecosystem)
	if argv == nil {
		return nil
	}
	if ecosystem == "cargo" {
		_ = os.Remove(filepath.Join(dir, "Cargo.lock"))
	}
	out, err := execLock(ctx, dir, argv)
	if err != nil {
		tail := strings.TrimSpace(string(out))
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		return fmt.Errorf("fixclaims: %s: %v: %s", strings.Join(argv, " "), err, tail)
	}
	return nil
}
