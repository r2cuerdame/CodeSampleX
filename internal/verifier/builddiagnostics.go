package verifier

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/sanitizer"
)

var (
	reNodeWarning   = regexp.MustCompile(`^\(node:\d+\) (?:\[([A-Z]+\d+)\] )?([A-Za-z]*Warning): (.+)$`)
	reNpmDeprecated = regexp.MustCompile(`^npm (?:warn|WARN) deprecated (\S+?): (.+)$`)
	rePythonWarning = regexp.MustCompile(`^(.+?):(\d+): ((?:Pending)?DeprecationWarning|FutureWarning|RuntimeWarning|UserWarning|SyntaxWarning|ResourceWarning): (.+)$`)
	reJavacWarning  = regexp.MustCompile(`^(.+\.java):(\d+): warning: \[([a-z]+)\] (.+)$`)
	reJavacNote     = regexp.MustCompile(`^Note: (.+ uses or overrides a deprecated API\.)$`)
	reRustWarning   = regexp.MustCompile(`^warning: (.+)$`)
	reRustLocation  = regexp.MustCompile(`^\s*--> (.+?):(\d+):\d+$`)
	rePHPDeprecated = regexp.MustCompile(`^(?:PHP )?Deprecated:\s+(.+) in (.+) on line (\d+)$`)
	reRubyWarning   = regexp.MustCompile(`^(.+\.rb):(\d+): warning: (.+)$`)
)

func extractBuildDiagnostics(stage, log string, roots []string, env domain.EnvironmentFingerprint) []domain.BuildDiagnostic {
	if strings.TrimSpace(log) == "" {
		return nil
	}
	digest := domain.SHA256Hex([]byte(log))
	lines := strings.Split(strings.ReplaceAll(log, "\r\n", "\n"), "\n")
	var out []domain.BuildDiagnostic
	seen := map[string]bool{}
	add := func(tool, code string, kind domain.BuildDiagnosticKind, message, file string, line int) {
		if len(out) >= domain.MaxBuildDiagnostics {
			return
		}
		message = sanitizer.PublicDiagnosticMessage(message)
		if message == "" {
			return
		}
		file = sanitizer.PublicDiagnosticFile(relativeToRoots(file, roots))
		if file == "" {
			line = 0
		}
		if line < 0 {
			line = 0
		}
		key := tool + "\x00" + code + "\x00" + message + "\x00" + file + "\x00" + strconv.Itoa(line)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, domain.BuildDiagnostic{
			Tool: tool, ToolVersion: toolVersion(tool, env), Code: code,
			Severity: domain.BuildDiagnosticWarning, Kind: kind, Stage: stage,
			Message: message, File: file, Line: line, RawLogDigest: digest,
		})
	}
	for i, raw := range lines {
		text := strings.TrimRight(raw, " \t")
		if m := reNodeWarning.FindStringSubmatch(text); m != nil {
			kind := domain.BuildDiagnosticOther
			if m[2] == "DeprecationWarning" {
				kind = domain.BuildDiagnosticDeprecated
			}
			code := m[1]
			if code == "" {
				code = m[2]
			}
			add("node", code, kind, m[3], "", 0)
			continue
		}
		if m := reNpmDeprecated.FindStringSubmatch(text); m != nil {
			add("npm", "deprecated", domain.BuildDiagnosticDeprecated, m[1]+": "+m[2], "", 0)
			continue
		}
		if m := rePythonWarning.FindStringSubmatch(text); m != nil {
			kind := domain.BuildDiagnosticOther
			if strings.HasSuffix(m[3], "DeprecationWarning") || m[3] == "FutureWarning" {
				kind = domain.BuildDiagnosticDeprecated
			}
			add("python", m[3], kind, m[4], m[1], atoi(m[2]))
			continue
		}
		if m := reJavacWarning.FindStringSubmatch(text); m != nil {
			kind := domain.BuildDiagnosticOther
			switch m[3] {
			case "deprecation", "removal":
				kind = domain.BuildDiagnosticDeprecated
			case "unchecked":
				kind = domain.BuildDiagnosticUnsafe
			}
			add("javac", m[3], kind, m[4], m[1], atoi(m[2]))
			continue
		}
		if m := reJavacNote.FindStringSubmatch(text); m != nil {
			add("javac", "deprecation", domain.BuildDiagnosticDeprecated, m[1], "", 0)
			continue
		}
		if m := rePHPDeprecated.FindStringSubmatch(text); m != nil {
			add("php", "E_DEPRECATED", domain.BuildDiagnosticDeprecated, m[1], m[2], atoi(m[3]))
			continue
		}
		if m := reRubyWarning.FindStringSubmatch(text); m != nil {
			add("ruby", "warning", kindFromText(m[3]), m[3], m[1], atoi(m[2]))
			continue
		}
		if m := reRustWarning.FindStringSubmatch(text); m != nil && i+1 < len(lines) {
			if loc := reRustLocation.FindStringSubmatch(strings.TrimRight(lines[i+1], " \t\r")); loc != nil {
				code := ""
				if strings.HasPrefix(m[1], "use of deprecated") {
					code = "deprecated"
				}
				add("rustc", code, kindFromText(m[1]), m[1], loc[1], atoi(loc[2]))
			}
		}
	}
	return out
}

func kindFromText(s string) domain.BuildDiagnosticKind {
	lower := strings.ToLower(s)
	switch {
	case strings.Contains(lower, "deprecated"):
		return domain.BuildDiagnosticDeprecated
	case strings.Contains(lower, "obsolete"):
		return domain.BuildDiagnosticObsolete
	case strings.Contains(lower, "unsafe"):
		return domain.BuildDiagnosticUnsafe
	}
	return domain.BuildDiagnosticOther
}

func toolVersion(tool string, env domain.EnvironmentFingerprint) string {
	v := ""
	switch {
	case strings.EqualFold(env.Runtime, tool):
		v = env.RuntimeVersion
	case strings.EqualFold(env.Compiler, tool):
		v = env.CompilerVersion
	case strings.EqualFold(env.PackageManager, tool):
		v = env.PackageManagerVersion
	}
	if len(v) > 64 || strings.ContainsAny(v, " \t\r\n") {
		return ""
	}
	return v
}

func relativeToRoots(file string, roots []string) string {
	f := strings.ReplaceAll(strings.TrimSpace(file), "\\", "/")
	for _, root := range roots {
		r := strings.TrimRight(strings.ReplaceAll(root, "\\", "/"), "/")
		if r == "" {
			continue
		}
		if len(f) > len(r) && strings.EqualFold(f[:len(r)], r) && f[len(r)] == '/' {
			return f[len(r)+1:]
		}
	}
	return f
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
