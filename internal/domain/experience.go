package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// ExperienceProvenance distinguishes where an execution observation originated.
// Field evidence represents real-world executions by actual users, LLMs, or agents.
// Farm evidence represents controlled CSX verification, replay, or gap-filling.
// They must never be silently collapsed into an indistinguishable source.
type ExperienceProvenance string

const (
	ProvenanceField ExperienceProvenance = "field"
	ProvenanceFarm  ExperienceProvenance = "farm"
)

// ProvenanceLabel provides human-facing attribution without using "memory" terminology.
func (p ExperienceProvenance) ProvenanceLabel() string {
	switch p {
	case ProvenanceField:
		return "Observed field experience"
	case ProvenanceFarm:
		return "CSX Farm supporting verification"
	default:
		return string(p)
	}
}

// CLIExperienceCoordinate is the canonical, non-personalized identity of a CLI
// execution. The canonical coordinate is the execution itself, shared globally
// across users and agents without per-repository personalization boundaries.
type CLIExperienceCoordinate struct {
	Tool        string                 `json:"tool"`
	ToolVersion string                 `json:"toolVersion,omitempty"`
	Subcommand  string                 `json:"subcommand,omitempty"`
	ArgsPattern string                 `json:"argsPattern,omitempty"`
	Shell       string                 `json:"shell,omitempty"`
	Environment EnvironmentFingerprint `json:"environment"`
}

// Canonical returns the normalized representation of the CLI coordinate.
// Tool names and subcommands are lowercased and stripped of executable extensions.
func (c CLIExperienceCoordinate) Canonical() CLIExperienceCoordinate {
	tool := strings.ToLower(strings.TrimSpace(c.Tool))
	for _, ext := range []string{".exe", ".cmd", ".bat", ".ps1", ".sh"} {
		if strings.HasSuffix(tool, ext) {
			tool = strings.TrimSuffix(tool, ext)
			break
		}
	}
	subcommand := strings.ToLower(strings.Join(strings.Fields(c.Subcommand), " "))
	version := strings.TrimSpace(c.ToolVersion)
	args := canonicalizeArgsPattern(c.ArgsPattern)
	shell := strings.ToLower(strings.TrimSpace(c.Shell))

	env := c.Environment.Normalize()
	if env.SchemaVersion == 0 {
		env.SchemaVersion = 1
	}

	return CLIExperienceCoordinate{
		Tool:        tool,
		ToolVersion: version,
		Subcommand:  subcommand,
		ArgsPattern: args,
		Shell:       shell,
		Environment: env,
	}
}

// CLIStreamEvidence is the secret-safe local description of one captured
// stream. Excerpt is normalized and bounded; raw command output is never
// stored here or copied into the public upload aggregate.
type CLIStreamEvidence struct {
	Fingerprint string `json:"fingerprint,omitempty"`
	Excerpt     string `json:"excerpt,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
}

// CoordinateID derives the content-addressed identifier for this CLI coordinate.
func (c CLIExperienceCoordinate) CoordinateID() string {
	canon := c.Canonical()
	sum := sha256.Sum256(MustCanonicalJSON(canon))
	return "cliexp:sha256:" + hex.EncodeToString(sum[:])
}

// PURL converts the tool and version to a public generic CLI target if valid.
func (c CLIExperienceCoordinate) PURL() (PURL, bool) {
	tool := c.Canonical().Tool
	version := c.ToolVersion
	if tool == "" || version == "" || !ConcreteResolvedVersion(version) {
		return PURL{}, false
	}
	desc := tool + "@" + version
	return PublicTargetFromDescriptor(desc)
}

// DisplayCommand renders the command line coordinate concisely.
func (c CLIExperienceCoordinate) DisplayCommand() string {
	var parts []string
	if c.Tool != "" {
		parts = append(parts, c.Tool)
	}
	if c.Subcommand != "" {
		parts = append(parts, c.Subcommand)
	}
	if c.ArgsPattern != "" {
		parts = append(parts, c.ArgsPattern)
	}
	return strings.Join(parts, " ")
}

// CLIExperienceObservation records one execution event or a compressed cluster
// of identical runs at this coordinate. Forensic integrity requires that PASS
// and FAIL observations coexist: a single verified failure is not erased by
// multiple passing runs.
type CLIExperienceObservation struct {
	ID                 string                  `json:"id,omitempty"`
	Coordinate         CLIExperienceCoordinate `json:"coordinate"`
	Provenance         ExperienceProvenance    `json:"provenance"`
	Result             Result                  `json:"result"` // PASS | FAIL
	Stage              Stage                   `json:"stage,omitempty"`
	Termination        FailureTermination      `json:"termination,omitempty"`
	ErrorFingerprint   string                  `json:"errorFingerprint,omitempty"`
	ErrorCode          string                  `json:"errorCode,omitempty"`
	ErrorSummary       string                  `json:"errorSummary,omitempty"`
	EvidenceQuality    EvidenceQuality         `json:"evidenceQuality,omitempty"`
	OuterStage         Stage                   `json:"outerStage,omitempty"`
	ActualToolchain    string                  `json:"actualToolchain,omitempty"`
	StageEvidence      FailureStageEvidence    `json:"stageEvidence,omitempty"`
	FailureEvidenceGap FailureEvidenceGap      `json:"failureEvidenceGap,omitempty"`
	ObservedAt         string                  `json:"observedAt,omitempty"` // RFC3339
	StartedAt          string                  `json:"startedAt,omitempty"`  // RFC3339
	FinishedAt         string                  `json:"finishedAt,omitempty"` // RFC3339
	EnvironmentID      string                  `json:"environmentId,omitempty"`
	Stdout             CLIStreamEvidence       `json:"stdout,omitempty"`
	Stderr             CLIStreamEvidence       `json:"stderr,omitempty"`
	Count              int64                   `json:"count"`
	IsHighInformation  bool                    `json:"isHighInformation"`
}

// ComputeID derives the content-addressed observation ID.
func (o CLIExperienceObservation) ComputeID() string {
	type idPayload struct {
		CoordID     string               `json:"coordId"`
		Provenance  ExperienceProvenance `json:"provenance"`
		Result      Result               `json:"result"`
		Termination FailureTermination   `json:"termination,omitempty"`
		ErrorFP     string               `json:"errorFp,omitempty"`
		ErrorCode   string               `json:"errorCode,omitempty"`
		ObservedAt  string               `json:"observedAt,omitempty"`
	}
	payload := idPayload{
		CoordID:     o.Coordinate.CoordinateID(),
		Provenance:  o.Provenance,
		Result:      o.Result,
		Termination: o.Termination.Canonical(),
		ErrorFP:     o.ErrorFingerprint,
		ErrorCode:   o.ErrorCode,
		ObservedAt:  o.ObservedAt,
	}
	sum := sha256.Sum256(MustCanonicalJSON(payload))
	return "cliobs:sha256:" + hex.EncodeToString(sum[:])
}

// EvidenceID identifies a comparable execution outcome independently of when
// it was observed. Repeated identical executions therefore accumulate while a
// different termination or stdout/stderr signature remains a separate row.
func (o CLIExperienceObservation) EvidenceID() string {
	type evidencePayload struct {
		CoordinateID string               `json:"coordinateId"`
		Provenance   ExperienceProvenance `json:"provenance"`
		Result       Result               `json:"result"`
		Termination  FailureTermination   `json:"termination,omitempty"`
		ErrorFP      string               `json:"errorFingerprint,omitempty"`
		StdoutFP     string               `json:"stdoutFingerprint,omitempty"`
		StderrFP     string               `json:"stderrFingerprint,omitempty"`
		StdoutCut    bool                 `json:"stdoutTruncated,omitempty"`
		StderrCut    bool                 `json:"stderrTruncated,omitempty"`
		Quality      EvidenceQuality      `json:"evidenceQuality,omitempty"`
	}
	payload := evidencePayload{
		CoordinateID: o.Coordinate.CoordinateID(),
		Provenance:   o.Provenance,
		Result:       o.Result,
		Termination:  o.Termination.Canonical(),
		ErrorFP:      o.ErrorFingerprint,
		StdoutFP:     o.Stdout.Fingerprint,
		StderrFP:     o.Stderr.Fingerprint,
		StdoutCut:    o.Stdout.Truncated,
		StderrCut:    o.Stderr.Truncated,
		Quality:      o.EvidenceQuality,
	}
	sum := sha256.Sum256(MustCanonicalJSON(payload))
	return "clievidence:sha256:" + hex.EncodeToString(sum[:])
}

// ExperienceBoundary documents a transition where command behavior changed across
// an environmental, version, or option boundary.
type ExperienceBoundary struct {
	Axis           string `json:"axis"` // "version" | "os" | "runtime"
	TransitionFrom string `json:"transitionFrom"`
	TransitionTo   string `json:"transitionTo"`
	FromVerdict    Result `json:"fromVerdict"`
	ToVerdict      Result `json:"toVerdict"`
	Explanation    string `json:"explanation"`
}

// CLIExperienceSummary is the compact, agent-facing recall representation.
// It is designed to be lightweight enough for frequent recall without dumping
// raw histories or logs. User-facing language strictly uses "experience".
type CLIExperienceSummary struct {
	Coordinate      CLIExperienceCoordinate    `json:"coordinate"`
	Status          string                     `json:"status"` // "OBSERVED_PASS" | "OBSERVED_FAIL" | "COEXISTING_BOUNDARY" | "UNOBSERVED"
	RecentFailures  []CLIExperienceObservation `json:"recentFailures,omitempty"`
	RecentSuccesses []CLIExperienceObservation `json:"recentSuccesses,omitempty"`
	Boundaries      []ExperienceBoundary       `json:"boundaries,omitempty"`
	FieldPassCount  int64                      `json:"fieldPassCount"`
	FieldFailCount  int64                      `json:"fieldFailCount"`
	FarmPassCount   int64                      `json:"farmPassCount"`
	FarmFailCount   int64                      `json:"farmFailCount"`
	Quality         string                     `json:"quality"` // "HIGH" | "MEDIUM" | "LOW" | "UNOBSERVED"
}

// TextSummary formats the summary for agent reading in markdown format.
// Strictly uses "experience", never "memory".
func (s CLIExperienceSummary) TextSummary() string {
	var b strings.Builder
	cmd := s.Coordinate.DisplayCommand()
	if cmd == "" {
		cmd = "unknown CLI command"
	}

	b.WriteString(fmt.Sprintf("CLI EXECUTION EXPERIENCE: %s\n", cmd))
	if s.Coordinate.ToolVersion != "" {
		b.WriteString(fmt.Sprintf("Version: %s\n", s.Coordinate.ToolVersion))
	}
	if s.Coordinate.Environment.OS != "" {
		envDesc := s.Coordinate.Environment.OS
		if s.Coordinate.Environment.Arch != "" {
			envDesc += "/" + s.Coordinate.Environment.Arch
		}
		b.WriteString(fmt.Sprintf("Environment: %s\n", envDesc))
	}

	b.WriteString(fmt.Sprintf("Status: %s (Quality: %s)\n", s.Status, s.Quality))
	b.WriteString(fmt.Sprintf("Evidence Ledger: Field (%d PASS, %d FAIL) | Farm (%d PASS, %d FAIL)\n",
		s.FieldPassCount, s.FieldFailCount, s.FarmPassCount, s.FarmFailCount))

	if len(s.RecentFailures) > 0 {
		b.WriteString("\nVerified Failure Observations:\n")
		for _, f := range s.RecentFailures {
			prov := string(f.Provenance)
			desc := f.Termination.FingerprintCoordinate()
			if f.ErrorCode != "" {
				desc += " [" + f.ErrorCode + "]"
			}
			if f.ErrorSummary != "" {
				desc += " — " + f.ErrorSummary
			}
			b.WriteString(fmt.Sprintf("- [%s] %s\n", prov, desc))
		}
	}

	if len(s.RecentSuccesses) > 0 {
		b.WriteString("\nVerified Success Observations:\n")
		for _, succ := range s.RecentSuccesses {
			prov := string(succ.Provenance)
			timeNote := ""
			if succ.ObservedAt != "" {
				timeNote = " (observed " + dateOnly(succ.ObservedAt) + ")"
			}
			b.WriteString(fmt.Sprintf("- [%s] exit:0%s (x%d runs)\n", prov, timeNote, succ.Count))
		}
	}

	if len(s.Boundaries) > 0 {
		b.WriteString("\nBehavioral Boundaries:\n")
		for _, bound := range s.Boundaries {
			b.WriteString(fmt.Sprintf("- %s axis: %s (%s) -> %s (%s) — %s\n",
				bound.Axis, bound.TransitionFrom, bound.FromVerdict,
				bound.TransitionTo, bound.ToVerdict, bound.Explanation))
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

func dateOnly(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}

// ParseCLICommand parses command line argv and environment into a structured
// CLIExperienceCoordinate with sanitized argument patterns.
func ParseCLICommand(argv []string, env EnvironmentFingerprint) CLIExperienceCoordinate {
	if len(argv) == 0 {
		return CLIExperienceCoordinate{Environment: env.Normalize()}
	}

	tool := CommandTool(argv)
	rawArgs := argv[1:]

	subcommand, argsPattern := extractSubcommandAndFlags(tool, rawArgs)

	coord := CLIExperienceCoordinate{
		Tool:        tool,
		Subcommand:  subcommand,
		ArgsPattern: argsPattern,
		Environment: env,
	}
	return coord.Canonical()
}

// multiWordSubcommands defines recognized subcommands for popular developer CLIs.
var multiWordSubcommands = map[string][]string{
	"gh": {
		"workflow run", "workflow view", "workflow list", "workflow enable", "workflow disable",
		"pr view", "pr list", "pr create", "pr checkout", "pr merge", "pr diff",
		"issue view", "issue list", "issue create", "issue close",
		"repo clone", "repo create", "repo view",
		"run view", "run watch", "run rerun", "run list",
	},
	"docker": {
		"compose up", "compose down", "compose build", "compose restart", "compose ps", "compose logs",
		"container run", "container start", "container stop", "container rm", "container ls",
		"image build", "image pull", "image push", "image ls", "image rm",
		"volume create", "volume ls", "volume rm",
		"network create", "network ls", "network rm",
	},
	"git": {
		"worktree add", "worktree list", "worktree remove", "worktree prune",
		"submodule update", "submodule add", "submodule status",
		"remote add", "remote remove", "remote set-url",
	},
	"npm": {
		"run build", "run test", "run lint", "run start", "run dev",
	},
	"pnpm": {
		"run build", "run test", "run lint", "run start", "run dev",
	},
}

// IsRecognizedCLITool reports whether name is a recognized command-line tool.
func IsRecognizedCLITool(name string) bool {
	tool := CommandTool([]string{name})
	if tool == "" {
		return false
	}
	if coord, ok := wantedTargetNames[tool]; ok && strings.HasPrefix(coord, "cli/") {
		return true
	}
	if _, ok := buildToolEcosystems[tool]; ok {
		return true
	}
	if _, ok := multiWordSubcommands[tool]; ok {
		return true
	}
	switch tool {
	case "docker", "docker-compose", "gh", "git", "ssh", "scp", "kubectl", "helm", "terraform", "opentofu",
		"ffmpeg", "ripgrep", "bash", "busybox", "coreutils", "powershell", "windows-powershell", "cmd", "pwsh",
		"npm", "pnpm", "yarn", "bun", "deno", "maven", "mvn", "mvnw", "gradle", "gradlew", "pip", "pip3", "uv",
		"cargo", "gem", "bundle", "bundler", "composer", "mix", "dart", "flutter", "curl", "jq",
		"openssl", "tar", "grep", "sed", "findutils", "go", "python", "python3", "pytest", "node",
		"tsc", "npx", "rustc", "dotnet", "java", "javac", "php", "ruby", "elixir":
		return true
	}
	return false
}

func extractSubcommandAndFlags(tool string, args []string) (subcommand, argsPattern string) {
	if len(args) == 0 {
		return "", ""
	}

	// Check multi-word subcommands first
	if multi, ok := multiWordSubcommands[tool]; ok {
		joined := strings.Join(args, " ")
		for _, pattern := range multi {
			if strings.HasPrefix(joined, pattern) {
				subcommand = pattern
				remaining := strings.TrimSpace(strings.TrimPrefix(joined, pattern))
				argsPattern = sanitizeAndNormalizeArgs(strings.Fields(remaining))
				return subcommand, argsPattern
			}
		}
	}

	// Only tools with a command vocabulary get a subcommand. Treating the
	// first positional of ssh/scp/curl/bash as a subcommand persisted hosts,
	// repository names, and scripts as if they were public command structure.
	if toolHasSubcommands(tool) && !strings.HasPrefix(args[0], "-") && !strings.Contains(args[0], "=") {
		subcommand = strings.ToLower(args[0])
		argsPattern = sanitizeAndNormalizeArgs(args[1:])
		return subcommand, argsPattern
	}

	// Only flags
	argsPattern = sanitizeAndNormalizeArgs(args)
	return "", argsPattern
}

func toolHasSubcommands(tool string) bool {
	switch tool {
	case "gh", "git", "docker", "docker-compose", "kubectl", "helm", "terraform", "opentofu",
		"npm", "pnpm", "yarn", "bun", "deno", "maven", "mvn", "mvnw", "gradle", "gradlew",
		"pip", "pip3", "uv", "cargo", "gem", "bundle", "bundler", "composer", "mix", "dart",
		"flutter", "go", "dotnet", "openssl":
		return true
	default:
		return false
	}
}

func isSensitiveName(name string) bool {
	clean := strings.ToLower(strings.TrimLeft(name, "-_"))
	clean = strings.ReplaceAll(clean, "-", "")
	clean = strings.ReplaceAll(clean, "_", "")
	if clean == "key" || strings.HasSuffix(clean, "key") {
		return true
	}
	for _, s := range []string{
		"password", "passwd", "pass", "secret", "token", "apikey",
		"credential", "auth", "privkey", "privatekey", "bearer",
	} {
		if strings.Contains(clean, s) {
			return true
		}
	}
	return false
}

func sanitizeAndNormalizeArgs(tokens []string) string {
	var normalized []string
	skipNext := false

	for i, tok := range tokens {
		if skipNext {
			skipNext = false
			continue
		}

		if strings.HasPrefix(tok, "-") {
			// Redact sensitive flag parameters
			eqIdx := strings.Index(tok, "=")
			if eqIdx > 0 {
				flagName := tok[:eqIdx]
				flagVal := tok[eqIdx+1:]
				if isSensitiveName(flagName) {
					normalized = append(normalized, flagName+"=<redacted-secret>")
				} else {
					normalized = append(normalized, flagName+"="+sanitizeArgValue(flagVal))
				}
			} else {
				normalized = append(normalized, tok)
				// Check if this flag expects a parameter
				if i+1 < len(tokens) && !strings.HasPrefix(tokens[i+1], "-") {
					if isSensitiveName(tok) {
						normalized = append(normalized, "<redacted-secret>")
						skipNext = true
					} else if isValueConsumingFlag(tok) {
						normalized = append(normalized, sanitizeArgValue(tokens[i+1]))
						skipNext = true
					}
				}
			}
		} else {
			normalized = append(normalized, sanitizeArgValue(tok))
		}
	}

	return strings.Join(normalized, " ")
}

func isValueConsumingFlag(flag string) bool {
	switch flag {
	case "-f", "--file", "-o", "--output", "-t", "--tag", "-b", "--branch",
		"-m", "--message", "--timeout", "-p", "--port", "-e", "--env":
		return true
	default:
		return false
	}
}

func sanitizeArgValue(val string) string {
	// Key-value pair like -e TOKEN=ghp_... or TOKEN=plainvalue or FOO=bar
	if eqIdx := strings.Index(val, "="); eqIdx > 0 {
		k := val[:eqIdx]
		if isSensitiveName(k) {
			return k + "=<redacted-secret>"
		}
		return "<assignment>"
	}

	// Secret/token pattern
	lower := strings.ToLower(val)
	if strings.Contains(lower, "bearer") || strings.Contains(lower, "ghp_") ||
		strings.Contains(lower, "secret") || strings.Contains(lower, "token") {
		return "<redacted-secret>"
	}
	// URL pattern
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return "<url>"
	}
	// Git branch prefix like feature/..., bugfix/..., fix/...
	if strings.HasPrefix(lower, "feature/") || strings.HasPrefix(lower, "bugfix/") ||
		strings.HasPrefix(lower, "fix/") || strings.HasPrefix(lower, "hotfix/") ||
		strings.HasPrefix(lower, "release/") {
		return "<branch>"
	}
	// File path pattern or known file extension
	if strings.ContainsAny(val, `/\`) || strings.HasPrefix(val, ".") ||
		strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml") ||
		strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".toml") ||
		strings.HasSuffix(lower, ".go") || strings.HasSuffix(lower, ".js") ||
		strings.HasSuffix(lower, ".ts") {
		return "<path>"
	}
	// Hex hash pattern (e.g. git commit hash)
	if len(val) >= 20 && isHex(val) {
		return "<hash>"
	}
	return "<arg>"
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func canonicalizeArgsPattern(args string) string {
	tokens := strings.Fields(args)
	if len(tokens) == 0 {
		return ""
	}
	// Normalize spacing
	return strings.Join(tokens, " ")
}

// CompressExperienceObservations merges repetitive, low-information observations
// into compact counts while preserving forensic integrity:
// 1. Never merges field observations with Farm verifications.
// 2. Never merges distinct failure fingerprints or exit conditions.
// 3. Compresses identical PASS runs into a single observation with an aggregated Count.
// 4. Preserves rare, high-information failures even if surrounded by huge PASS volume.
func CompressExperienceObservations(observations []CLIExperienceObservation) []CLIExperienceObservation {
	type groupKey struct {
		CoordID    string
		Provenance ExperienceProvenance
		Result     Result
		TermKind   TerminationKind
		ExitCode   int
		ErrorFP    string
		ErrorCode  string
	}

	groups := map[groupKey]*CLIExperienceObservation{}
	var order []groupKey

	for _, o := range observations {
		exitCode := 0
		if o.Termination.ExitCode != nil {
			exitCode = *o.Termination.ExitCode
		}

		key := groupKey{
			CoordID:    o.Coordinate.CoordinateID(),
			Provenance: o.Provenance,
			Result:     o.Result,
			TermKind:   o.Termination.Kind,
			ExitCode:   exitCode,
			ErrorFP:    o.ErrorFingerprint,
			ErrorCode:  o.ErrorCode,
		}

		count := o.Count
		if count <= 0 {
			count = 1
		}

		existing, exists := groups[key]
		if !exists {
			copyObs := o
			copyObs.Count = count
			// Single failure is high information
			if copyObs.Result == ResultFail {
				copyObs.IsHighInformation = true
			}
			groups[key] = &copyObs
			order = append(order, key)
		} else {
			existing.Count += count
			// Update to latest seen timestamp if newer
			if o.ObservedAt > existing.ObservedAt {
				existing.ObservedAt = o.ObservedAt
			}
			if o.IsHighInformation {
				existing.IsHighInformation = true
			}
		}
	}

	out := make([]CLIExperienceObservation, 0, len(order))
	for _, key := range order {
		item := *groups[key]
		if item.ID == "" {
			item.ID = item.ComputeID()
		}
		out = append(out, item)
	}
	return out
}

// EncodeCLISymbol encodes the CLI subcommand, arguments pattern, and provenance into the symbol column.
// Format: "<provenance>:<subcommand>[ <argsPattern>]"
func EncodeCLISymbol(subcommand, argsPattern string, prov ExperienceProvenance) string {
	if prov == "" {
		prov = ProvenanceField
	}
	var b strings.Builder
	b.WriteString(string(prov))
	b.WriteString(":")
	subcommand = strings.TrimSpace(subcommand)
	argsPattern = strings.TrimSpace(argsPattern)
	if subcommand != "" {
		b.WriteString(subcommand)
		if argsPattern != "" {
			b.WriteString(" ")
			b.WriteString(argsPattern)
		}
	} else if argsPattern != "" {
		b.WriteString(argsPattern)
	}
	return b.String()
}

// DecodeCLISymbol parses a symbol recorded for a CLI experience observation back into
// subcommand, argsPattern, and provenance.
func DecodeCLISymbol(symbol string, tool string, env EnvironmentFingerprint) (subcommand, argsPattern string, prov ExperienceProvenance) {
	prov = ProvenanceField
	raw := strings.TrimSpace(symbol)
	if strings.HasPrefix(raw, "farm:") {
		prov = ProvenanceFarm
		raw = strings.TrimPrefix(raw, "farm:")
	} else if strings.HasPrefix(raw, "field:") {
		prov = ProvenanceField
		raw = strings.TrimPrefix(raw, "field:")
	} else if strings.HasPrefix(raw, "[farm]") {
		prov = ProvenanceFarm
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "[farm]"))
	} else if strings.HasPrefix(raw, "[field]") {
		prov = ProvenanceField
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "[field]"))
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", prov
	}
	fields := strings.Fields(raw)
	argv := append([]string{tool}, fields...)
	parsed := ParseCLICommand(argv, env)
	return parsed.Subcommand, parsed.ArgsPattern, prov
}

// MatchesExactCoordinate reports whether candidate observation matches the target coordinate's
// tool, subcommand and argument pattern.
func MatchesExactCoordinate(target, candidate CLIExperienceCoordinate) bool {
	t := target.Canonical()
	c := candidate.Canonical()
	if t.Tool != "" && c.Tool != t.Tool {
		return false
	}
	if t.Subcommand != c.Subcommand {
		return false
	}
	if t.ArgsPattern != c.ArgsPattern {
		return false
	}
	return true
}

// DetectExperienceBoundaries detects behavioral divergence across version or OS
// dimensions where a command transitions between PASS and FAIL.
func DetectExperienceBoundaries(observations []CLIExperienceObservation) []ExperienceBoundary {
	var boundaries []ExperienceBoundary

	// Group by Tool + Subcommand + ArgsPattern + OS to find version boundaries
	type versionGroupKey struct {
		Tool        string
		Subcommand  string
		ArgsPattern string
		OS          string
	}
	versionBuckets := map[versionGroupKey][]CLIExperienceObservation{}

	for _, o := range observations {
		if o.Coordinate.ToolVersion != "" {
			k := versionGroupKey{
				Tool:        o.Coordinate.Tool,
				Subcommand:  o.Coordinate.Subcommand,
				ArgsPattern: o.Coordinate.ArgsPattern,
				OS:          o.Coordinate.Environment.OS,
			}
			versionBuckets[k] = append(versionBuckets[k], o)
		}
	}

	for _, bucket := range versionBuckets {
		if len(bucket) < 2 {
			continue
		}

		// 1. Group observations by exact ToolVersion
		type versionOutcome struct {
			version     string
			hasPass     bool
			hasFail     bool
			failSummary string
		}
		byVersion := map[string]*versionOutcome{}
		for _, o := range bucket {
			ver := o.Coordinate.ToolVersion
			vo, exists := byVersion[ver]
			if !exists {
				vo = &versionOutcome{version: ver}
				byVersion[ver] = vo
			}
			if o.Result == ResultPass {
				vo.hasPass = true
			} else if o.Result == ResultFail {
				vo.hasFail = true
				if vo.failSummary == "" && o.ErrorSummary != "" {
					vo.failSummary = o.ErrorSummary
				}
			}
		}

		if len(byVersion) < 2 {
			continue
		}

		// 2. Sort distinct versions in ascending version order
		distinctVersions := make([]*versionOutcome, 0, len(byVersion))
		for _, vo := range byVersion {
			distinctVersions = append(distinctVersions, vo)
		}
		sort.Slice(distinctVersions, func(i, j int) bool {
			return CompareVersions(distinctVersions[i].version, distinctVersions[j].version) < 0
		})

		// 3. Compare adjacent distinct versions
		for i := 0; i < len(distinctVersions)-1; i++ {
			curr := distinctVersions[i]
			next := distinctVersions[i+1]

			currVerdict := ""
			if curr.hasPass && !curr.hasFail {
				currVerdict = string(ResultPass)
			} else if curr.hasFail && !curr.hasPass {
				currVerdict = string(ResultFail)
			}

			nextVerdict := ""
			if next.hasPass && !next.hasFail {
				nextVerdict = string(ResultPass)
			} else if next.hasFail && !next.hasPass {
				nextVerdict = string(ResultFail)
			}

			if currVerdict != "" && nextVerdict != "" && currVerdict != nextVerdict {
				expl := fmt.Sprintf("%s was %s at %s, changed to %s at %s",
					bucket[0].Coordinate.Tool, currVerdict, curr.version,
					nextVerdict, next.version)
				if nextVerdict == string(ResultFail) && next.failSummary != "" {
					expl += fmt.Sprintf(" (%s)", next.failSummary)
				}
				boundaries = append(boundaries, ExperienceBoundary{
					Axis:           "version",
					TransitionFrom: curr.version,
					TransitionTo:   next.version,
					FromVerdict:    Result(currVerdict),
					ToVerdict:      Result(nextVerdict),
					Explanation:    expl,
				})
			}
		}
	}

	return boundaries
}

// RankExperienceObservations ranks observations relative to a target query coordinate.
// Coordinates matching tool, subcommand and OS exactly rank highest.
// Newer observations rank higher as a temporal ranking signal, while old observations
// are fully preserved without decay penalties.
func RankExperienceObservations(target CLIExperienceCoordinate, observations []CLIExperienceObservation) []CLIExperienceObservation {
	type scoredObs struct {
		obs   CLIExperienceObservation
		score int
	}

	scored := make([]scoredObs, len(observations))
	tCanon := target.Canonical()

	for i, o := range observations {
		score := 0
		oCanon := o.Coordinate.Canonical()

		if oCanon.Tool == tCanon.Tool && oCanon.Tool != "" {
			score += 1000
		}
		if oCanon.Subcommand == tCanon.Subcommand && oCanon.Subcommand != "" {
			score += 500
		}
		if oCanon.ToolVersion != "" && tCanon.ToolVersion != "" {
			if oCanon.ToolVersion == tCanon.ToolVersion {
				score += 300
			} else if CompareVersions(oCanon.ToolVersion, tCanon.ToolVersion) == 0 {
				score += 250
			}
		}
		if oCanon.Environment.OS == tCanon.Environment.OS && oCanon.Environment.OS != "" {
			score += 200
		}
		if oCanon.Environment.Arch == tCanon.Environment.Arch && oCanon.Environment.Arch != "" {
			score += 100
		}
		if oCanon.ArgsPattern == tCanon.ArgsPattern && oCanon.ArgsPattern != "" {
			score += 150
		}
		if o.IsHighInformation {
			score += 50
		}
		scored[i] = scoredObs{obs: o, score: score}
	}

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		// Recency ranking signal: newer timestamps sort ahead
		if scored[i].obs.ObservedAt != scored[j].obs.ObservedAt {
			return scored[i].obs.ObservedAt > scored[j].obs.ObservedAt
		}
		return scored[i].obs.Count > scored[j].obs.Count
	})

	out := make([]CLIExperienceObservation, len(scored))
	for i := range scored {
		out[i] = scored[i].obs
	}
	return out
}

// BuildExperienceSummary constructs the compact agent recall payload from a corpus of observations.
// In accordance with the lightweight agent contract:
// - Returns only 1-2 most relevant verified failures
// - Returns only 1-2 most relevant observed successes
// - Preserves explicit separation of field vs farm observations
// - Surfaces detected version/environment boundaries
func BuildExperienceSummary(target CLIExperienceCoordinate, observations []CLIExperienceObservation) CLIExperienceSummary {
	canon := target.Canonical()

	// 1. Detect boundaries on command-coordinate matches (tool, subcommand, args) across versions
	var boundaryCandidates []CLIExperienceObservation
	for _, o := range observations {
		if o.Coordinate.Tool == canon.Tool &&
			o.Coordinate.Subcommand == canon.Subcommand &&
			o.Coordinate.ArgsPattern == canon.ArgsPattern {
			boundaryCandidates = append(boundaryCandidates, o)
		}
	}
	compressedForBoundaries := CompressExperienceObservations(boundaryCandidates)
	boundaries := DetectExperienceBoundaries(compressedForBoundaries)

	summary := CLIExperienceSummary{
		Coordinate: canon,
		Status:     "UNOBSERVED",
		Quality:    "UNOBSERVED",
		Boundaries: boundaries,
	}

	// 2. Restrict recall to the exact requested coordinate (unrelated subcommands and args excluded)
	var relevant []CLIExperienceObservation
	for _, o := range observations {
		if MatchesExactCoordinate(canon, o.Coordinate) {
			relevant = append(relevant, o)
		}
	}

	compressed := CompressExperienceObservations(relevant)
	ranked := RankExperienceObservations(canon, compressed)

	if len(ranked) == 0 {
		return summary
	}

	var failures []CLIExperienceObservation
	var successes []CLIExperienceObservation

	for _, o := range ranked {
		// Tally counts by provenance and result
		switch o.Provenance {
		case ProvenanceField:
			if o.Result == ResultPass {
				summary.FieldPassCount += o.Count
			} else {
				summary.FieldFailCount += o.Count
			}
		case ProvenanceFarm:
			if o.Result == ResultPass {
				summary.FarmPassCount += o.Count
			} else {
				summary.FarmFailCount += o.Count
			}
		}

		if o.Result == ResultFail && len(failures) < 2 {
			failures = append(failures, o)
		} else if o.Result == ResultPass && len(successes) < 2 {
			successes = append(successes, o)
		}
	}

	summary.RecentFailures = failures
	summary.RecentSuccesses = successes

	// Determine high-level status
	totalPass := summary.FieldPassCount + summary.FarmPassCount
	totalFail := summary.FieldFailCount + summary.FarmFailCount

	switch {
	case totalPass > 0 && totalFail > 0:
		summary.Status = "COEXISTING_BOUNDARY"
	case totalFail > 0:
		summary.Status = "OBSERVED_FAIL"
	case totalPass > 0:
		summary.Status = "OBSERVED_PASS"
	default:
		summary.Status = "UNOBSERVED"
	}

	// Determine quality: based on corroboration and provenance completeness
	switch {
	case summary.FarmPassCount+summary.FarmFailCount > 0 && summary.FieldPassCount+summary.FieldFailCount > 0:
		summary.Quality = "HIGH" // Corroborated across field and Farm
	case summary.FieldPassCount+summary.FieldFailCount >= 3:
		summary.Quality = "HIGH"
	case summary.FieldPassCount+summary.FieldFailCount > 0 || summary.FarmPassCount+summary.FarmFailCount > 0:
		summary.Quality = "MEDIUM"
	default:
		summary.Quality = "LOW"
	}

	return summary
}

// QueryCLIExperience provides the top-level retrieval entrypoint for an LLM agent
// inquiring about a specific CLI execution coordinate.
func QueryCLIExperience(target CLIExperienceCoordinate, corpus []CLIExperienceObservation) CLIExperienceSummary {
	return BuildExperienceSummary(target, corpus)
}
