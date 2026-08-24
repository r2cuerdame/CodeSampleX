from pathlib import Path


def replace_once(path, old, new):
    p = Path(path)
    s = p.read_text(encoding="utf-8")
    if old not in s:
        raise SystemExit(f"expected text missing in {path}: {old[:120]!r}")
    if s.count(old) != 1:
        raise SystemExit(f"expected one occurrence in {path}, got {s.count(old)}")
    p.write_text(s.replace(old, new, 1), encoding="utf-8")

# v2 public failure evidence must be independently re-validatable by the server.
# A per-package ObservationBatch does not carry the whole scanner public-name
# allowlist, so retaining node_modules package tokens makes canonical validation
# impossible without trusting client-only state. Keep Sanitize()'s optional
# package preservation for local/other callers, but make the public v2 failure
# contract deliberately strip all node_modules paths.
replace_once(
    "internal/sanitizer/sanitizer.go",
    "func SanitizeFailure(raw string, stage domain.Stage, term domain.FailureTermination, publicPkgs []string) domain.FailureEvidence {\n\tsan := Sanitize(raw, stage, publicPkgs)",
    "func SanitizeFailure(raw string, stage domain.Stage, term domain.FailureTermination, _ []string) domain.FailureEvidence {\n\t// FailureEvidence crosses the public wire and must be canonical without\n\t// client-only context. Observation batches are per package and therefore\n\t// cannot prove the complete public package allowlist that existed on the\n\t// producer. Strip node_modules paths here even when the caller knows a\n\t// package is public; package identity is carried separately as structured\n\t// PURLs/receipt fields.\n\tsan := Sanitize(raw, stage, nil)"
)
replace_once(
    "internal/sanitizer/sanitizer.go",
    "\t\tf.Fingerprint = domain.SHA256Hex([]byte(\"v2|\" + string(stage) + \"|\" + term.FingerprintCoordinate() + \"|\" + san.Code + \"|\" + summary))",
    "\t\tf.Fingerprint = domain.FailureFingerprint(stage, term, san.Code, summary)"
)

# One canonical v2 fingerprint constructor shared by producers and validators.
replace_once(
    "internal/domain/failure.go",
    "// FailureEnvironmentVariant is one exact recorded environment bucket inside\n",
    "// FailureFingerprint returns the canonical v2 cluster identity for modern\n// failure evidence. Producers and ingest validators share this function so a\n// syntactically valid but semantically unrelated SHA cannot become a cluster\n// key. Package/version and environment are intentionally not part of this hash.\nfunc FailureFingerprint(stage Stage, term FailureTermination, errorCode, errorSummary string) string {\n\treturn SHA256Hex([]byte(\"v2|\" + string(stage) + \"|\" + term.FingerprintCoordinate() + \"|\" + errorCode + \"|\" + errorSummary))\n}\n\n// FailureEnvironmentVariant is one exact recorded environment bucket inside\n"
)

# Revert the earlier root-package-only workaround. Canonical v2 evidence now
# strips package paths at the producer, so nil is intentionally sufficient and
# equally correct for batches and receipts.
replace_once(
    "internal/serverstore/validate.go",
    "\tif err := validFailureEvidence(b, []string{p.Name}); err != nil {",
    "\tif err := validFailureEvidence(b); err != nil {"
)
replace_once(
    "internal/serverstore/validate.go",
    "func validFailureEvidence(b domain.ObservationBatch, publicNames []string) error {",
    "func validFailureEvidence(b domain.ObservationBatch) error {"
)
replace_once(
    "internal/serverstore/validate.go",
    "\t\tcanonical := sanitizer.PublicErrorSummary(sanitizer.Sanitize(b.ErrorSummary, b.Stage, publicNames).Template)",
    "\t\tcanonical := sanitizer.PublicErrorSummary(sanitizer.Sanitize(b.ErrorSummary, b.Stage, nil).Template)"
)
replace_once(
    "internal/serverstore/validate.go",
    "\t\texpectedFingerprint := domain.SHA256Hex([]byte(\"v2|\" + string(b.Stage) + \"|\" + term.FingerprintCoordinate() + \"|\" + b.ErrorCode + \"|\" + b.ErrorSummary))",
    "\t\texpectedFingerprint := domain.FailureFingerprint(b.Stage, term, b.ErrorCode, b.ErrorSummary)"
)

# Receipt validation already recomputed the hash; make it use the same domain
# constructor so receipt and batch contracts cannot drift.
replace_once(
    "internal/httpapi/verifications.go",
    "\t\t\twant := domain.SHA256Hex([]byte(\"v2|\" + strings.ToUpper(stage) + \"|\" + term.FingerprintCoordinate() + \"|\" + failure.ErrorCode + \"|\" + failure.ErrorSummary))",
    "\t\t\twant := domain.FailureFingerprint(domain.Stage(strings.ToUpper(stage)), term, failure.ErrorCode, failure.ErrorSummary)"
)

# The prior regression test encoded the wrong contract (preserving a package
# token the server cannot independently prove). Pin the actual invariant:
# public v2 evidence strips that path and is accepted by canonical validation.
p = Path("internal/serverstore/failure_evidence_review_test.go")
s = p.read_text(encoding="utf-8")
s = s.replace("func TestValidateBatchPreservesProducerAllowedPublicNodeModuleName(t *testing.T) {", "func TestValidateBatchAcceptsCanonicalFailureWithoutNodeModuleLeak(t *testing.T) {")
s = s.replace("\tif !strings.Contains(f.ErrorSummary, \"node_modules/react\") {\n\t\tt.Fatalf(\"fixture did not preserve public package token: %q\", f.ErrorSummary)\n\t}", "\tif strings.Contains(f.ErrorSummary, \"node_modules/react\") {\n\t\tt.Fatalf(\"public failure evidence retained a client-only package path token: %q\", f.ErrorSummary)\n\t}\n\tif !strings.Contains(f.ErrorSummary, \"<path>\") {\n\t\tt.Fatalf(\"node_modules path was not normalized: %q\", f.ErrorSummary)\n\t}")
s = s.replace("producer-canonical summary was rejected", "canonical v2 summary was rejected")
p.write_text(s, encoding="utf-8")

# Receipt path regression: even if a verifier supplies its manifest public-name
# list, v2 failure evidence must remain server-revalidatable with nil context.
p = Path("internal/httpapi/failure_evidence_test.go")
s = p.read_text(encoding="utf-8")
s = s.replace(
    'failure := sanitizer.SanitizeFailure("connection refused 127.0.0.1:5432", domain.StageContract,\n\t\tdomain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &code}, nil)',
    'failure := sanitizer.SanitizeFailure("at render (/tmp/app/node_modules/react/index.js:42:7): connection refused 127.0.0.1:5432", domain.StageContract,\n\t\tdomain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &code}, []string{"react"})\n\tif strings.Contains(failure.ErrorSummary, "node_modules/react") {\n\t\tt.Fatalf("receipt failure retained client-only package path token: %q", failure.ErrorSummary)\n\t}'
)
p.write_text(s, encoding="utf-8")
