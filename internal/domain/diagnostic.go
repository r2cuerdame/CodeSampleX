package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
)

const DiagnosticSchemaVersion = "csx.debug.v1"

const (
	DependencyUnknown    = "unknown"
	DependencyProvenNone = "proven-no-dependencies"
	DependencyResolved   = "resolved"
)

// DiagnosticTrace is the single debug representation rendered by CLI/MCP as
// text and JSON. It contains public coordinates, bounded aggregate counts and
// sanitized reason codes only. Query prose, local paths and raw diagnostics
// do not belong here.
type DiagnosticTrace struct {
	SchemaVersion    string                   `json:"schemaVersion"`
	RequestID        string                   `json:"requestId"`
	Versions         DiagnosticVersions       `json:"versions"`
	Coordinate       DiagnosticCoordinate     `json:"normalizedCoordinate"`
	Query            DiagnosticDecomposition  `json:"queryDecomposition"`
	Pipeline         []DiagnosticPipelineStep `json:"matchPipeline,omitempty"`
	Candidates       []DiagnosticCandidate    `json:"candidates,omitempty"`
	Evidence         DiagnosticEvidence       `json:"evidence"`
	Failure          *DiagnosticFailure       `json:"failure,omitempty"`
	Gaps             []DiagnosticGap          `json:"gaps,omitempty"`
	Decision         string                   `json:"decision"`
	PotentialAnomaly bool                     `json:"potentialAnomaly"`
}

type DiagnosticVersions struct {
	Client   string `json:"client,omitempty"`
	Server   string `json:"server,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Payload  int    `json:"payload"`
}

type DiagnosticCoordinate struct {
	Packages    []string               `json:"packages,omitempty"`
	Symbols     []string               `json:"symbols,omitempty"`
	Environment EnvironmentFingerprint `json:"environment"`
}

type DiagnosticDecomposition struct {
	PackageCount          int `json:"packageCount"`
	SymbolCount           int `json:"symbolCount"`
	ContextSymbolCount    int `json:"contextSymbolCount"`
	StructuredDiagnostics int `json:"structuredDiagnostics"`
	TopicTokenCount       int `json:"topicTokenCount"`
}

type DiagnosticPipelineStep struct {
	Stage          string `json:"stage"`
	InputCount     int    `json:"inputCount"`
	OutputCount    int    `json:"outputCount"`
	RejectedCount  int    `json:"rejectedCount,omitempty"`
	ElapsedMicros  int64  `json:"elapsedMicros"`
	EvidenceSource string `json:"evidenceSource,omitempty"`
}

type DiagnosticCandidate struct {
	SampleID            string             `json:"sampleId,omitempty"`
	Packages            []string           `json:"packages,omitempty"`
	Match               MatchGrade         `json:"match,omitempty"`
	Score               float64            `json:"score"`
	Outcome             string             `json:"outcome"`
	ReasonCodes         []string           `json:"reasonCodes,omitempty"`
	RelevanceSignals    []string           `json:"relevanceSignals,omitempty"`
	FeatureContribution map[string]float64 `json:"featureContribution,omitempty"`
}

type DiagnosticEvidence struct {
	UsageObservationScope       string `json:"usageObservationScope"`
	UsageObservationSymbolProof bool   `json:"usageObservationIsSymbolProof"`
	VerifiedCoordinateCount     int    `json:"verifiedCoordinateCount"`
	UnverifiedCoordinateCount   int    `json:"unverifiedCoordinateCount"`
	DependencyState             string `json:"dependencyState"`
}

type DiagnosticFailure struct {
	OuterCommand string                   `json:"outerCommand,omitempty"`
	OuterStage   string                   `json:"outerStage,omitempty"`
	ExitCode     int                      `json:"outerExit"`
	Termination  string                   `json:"termination,omitempty"`
	Events       []DiagnosticFailureEvent `json:"processLineage,omitempty"`
}

type DiagnosticFailureEvent struct {
	ActualToolchain   string `json:"actualToolchain,omitempty"`
	ActualStage       string `json:"actualFailureStage"`
	DiagnosticCode    string `json:"diagnosticCode,omitempty"`
	Fingerprint       string `json:"fingerprint,omitempty"`
	DiagnosticQuality string `json:"diagnosticQuality,omitempty"`
	StageEvidence     string `json:"stageEvidence,omitempty"`
	EvidenceGap       string `json:"evidenceGap,omitempty"`
}

type DiagnosticGap struct {
	Code       string `json:"code"`
	ReasonCode string `json:"reasonCode"`
	Detail     string `json:"detail,omitempty"`
}

func NewDiagnosticTrace(req SearchRequest) *DiagnosticTrace {
	return &DiagnosticTrace{
		SchemaVersion: DiagnosticSchemaVersion,
		RequestID:     newDiagnosticRequestID(),
		Versions:      DiagnosticVersions{Payload: req.SchemaVersion},
		Coordinate: DiagnosticCoordinate{
			Packages:    normalizedPublicCoordinates(req.Packages),
			Symbols:     safeDiagnosticSymbols(req.Symbols),
			Environment: req.Environment.Normalize(),
		},
		Query: DiagnosticDecomposition{
			PackageCount: len(req.Packages), SymbolCount: len(req.Symbols),
			ContextSymbolCount:    len(req.ContextSymbols),
			StructuredDiagnostics: diagnosticPresence(req),
		},
		Evidence: DiagnosticEvidence{
			UsageObservationScope:       "project-level",
			UsageObservationSymbolProof: false,
			DependencyState:             DependencyUnknown,
		},
	}
}

func newDiagnosticRequestID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "req_" + hex.EncodeToString(b[:])
	}
	// Entropy failure must not change search semantics. The value remains a
	// stable identifier for this response without pretending to be random.
	return "req_unavailable"
}

func normalizedPublicCoordinates(raw []string) []string {
	var out []string
	for _, coordinate := range raw {
		p, err := ParsePURL(coordinate)
		if err == nil {
			out = append(out, p.String())
		}
	}
	sort.Strings(out)
	return out
}

func safeDiagnosticSymbols(raw []string) []string {
	var out []string
	for _, symbol := range raw {
		symbol = strings.TrimSpace(symbol)
		pathLike := strings.HasPrefix(symbol, "/") || strings.HasPrefix(symbol, "./") ||
			strings.HasPrefix(symbol, "../") || strings.Contains(symbol, "/../")
		if symbol == "" || len(symbol) > 128 || pathLike || strings.Contains(symbol, "://") ||
			strings.ContainsAny(symbol, "\\\r\n\t") {
			continue
		}
		out = append(out, symbol)
	}
	sort.Strings(out)
	return out
}

func diagnosticPresence(req SearchRequest) int {
	n := 0
	if req.ErrorCode != "" {
		n++
	}
	if req.ErrorFingerprint != "" || len(req.ErrorFingerprints) > 0 {
		n++
	}
	return n
}

func (d *DiagnosticTrace) AddGap(code, reason, detail string) {
	if d == nil || code == "" || reason == "" {
		return
	}
	for _, gap := range d.Gaps {
		if gap.Code == code && gap.ReasonCode == reason && gap.Detail == detail {
			return
		}
	}
	d.Gaps = append(d.Gaps, DiagnosticGap{Code: code, ReasonCode: reason, Detail: detail})
}

// RecordOutputGateDiagnostic adds the final rendering decision to the same
// trace the search engine started. The output gate is shared by MCP, daemon
// and CLI paths, so keeping this mutation here prevents those entry points
// from explaining the same suppression differently.
func RecordOutputGateDiagnostic(resp *SearchResponse, req SearchRequest, beforeGate int, suppressed []SuppressedCandidate) {
	if resp == nil || !req.Debug {
		return
	}
	if resp.Diagnostic == nil {
		resp.Diagnostic = NewDiagnosticTrace(req)
	}
	d := resp.Diagnostic
	for _, step := range d.Pipeline {
		if step.Stage == "output-relevance" {
			return
		}
	}
	d.Pipeline = append(d.Pipeline, DiagnosticPipelineStep{
		Stage: "output-relevance", InputCount: beforeGate, OutputCount: len(resp.Results),
		RejectedCount: len(suppressed), EvidenceSource: "stable-relevance-reason-codes",
	})
	for _, result := range resp.Results {
		signals := result.RelevanceSignals(req, nil)
		for i := range d.Candidates {
			if d.Candidates[i].SampleID == result.SampleID {
				d.Candidates[i].RelevanceSignals = signals
				break
			}
		}
	}
	for _, candidate := range suppressed {
		found := false
		for i := range d.Candidates {
			if d.Candidates[i].SampleID == candidate.SampleID {
				d.Candidates[i].Outcome = "suppressed"
				d.Candidates[i].ReasonCodes = appendDiagnosticReason(d.Candidates[i].ReasonCodes, candidate.Reason)
				d.Candidates[i].RelevanceSignals = candidate.Signals
				found = true
				break
			}
		}
		if !found && len(d.Candidates) < 50 {
			d.Candidates = append(d.Candidates, DiagnosticCandidate{
				SampleID: candidate.SampleID, Packages: candidate.Packages,
				Match: candidate.Grade, Score: candidate.Score, Outcome: "suppressed",
				ReasonCodes: []string{candidate.Reason}, RelevanceSignals: candidate.Signals,
			})
		}
	}
	if resp.Miss || len(resp.Results) == 0 {
		d.Decision = string(GradeNoSafeMatch)
		if len(suppressed) > 0 {
			d.AddGap("BOUNDARY", "CANDIDATE_REJECTED_BY_RELEVANCE", "retrieved candidates shared no concrete subject with the request")
		}
	} else {
		d.Decision = string(resp.Results[0].Grade)
	}
}

func appendDiagnosticReason(values []string, value string) []string {
	for _, have := range values {
		if have == value {
			return values
		}
	}
	return append(values, value)
}

// RenderDiagnosticText renders only fields from DiagnosticTrace. JSON and
// human output therefore cannot drift into separate diagnostic models.
func RenderDiagnosticText(w io.Writer, d *DiagnosticTrace) {
	if d == nil {
		return
	}
	fmt.Fprintf(w, "\nDEBUG DIAGNOSTIC (%s)\n", d.SchemaVersion)
	fmt.Fprintf(w, "request_id: %s\n", d.RequestID)
	fmt.Fprintf(w, "versions: client=%s server=%s protocol=%s payload=%d\n",
		unknownIfEmpty(d.Versions.Client), unknownIfEmpty(d.Versions.Server),
		unknownIfEmpty(d.Versions.Protocol), d.Versions.Payload)
	if len(d.Coordinate.Packages) > 0 {
		fmt.Fprintf(w, "normalized_packages: %s\n", strings.Join(d.Coordinate.Packages, ", "))
	}
	if len(d.Coordinate.Symbols) > 0 {
		fmt.Fprintf(w, "normalized_symbols: %s\n", strings.Join(d.Coordinate.Symbols, ", "))
	}
	fmt.Fprintf(w, "normalized_environment: %s\n", diagnosticEnvironment(d.Coordinate.Environment))
	fmt.Fprintln(w, "match_pipeline:")
	for _, step := range d.Pipeline {
		fmt.Fprintf(w, "- %s: %d -> %d, rejected=%d, elapsed=%dus",
			step.Stage, step.InputCount, step.OutputCount, step.RejectedCount, step.ElapsedMicros)
		if step.EvidenceSource != "" {
			fmt.Fprintf(w, ", source=%s", step.EvidenceSource)
		}
		fmt.Fprintln(w)
	}
	if len(d.Candidates) > 0 {
		fmt.Fprintln(w, "candidate_decisions:")
		for _, candidate := range d.Candidates {
			fmt.Fprintf(w, "- %s: outcome=%s score=%.4f match=%s",
				candidate.SampleID, candidate.Outcome, candidate.Score, candidate.Match)
			if len(candidate.ReasonCodes) > 0 {
				fmt.Fprintf(w, " reasons=%s", strings.Join(candidate.ReasonCodes, ","))
			}
			if len(candidate.RelevanceSignals) > 0 {
				fmt.Fprintf(w, " relevance=%s", strings.Join(candidate.RelevanceSignals, ","))
			}
			if len(candidate.FeatureContribution) > 0 {
				keys := make([]string, 0, len(candidate.FeatureContribution))
				for key := range candidate.FeatureContribution {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				fmt.Fprint(w, " features=")
				for i, key := range keys {
					if i > 0 {
						fmt.Fprint(w, ",")
					}
					fmt.Fprintf(w, "%s:%.4f", key, candidate.FeatureContribution[key])
				}
			}
			fmt.Fprintln(w)
		}
	}
	fmt.Fprintf(w, "evidence_scope: %s; usage_observation_is_symbol_proof=%t; verified=%d; unverified=%d\n",
		d.Evidence.UsageObservationScope, d.Evidence.UsageObservationSymbolProof,
		d.Evidence.VerifiedCoordinateCount, d.Evidence.UnverifiedCoordinateCount)
	fmt.Fprintf(w, "dependency_state: %s\n", d.Evidence.DependencyState)
	if d.Failure != nil {
		fmt.Fprintf(w, "failure: outer=%s outer_stage=%s exit=%d termination=%s\n",
			unknownIfEmpty(d.Failure.OuterCommand), unknownIfEmpty(d.Failure.OuterStage),
			d.Failure.ExitCode, unknownIfEmpty(d.Failure.Termination))
		for _, event := range d.Failure.Events {
			fmt.Fprintf(w, "- lineage: toolchain=%s actual_stage=%s code=%s fingerprint=%s quality=%s evidence=%s gap=%s\n",
				unknownIfEmpty(event.ActualToolchain), event.ActualStage,
				unknownIfEmpty(event.DiagnosticCode), unknownIfEmpty(event.Fingerprint),
				unknownIfEmpty(event.DiagnosticQuality), unknownIfEmpty(event.StageEvidence),
				unknownIfEmpty(event.EvidenceGap))
		}
	}
	if len(d.Gaps) > 0 {
		fmt.Fprintln(w, "gaps:")
		for _, gap := range d.Gaps {
			fmt.Fprintf(w, "- %s/%s", gap.Code, gap.ReasonCode)
			if gap.Detail != "" {
				fmt.Fprintf(w, ": %s", gap.Detail)
			}
			fmt.Fprintln(w)
		}
	}
	fmt.Fprintf(w, "decision: %s\n", d.Decision)
	fmt.Fprintf(w, "potential_anomaly: %t\n", d.PotentialAnomaly)
}

func diagnosticEnvironment(e EnvironmentFingerprint) string {
	var parts []string
	for _, item := range []struct{ key, value string }{
		{"ecosystem", e.Ecosystem}, {"os", e.OS}, {"osVersion", e.OSVersionBucket},
		{"arch", e.Arch}, {"runtime", e.Runtime}, {"runtimeVersion", e.RuntimeVersion},
		{"context", e.ExecutionContext}, {"compiler", e.Compiler}, {"compilerVersion", e.CompilerVersion},
		{"libc", e.Libc}, {"libcVersion", e.LibcVersion},
	} {
		if item.value != "" {
			parts = append(parts, item.key+"="+item.value)
		}
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, " ")
}

func unknownIfEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}
