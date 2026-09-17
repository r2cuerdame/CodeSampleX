package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// CLISubjectSchemaVersion is the version of the CLI subject identity contract
// (schemas/v1/cli-subject.json). A change to what enters the identity — a new
// dimension, a different value-class vocabulary, a different option
// normalization — bumps it, because every subject id and reference computed
// before the change would otherwise silently name something else.
const CLISubjectSchemaVersion = 1

// CLISubjectRefPrefix opens a CLI subject reference. It is deliberately not
// "pkg:": a command is a different kind of coordinate from a package, and the
// point of the subject is that a caller can address one without a fake
// package identity such as pkg:generic/cli/gh@2.40.0 standing in for it.
const CLISubjectRefPrefix = "cli:"

// CLIValueClass is what an option value or operand becomes in a subject's
// identity. The literal never enters: a path, a branch, a host, a token are
// the caller's, and none of them is what makes two runs the same command.
// The class is — `-f <path>` and `-f <url>` are different invocations of
// docker compose, and the network can learn about each.
type CLIValueClass string

const (
	CLIValueArg        CLIValueClass = "arg"
	CLIValuePath       CLIValueClass = "path"
	CLIValueURL        CLIValueClass = "url"
	CLIValueBranch     CLIValueClass = "branch"
	CLIValueHash       CLIValueClass = "hash"
	CLIValueAssignment CLIValueClass = "assignment"
	CLIValueSecret     CLIValueClass = "secret"
)

// valueClassByPlaceholder maps the sanitizer's argument placeholders to the
// closed class vocabulary. The sanitizer stays the single authority on what a
// token looks like; the subject only names the outcome.
var valueClassByPlaceholder = map[string]CLIValueClass{
	"<arg>":             CLIValueArg,
	"<path>":            CLIValuePath,
	"<url>":             CLIValueURL,
	"<branch>":          CLIValueBranch,
	"<hash>":            CLIValueHash,
	"<assignment>":      CLIValueAssignment,
	"<redacted-secret>": CLIValueSecret,
}

func knownValueClass(c CLIValueClass) bool {
	for _, known := range valueClassByPlaceholder {
		if known == c {
			return true
		}
	}
	return false
}

// CLIOption is one semantic option of a command: its name, and the class of
// the value it took if the sanitizer knew it takes one. A bare flag has no
// class. An option whose value-taking is unknown is recorded as bare and its
// value becomes the next operand — the safe reading, since guessing that
// `--ref main` binds would also bind `--yes ci.yml`.
type CLIOption struct {
	Name       string        `json:"name"`
	ValueClass CLIValueClass `json:"valueClass,omitempty"`
}

// CLISubject is the first-class canonical identity of a command-line
// invocation: the thing evidence is ABOUT when the thing is a tool rather
// than a library.
//
// Identity is tool + version + structural command path + semantic options +
// operand classes + OS/arch + shell/runtime. What the run produced — exit
// code, termination, stream fingerprints, timestamps — is evidence about the
// subject (CLIExperienceObservation) and never part of it.
//
// It coexists with package subjects rather than impersonating one. A
// package is a purl; a command is a CLISubject with a `cli:` reference and a
// `clisubject:` id, and neither can be mistaken for the other.
type CLISubject struct {
	SchemaVersion int    `json:"schemaVersion"`
	Tool          string `json:"tool"`
	ToolVersion   string `json:"toolVersion,omitempty"`
	// Path is the command path below the tool, one segment per word:
	// `gh workflow run` is ["workflow","run"], `docker compose up` is
	// ["compose","up"]. A tool with no subcommand vocabulary has none.
	Path []string `json:"path,omitempty"`
	// Options are the semantic flags, sorted by name and deduplicated:
	// `--yes --ref x` and `--ref y --yes` are one command.
	Options []CLIOption `json:"options,omitempty"`
	// Operands are the positional argument classes in the order given.
	// Order is kept because `mv <path> <path>` is not symmetric.
	Operands []CLIValueClass `json:"operands,omitempty"`
	OS       string          `json:"os,omitempty"`
	Arch     string          `json:"arch,omitempty"`
	// Shell is what launched the tool: bash, pwsh, powershell, cmd, zsh.
	Shell string `json:"shell,omitempty"`
	// Runtime is the runtime the tool executes inside, for tools that have
	// one — "node 22.18" for npx, "go 1.26.5" for go test. A tool that does
	// not run in the project's runtime (gh, git, docker) carries none, so a
	// Node project and a Go project running the same gh command are the
	// same subject.
	Runtime string `json:"runtime,omitempty"`
}

// CLISubjectFromArgv builds the subject of one invocation. It goes through
// the same recognition and sanitization as the recorder's coordinate, so an
// argv and the coordinate it was recorded as always yield one subject.
func CLISubjectFromArgv(argv []string, env EnvironmentFingerprint, toolVersion, shell string) CLISubject {
	coord := ParseCLICommand(argv, env)
	coord.ToolVersion = toolVersion
	coord.Shell = shell
	return coord.Subject()
}

// Subject is the first-class identity behind a recorded coordinate. The
// coordinate's flat subcommand string and sanitized args pattern are read
// back into structure using the same vocabulary that produced them.
func (c CLIExperienceCoordinate) Subject() CLISubject {
	canon := c.Canonical()
	s := CLISubject{
		SchemaVersion: CLISubjectSchemaVersion,
		Tool:          canon.Tool,
		ToolVersion:   canon.ToolVersion,
		Path:          strings.Fields(canon.Subcommand),
		OS:            canon.Environment.OS,
		Arch:          canon.Environment.Arch,
		Shell:         canon.Shell,
	}
	s.Options, s.Operands = splitArgsPattern(canon.ArgsPattern)
	if eco := buildToolEcosystems[canon.Tool]; eco != "" && eco == canon.Environment.Ecosystem {
		s.Runtime = canon.Environment.ContextLabel()
	}
	return s.Canonical()
}

// splitArgsPattern reads a sanitized args pattern back into options and
// operand classes. A placeholder directly after a flag is that flag's value
// only when the sanitizer would have bound it there — a sensitive or
// value-consuming flag; otherwise it is an operand, which is exactly how the
// sanitizer classified it.
func splitArgsPattern(pattern string) ([]CLIOption, []CLIValueClass) {
	tokens := strings.Fields(pattern)
	var options []CLIOption
	var operands []CLIValueClass
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if !strings.HasPrefix(tok, "-") || tok == "-" {
			operands = append(operands, classOfPlaceholder(tok))
			continue
		}
		if name, value, ok := strings.Cut(tok, "="); ok {
			options = append(options, CLIOption{Name: name, ValueClass: classOfPlaceholder(value)})
			continue
		}
		opt := CLIOption{Name: tok}
		if (isSensitiveName(tok) || isValueConsumingFlag(tok)) && i+1 < len(tokens) {
			if class, ok := valueClassByPlaceholder[tokens[i+1]]; ok {
				opt.ValueClass = class
				i++
			}
		}
		options = append(options, opt)
	}
	return options, operands
}

// classOfPlaceholder never returns a literal: a token the sanitizer did not
// class is an argument, not a value to keep.
func classOfPlaceholder(tok string) CLIValueClass {
	if class, ok := valueClassByPlaceholder[tok]; ok {
		return class
	}
	return CLIValueArg
}

// Canonical returns the normalized subject: lowercased tool without launcher
// suffix, lowercased path, options sorted and deduplicated, lowercased
// platform dimensions, schema version filled in.
func (s CLISubject) Canonical() CLISubject {
	out := CLISubject{
		SchemaVersion: s.SchemaVersion,
		Tool:          CommandTool([]string{s.Tool}),
		ToolVersion:   strings.TrimSpace(s.ToolVersion),
		OS:            strings.ToLower(strings.TrimSpace(s.OS)),
		Arch:          strings.ToLower(strings.TrimSpace(s.Arch)),
		Shell:         canonicalShell(s.Shell),
		Runtime:       strings.Join(strings.Fields(s.Runtime), " "),
	}
	if out.SchemaVersion == 0 {
		out.SchemaVersion = CLISubjectSchemaVersion
	}
	for _, seg := range s.Path {
		if seg = strings.ToLower(strings.TrimSpace(seg)); seg != "" {
			out.Path = append(out.Path, seg)
		}
	}
	seen := map[CLIOption]bool{}
	for _, o := range s.Options {
		o.Name = strings.TrimSpace(o.Name)
		if o.Name == "" || seen[o] {
			continue
		}
		seen[o] = true
		out.Options = append(out.Options, o)
	}
	sort.Slice(out.Options, func(i, j int) bool {
		if out.Options[i].Name != out.Options[j].Name {
			return out.Options[i].Name < out.Options[j].Name
		}
		return out.Options[i].ValueClass < out.Options[j].ValueClass
	})
	for _, c := range s.Operands {
		if c == "" {
			c = CLIValueArg
		}
		out.Operands = append(out.Operands, c)
	}
	return out
}

func canonicalShell(shell string) string {
	shell = strings.ToLower(strings.TrimSpace(shell))
	if i := strings.LastIndexAny(shell, `/\`); i >= 0 {
		shell = shell[i+1:]
	}
	return strings.TrimSuffix(shell, ".exe")
}

// SubjectID is the content address of the canonical subject.
func (s CLISubject) SubjectID() string {
	sum := sha256.Sum256(MustCanonicalJSON(s.Canonical()))
	return "clisubject:sha256:" + hex.EncodeToString(sum[:])
}

// Ref renders the subject as an addressable reference:
//
//	cli:gh@2.40.0/workflow/run?opt=--ref&opt=--yes&operand=arg&operand=path&os=linux&arch=x64&shell=bash
//
// Tool and version, then the command path as segments, then the options,
// operands and platform dimensions in a fixed order so equal subjects render
// equal strings. ParseCLISubjectRef reads it back.
func (s CLISubject) Ref() string {
	c := s.Canonical()
	var b strings.Builder
	b.WriteString(CLISubjectRefPrefix)
	b.WriteString(url.PathEscape(c.Tool))
	if c.ToolVersion != "" {
		b.WriteString("@")
		b.WriteString(url.PathEscape(c.ToolVersion))
	}
	for _, seg := range c.Path {
		b.WriteString("/")
		b.WriteString(url.PathEscape(seg))
	}
	var params []string
	for _, o := range c.Options {
		v := o.Name
		if o.ValueClass != "" {
			v += ":" + string(o.ValueClass)
		}
		params = append(params, "opt="+refEscape(v))
	}
	for _, op := range c.Operands {
		params = append(params, "operand="+refEscape(string(op)))
	}
	for _, dim := range []struct{ k, v string }{
		{"os", c.OS}, {"arch", c.Arch}, {"shell", c.Shell}, {"runtime", c.Runtime},
	} {
		if dim.v != "" {
			params = append(params, dim.k+"="+refEscape(dim.v))
		}
	}
	if len(params) > 0 {
		b.WriteString("?")
		b.WriteString(strings.Join(params, "&"))
	}
	return b.String()
}

// refEscape escapes only what would break the query grammar, so a reference
// stays readable: `opt=-f:path` rather than `opt=-f%3Apath`.
var refEscape = strings.NewReplacer(
	"%", "%25", "&", "%26", "=", "%3D", "+", "%2B", "#", "%23", " ", "%20", "?", "%3F",
).Replace

// IsCLISubjectRef reports whether s is spelled as a CLI subject reference.
// It says nothing about validity; ParseCLISubjectRef does.
func IsCLISubjectRef(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), CLISubjectRefPrefix)
}

// ParseCLISubjectRef reads a reference produced by Ref. A purl, a bare
// command line or a reference with no tool is refused rather than guessed
// at: an address that half-parses would address the wrong thing.
func ParseCLISubjectRef(ref string) (CLISubject, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(ref), CLISubjectRefPrefix)
	if !ok {
		return CLISubject{}, fmt.Errorf("cli subject: missing %s prefix in %q", CLISubjectRefPrefix, ref)
	}
	head, query, _ := strings.Cut(rest, "?")
	segments := strings.Split(head, "/")
	toolAndVersion, err := url.PathUnescape(segments[0])
	if err != nil {
		return CLISubject{}, fmt.Errorf("cli subject: bad escaping in %q: %w", ref, err)
	}
	tool, version := toolAndVersion, ""
	if at := strings.LastIndex(toolAndVersion, "@"); at > 0 {
		tool, version = toolAndVersion[:at], toolAndVersion[at+1:]
	}
	if strings.TrimSpace(tool) == "" {
		return CLISubject{}, fmt.Errorf("cli subject: missing tool in %q", ref)
	}
	s := CLISubject{SchemaVersion: CLISubjectSchemaVersion, Tool: tool, ToolVersion: version}
	for _, seg := range segments[1:] {
		seg, err := url.PathUnescape(seg)
		if err != nil {
			return CLISubject{}, fmt.Errorf("cli subject: bad escaping in %q: %w", ref, err)
		}
		if seg != "" {
			s.Path = append(s.Path, seg)
		}
	}
	if query == "" {
		return s.Canonical(), nil
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return CLISubject{}, fmt.Errorf("cli subject: bad query in %q: %w", ref, err)
	}
	for _, v := range values["opt"] {
		name, class, _ := strings.Cut(v, ":")
		if name == "" {
			return CLISubject{}, fmt.Errorf("cli subject: empty option in %q", ref)
		}
		if class != "" && !knownValueClass(CLIValueClass(class)) {
			return CLISubject{}, fmt.Errorf("cli subject: unknown value class %q in %q", class, ref)
		}
		s.Options = append(s.Options, CLIOption{Name: name, ValueClass: CLIValueClass(class)})
	}
	for _, v := range values["operand"] {
		if !knownValueClass(CLIValueClass(v)) {
			return CLISubject{}, fmt.Errorf("cli subject: unknown value class %q in %q", v, ref)
		}
		s.Operands = append(s.Operands, CLIValueClass(v))
	}
	s.OS = values.Get("os")
	s.Arch = values.Get("arch")
	s.Shell = values.Get("shell")
	s.Runtime = values.Get("runtime")
	for k := range values {
		switch k {
		case "opt", "operand", "os", "arch", "shell", "runtime":
		default:
			return CLISubject{}, fmt.Errorf("cli subject: unknown dimension %q in %q", k, ref)
		}
	}
	return s.Canonical(), nil
}

// Coordinate projects the subject back onto the recorder's coordinate shape
// so subject-addressed lookups can reuse the existing experience summary.
// Options are rendered in canonical order, which is why lookups by subject
// must compare subjects (MatchCLISubject), never the rendered pattern.
func (s CLISubject) Coordinate() CLIExperienceCoordinate {
	c := s.Canonical()
	var args []string
	for _, o := range c.Options {
		if o.ValueClass != "" {
			args = append(args, o.Name, "<"+placeholderOfClass(o.ValueClass)+">")
		} else {
			args = append(args, o.Name)
		}
	}
	for _, op := range c.Operands {
		args = append(args, "<"+placeholderOfClass(op)+">")
	}
	return CLIExperienceCoordinate{
		Tool:        c.Tool,
		ToolVersion: c.ToolVersion,
		Subcommand:  strings.Join(c.Path, " "),
		ArgsPattern: strings.Join(args, " "),
		Shell:       c.Shell,
		Environment: EnvironmentFingerprint{SchemaVersion: 1, OS: c.OS, Arch: c.Arch},
	}
}

func placeholderOfClass(c CLIValueClass) string {
	for placeholder, class := range valueClassByPlaceholder {
		if class == c {
			return strings.Trim(placeholder, "<>")
		}
	}
	return string(CLIValueArg)
}

// DisplayCommand renders the subject the way a reader recognizes it:
// `gh workflow run --ref --yes <arg> <path>`.
func (s CLISubject) DisplayCommand() string {
	c := s.Canonical()
	parts := []string{c.Tool}
	parts = append(parts, c.Path...)
	for _, o := range c.Options {
		if o.ValueClass != "" {
			parts = append(parts, o.Name+" <"+string(o.ValueClass)+">")
		} else {
			parts = append(parts, o.Name)
		}
	}
	for _, op := range c.Operands {
		parts = append(parts, "<"+string(op)+">")
	}
	return strings.Join(parts, " ")
}

// CLISubjectMatch is the exact/adaptable/miss verdict for one candidate
// subject against the subject a caller asked about, with the dimensions that
// decided it. Different lists dimensions both sides declared and disagree on;
// Undeclared lists dimensions only one side declared, which is unknown, not
// a difference.
type CLISubjectMatch struct {
	Grade      MatchGrade `json:"grade"`
	Different  []string   `json:"different,omitempty"`
	Undeclared []string   `json:"undeclared,omitempty"`
}

// MatchCLISubject grades candidate against target.
//
//   - NO_SAFE_MATCH: a different tool or a different command path. Evidence
//     about `git worktree list` says nothing about `git worktree add`.
//   - ADAPTATION_REQUIRED: the same command, but a declared dimension —
//     version, options, operands, OS, arch, shell, runtime — differs. The
//     evidence is about this command somewhere else, and the delta is named.
//   - COMPATIBLE: nothing declared differs, but a dimension is declared on
//     one side only. Not a difference, and not proof of sameness either.
//   - EXACT: every dimension declared on both sides and equal.
//
// Options and operands are always declared: an empty list is the fact that
// the command took none, not an absence of information.
func MatchCLISubject(target, candidate CLISubject) CLISubjectMatch {
	t, c := target.Canonical(), candidate.Canonical()
	if t.Tool == "" || t.Tool != c.Tool {
		return CLISubjectMatch{Grade: GradeNoSafeMatch, Different: []string{"tool"}}
	}
	if strings.Join(t.Path, "/") != strings.Join(c.Path, "/") {
		return CLISubjectMatch{Grade: GradeNoSafeMatch, Different: []string{"path"}}
	}
	var m CLISubjectMatch
	declared := func(name, a, b string) {
		switch {
		case a == "" && b == "":
		case a == "" || b == "":
			m.Undeclared = append(m.Undeclared, name)
		case a != b:
			m.Different = append(m.Different, name)
		}
	}
	declared("toolVersion", t.ToolVersion, c.ToolVersion)
	if !sameOptions(t.Options, c.Options) {
		m.Different = append(m.Different, "options")
	}
	if !sameOperands(t.Operands, c.Operands) {
		m.Different = append(m.Different, "operands")
	}
	declared("os", t.OS, c.OS)
	declared("arch", t.Arch, c.Arch)
	declared("shell", t.Shell, c.Shell)
	declared("runtime", t.Runtime, c.Runtime)
	switch {
	case len(m.Different) > 0:
		m.Grade = GradeAdaptationRequired
	case len(m.Undeclared) > 0:
		m.Grade = GradeCompatible
	default:
		m.Grade = GradeExact
	}
	return m
}

func sameOptions(a, b []CLIOption) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameOperands(a, b []CLIValueClass) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
