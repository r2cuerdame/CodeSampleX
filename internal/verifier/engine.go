// Package verifier turns sandbox stage runs into signed verification
// receipts (goal.md §7.7, §16) and implements the cross-verification
// client that claims jobs from the server, re-verifies artifacts locally,
// and posts receipts back.
package verifier

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/samples"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
)

// Run executes the verification pipeline over an unpacked sample dir and
// returns a signed receipt. Stage semantics:
//
//   - resolve → compile → contract run in order; a FAIL short-circuits every
//     later stage to SKIPPED (never guessed results).
//   - load is derived, never guessed: PASS only when the contract actually
//     ran and passed (the module necessarily loaded); SKIPPED otherwise.
//
// Full logs stay local; only their SHA-256 digest enters the receipt.
// The sample ID is recomputed from the directory via the canonical
// artifact builder, so a receipt can never mis-attribute results.
func Run(
	ctx context.Context,
	r sandbox.Runner,
	cap domain.SandboxCapability,
	sampleDir string,
	m domain.SampleManifest,
	ident *identity.Identity,
	env domain.EnvironmentFingerprint,
) (domain.VerificationReceipt, error) {
	receipt, _, err := RunLogged(ctx, r, cap, sampleDir, m, ident, env)
	return receipt, err
}

// RunLogged is Run, and also returns each stage's output.
//
// The receipt stores only a DIGEST of the logs, deliberately: raw output
// carries paths, usernames and tokens and must never leave the machine
// (goal.md §8.5). But the logs were then discarded entirely, so an author
// whose contract failed got a table of PASS/FAIL and nothing else -- no way
// to see what their own assertion did, on their own machine, in a container
// they cannot enter. The only way to find out was to rebuild the container
// by hand and guess at the flags.
//
// That is the loop the whole contribution side depends on. These logs stay
// local: they go to the author's terminal and nowhere near a receipt.
func RunLogged(
	ctx context.Context,
	r sandbox.Runner,
	cap domain.SandboxCapability,
	sampleDir string,
	m domain.SampleManifest,
	ident *identity.Identity,
	env domain.EnvironmentFingerprint,
) (domain.VerificationReceipt, map[string]string, error) {
	var zero domain.VerificationReceipt
	if r == nil {
		return zero, nil, errors.New("verifier: nil runner")
	}
	if ident == nil {
		return zero, nil, errors.New("verifier: nil identity")
	}

	// Hash the pristine tree BEFORE any stage dirties it with node_modules etc.
	_, sampleID, err := samples.BuildArtifact(sampleDir)
	if err != nil {
		return zero, nil, fmt.Errorf("verifier: hash sample: %w", err)
	}

	// The receipt must describe where the stages ran. Under CONTAINER_RUN
	// that is the container (linux + the image runtime), not this host —
	// otherwise a Windows machine would be credited with a linux result.
	env = r.StageEnvironment(env, m)

	resolve := r.Resolve(ctx, sampleDir, m)
	var compile, contract sandbox.StageResult
	switch {
	case resolve.Result == sandbox.ResultFail:
		compile = sandbox.StageResult{Result: sandbox.ResultSkipped}
		contract = sandbox.StageResult{Result: sandbox.ResultSkipped}
	default:
		compile = r.Build(ctx, sampleDir, m)
		if compile.Result == sandbox.ResultFail {
			contract = sandbox.StageResult{Result: sandbox.ResultSkipped}
		} else {
			contract = r.Contract(ctx, sampleDir, m)
		}
	}

	load := sandbox.ResultSkipped
	if contract.Result == sandbox.ResultPass {
		load = sandbox.ResultPass
	}

	logs := resolve.Log + "\n" + compile.Log + "\n" + contract.Log

	caseID := m.Case.CaseID
	if caseID == "" {
		caseID = m.Case.ComputeID()
	}

	receipt := domain.VerificationReceipt{
		SchemaVersion:    1,
		SampleID:         sampleID,
		CaseID:           caseID,
		EnvironmentHash:  env.Hash(),
		Environment:      env,
		ResolvedPackages: resolvedPackages(sampleDir, m),
		Stages: map[string]string{
			"resolve":  resolve.Result,
			"compile":  compile.Result,
			"contract": contract.Result,
			"load":     load,
		},
		VerifierAdapter:   m.VerifierAdapter,
		SandboxCapability: cap,
		LogsDigest:        domain.SHA256Hex([]byte(logs)),
		CreatedAt:         time.Now().UTC().Format(time.RFC3339),
		PeerID:            ident.PeerID(),
		PeerPubkey:        ident.PubkeyB64(),
	}
	receipt.PeerSignature = ident.Sign(receipt.SigningBytes())
	return receipt, map[string]string{
		"resolve":  resolve.Log,
		"compile":  compile.Log,
		"contract": contract.Log,
	}, nil
}
