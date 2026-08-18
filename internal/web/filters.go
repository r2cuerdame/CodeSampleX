package web

import (
	"strings"
	"unicode"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

const maxRecordQueryTerms = 32

// ParsedRecordQuery turns the record search box into a small batch query.
// Package names are ORed: "react axios lodash" finds all three instead of
// looking for that impossible literal phrase in one package name.
type ParsedRecordQuery struct {
	terms []string
}

func ParseRecordQuery(raw string) ParsedRecordQuery {
	fields := strings.FieldsFunc(strings.ToLower(raw), func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",;|，、", r)
	})
	seen := make(map[string]bool, min(len(fields), maxRecordQueryTerms))
	terms := make([]string, 0, min(len(fields), maxRecordQueryTerms))
	for _, field := range fields {
		field = strings.Trim(field, "\"'`[](){}")
		if field == "" || seen[field] {
			continue
		}
		seen[field] = true
		terms = append(terms, field)
		if len(terms) == maxRecordQueryTerms {
			break
		}
	}
	return ParsedRecordQuery{terms: terms}
}

// MatchPackage reports a union match and two ranking hints. Exact package
// names lead prefix matches, which lead other substring matches.
func (q ParsedRecordQuery) MatchPackage(name string) (match, exact, prefix bool) {
	if len(q.terms) == 0 {
		return true, false, false
	}
	name = strings.ToLower(name)
	for _, term := range q.terms {
		if name == term {
			match, exact, prefix = true, true, true
			continue
		}
		if strings.HasPrefix(name, term) {
			match, prefix = true, true
			continue
		}
		if strings.Contains(name, term) {
			match = true
		}
	}
	return match, exact, prefix
}

// filterOption is rendered by the native select controls. Selected is
// computed server-side so templates do not need clever string comparisons.
type filterOption struct {
	Value    string
	Label    string
	Selected bool
}

var ecosystemFilterValues = []string{"npm", "pypi", "cargo", "golang", "gem", "composer", "hex", "pub", "maven", "generic"}
var osFilterValues = []string{"linux", "windows", "darwin"}
var runtimeFilterValues = []string{"node", "python", "go", "rust", "ruby", "php", "elixir", "dart", "java", "browser", "bun", "deno"}
var basisFilterValues = []string{"observed", "verified"}

func cleanFilterValue(raw string, allowed []string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	for _, value := range allowed {
		if raw == value {
			return value
		}
	}
	return ""
}

func cleanRecordFilter(f RecordFilter) RecordFilter {
	f.Query = strings.TrimSpace(f.Query)
	f.Ecosystem = cleanFilterValue(f.Ecosystem, ecosystemFilterValues)
	f.OS = cleanFilterValue(f.OS, osFilterValues)
	f.Runtime = cleanFilterValue(f.Runtime, runtimeFilterValues)
	f.Basis = cleanFilterValue(f.Basis, basisFilterValues)
	return f
}

func filterOptions(values []string, selected string, label func(string) string) []filterOption {
	out := make([]filterOption, 0, len(values))
	for _, value := range values {
		out = append(out, filterOption{Value: value, Label: label(value), Selected: value == selected})
	}
	return out
}

func ecosystemOptions(selected string) []filterOption {
	return filterOptions(ecosystemFilterValues, selected, func(value string) string {
		if value == "generic" {
			return "CLI / SDK / OS"
		}
		return value
	})
}

func osOptions(selected string) []filterOption {
	return filterOptions(osFilterValues, selected, func(value string) string {
		if value == "darwin" {
			return "macOS"
		}
		return value
	})
}

func runtimeOptions(selected string) []filterOption {
	return filterOptions(runtimeFilterValues, selected, func(value string) string { return value })
}

func basisOptions(lang, selected string) []filterOption {
	return filterOptions(basisFilterValues, selected, func(value string) string {
		return i18n.T(lang, "filters."+value)
	})
}

func RecordEnvironmentOS(env domain.EnvironmentFingerprint) string {
	os := strings.ToLower(strings.TrimSpace(env.Normalize().OS))
	switch os {
	case "macos", "mac", "osx":
		return "darwin"
	}
	return os
}

var browserContexts = map[string]bool{
	"browser": true, "webview": true, "webworker": true,
	"serviceworker": true, "chrome": true, "chromium": true,
	"edge": true, "firefox": true, "safari": true,
	"android-webview": true, "ios-wkwebview": true,
}

// environmentRuntime returns a filter bucket from facts actually recorded in
// the fingerprint. It does not infer Python from PyPI, Ruby from RubyGems,
// and so on: ecosystem is not an execution environment.
func RecordEnvironmentRuntime(env domain.EnvironmentFingerprint) string {
	env = env.Normalize()
	if env.BrowserFamily != "" || browserContexts[strings.ToLower(env.ExecutionContext)] {
		return "browser"
	}
	if env.ExecutionContext != "" {
		return strings.ToLower(env.ExecutionContext)
	}
	if env.Runtime != "" {
		return strings.ToLower(env.Runtime)
	}
	if env.Language != "" {
		return strings.ToLower(env.Language)
	}
	return ""
}

// RecordEnvironmentMatches applies the /records environment buckets to one
// recorded fingerprint. It is exported for the production Store adapter;
// keeping the normalization here makes test and production matching agree.
func RecordEnvironmentMatches(env domain.EnvironmentFingerprint, osName, runtime string) bool {
	if osName != "" && RecordEnvironmentOS(env) != osName {
		return false
	}
	if runtime != "" && RecordEnvironmentRuntime(env) != runtime {
		return false
	}
	return true
}

func joinVersion(name, version string) string {
	name, version = strings.TrimSpace(name), strings.TrimSpace(version)
	if name == "" {
		return ""
	}
	if version == "" {
		return name
	}
	return name + " " + version
}

// environmentSummary is deliberately compact enough for a finding card.
// It only joins fields that exist in the recorded fingerprint.
func RecordEnvironmentSummary(env domain.EnvironmentFingerprint) string {
	env = env.Normalize().Bucketed()
	var parts []string
	if context := env.ContextLabel(); context != "" {
		parts = append(parts, context)
	} else if language := joinVersion(env.Language, env.LanguageVersion); language != "" {
		parts = append(parts, language)
	}
	if env.OS != "" {
		os := env.OS
		if env.OSVersionBucket != "" {
			os += " " + env.OSVersionBucket
		}
		if env.Arch != "" {
			os += "/" + env.Arch
		}
		parts = append(parts, os)
	}
	if env.Virtualization != "" {
		virtualization := env.Virtualization
		if env.ContainerRuntime != "" {
			virtualization = env.ContainerRuntime
		}
		parts = append(parts, virtualization)
	}
	return strings.Join(parts, " · ")
}

type environmentFact struct {
	Label string
	Value string
}

type environmentView struct {
	Summary string
	Facts   []environmentFact
	Present bool
}

func makeEnvironmentView(lang string, env domain.EnvironmentFingerprint) environmentView {
	env = env.Normalize().Bucketed()
	view := environmentView{Summary: RecordEnvironmentSummary(env)}
	add := func(key, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		view.Facts = append(view.Facts, environmentFact{Label: i18n.T(lang, key), Value: value})
		view.Present = true
	}
	add("env.context", env.ContextLabel())
	os := env.OS
	if env.OSVersionBucket != "" {
		os = strings.TrimSpace(os + " " + env.OSVersionBucket)
	}
	if env.Distro != "" {
		os = strings.TrimSpace(os + " · " + env.Distro)
	}
	if env.Libc != "" {
		os = strings.TrimSpace(os + " · " + joinVersion(env.Libc, env.LibcVersion))
	}
	add("env.os", os)
	add("env.arch", env.Arch)
	add("env.runtime", joinVersion(env.Runtime, env.RuntimeVersion))
	add("env.language", joinVersion(env.Language, env.LanguageVersion))
	add("env.package_manager", joinVersion(env.PackageManager, env.PackageManagerVersion))
	execution := env.Virtualization
	if env.ContainerRuntime != "" {
		execution = strings.TrimSpace(execution + " · " + env.ContainerRuntime)
	}
	if env.CI {
		execution = strings.TrimSpace(execution + " · CI")
	}
	add("env.execution", execution)
	return view
}
