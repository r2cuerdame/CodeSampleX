package samples

import (
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// ScanInputs is what the local extractor is allowed to hand the spec
// builder: public facts about the problem, never project source, paths,
// or names (goal.md §9.2).
type ScanInputs struct {
	Packages        []string // purls
	Symbols         []string
	Goal            string // intent
	Kind            string // HOW | FIX | MIGRATION | CONFIG
	Constraints     map[string]string
	Environment     domain.EnvironmentFingerprint
	PassedStages    []string // stages that succeeded in the origin project
	UserDescription string
}

// SanitizedSpec is the clean-room brief given to the user's local LLM.
// By construction it can only carry the goal.md §9.2 whitelist: public
// packages, public symbols, intent, constraints (incl. executionContext),
// required runtime conditions, passed stages, and the user's description.
// Nothing project-identifying is representable.
type SanitizedSpec struct {
	SchemaVersion     int               `json:"schemaVersion"`
	Goal              string            `json:"goal"`
	Kind              string            `json:"kind,omitempty"`
	Packages          []string          `json:"packages"`
	Symbols           []string          `json:"symbols,omitempty"`
	Constraints       map[string]string `json:"constraints,omitempty"`
	RuntimeConditions map[string]string `json:"runtimeConditions,omitempty"`
	PassedStages      []string          `json:"passedStages,omitempty"`
	UserDescription   string            `json:"userDescription,omitempty"`
}

// BuildSpec derives a SanitizedSpec from extractor results. Packages and
// symbols are deduplicated and sorted; runtime conditions come from the
// environment fingerprint (already free of paths and identifiers); the
// execution context becomes an explicit constraint.
func BuildSpec(res ScanInputs) SanitizedSpec {
	spec := SanitizedSpec{
		SchemaVersion:   1,
		Goal:            res.Goal,
		Kind:            res.Kind,
		Packages:        dedupeSorted(res.Packages),
		Symbols:         dedupeSorted(res.Symbols),
		PassedStages:    append([]string(nil), res.PassedStages...),
		UserDescription: res.UserDescription,
	}

	constraints := map[string]string{}
	for k, v := range res.Constraints {
		constraints[k] = v
	}
	env := res.Environment.Normalize()
	if env.ExecutionContext != "" {
		constraints["executionContext"] = env.ExecutionContext
	}
	if len(constraints) > 0 {
		spec.Constraints = constraints
	}

	rc := map[string]string{}
	if env.Ecosystem != "" {
		rc["ecosystem"] = env.Ecosystem
	}
	if env.Runtime != "" {
		rc["runtime"] = versioned(env.Runtime, env.RuntimeVersion)
	}
	if env.Language != "" {
		rc["language"] = versioned(env.Language, env.LanguageVersion)
	}
	if env.Compiler != "" {
		rc["compiler"] = versioned(env.Compiler, env.CompilerVersion)
	}
	if env.PackageManager != "" {
		rc["packageManager"] = versioned(env.PackageManager, env.PackageManagerVersion)
	}
	if env.ModuleSystem != "" {
		rc["moduleSystem"] = env.ModuleSystem
	}
	if len(rc) > 0 {
		spec.RuntimeConditions = rc
	}
	return spec
}

// PromptText renders the clean-room generation instructions handed to the
// user's local LLM. The model starts from an empty directory and works
// only from this spec.
func (s SanitizedSpec) PromptText() string {
	var b strings.Builder
	b.WriteString("Clean-room public code sample — generation instructions\n\n")
	b.WriteString("Write a brand-new, minimal, self-contained code sample in this clean-room directory.\n")
	b.WriteString("Do not copy, paraphrase, or reference any existing project source. Work only from this spec.\n\n")
	b.WriteString("A csx.json manifest scaffold already exists. Do not recreate it from memory. Preserve its case.goal, packages and symbols; fill its empty case.contract with exact assertions and correct its environment, commands and verifierAdapter for the files you generate.\n\n")

	b.WriteString("Goal: " + s.Goal + "\n")
	if s.Kind != "" {
		b.WriteString("Kind: " + s.Kind + "\n")
	}
	if s.UserDescription != "" {
		b.WriteString("Context from the user: " + s.UserDescription + "\n")
	}

	b.WriteString("\nUse EXACTLY these public packages and versions:\n")
	for _, p := range s.Packages {
		b.WriteString("  - " + p + "\n")
	}
	if len(s.Symbols) > 0 {
		b.WriteString("Demonstrate these symbols/APIs:\n")
		for _, sym := range s.Symbols {
			b.WriteString("  - " + sym + "\n")
		}
	}
	writeSortedKV(&b, "Constraints", s.Constraints)
	writeSortedKV(&b, "Required runtime conditions", s.RuntimeConditions)
	if len(s.PassedStages) > 0 {
		b.WriteString("Stages that passed in the origin environment: " + strings.Join(s.PassedStages, ", ") + "\n")
	}

	b.WriteString(`
Rules:
  - One focused purpose; the smallest project that proves the goal.
  - Include a contract test (test/contract.*) that runs OFFLINE and exits 0 exactly when the goal behavior works.
  - Pin every dependency with a lockfile so resolution is reproducible.
  - No secrets, credentials, or tokens. No real URLs (only example.com or localhost). No absolute paths.
  - No personal names, emails, company names, or project identifiers of any kind.
  - No binaries and no generated output (node_modules, dist, target, venv, .git, .env).
  - Keep it under 200 files and 256KB packed.
`)
	return b.String()
}

func writeSortedKV(b *strings.Builder, title string, m map[string]string) {
	if len(m) == 0 {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b.WriteString(title + ":\n")
	for _, k := range keys {
		b.WriteString("  - " + k + ": " + m[k] + "\n")
	}
}

func versioned(name, version string) string {
	if version == "" {
		return name
	}
	return name + "@" + version
}

func dedupeSorted(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
