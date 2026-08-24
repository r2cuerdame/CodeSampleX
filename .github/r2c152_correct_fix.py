from pathlib import Path


def replace_once(path, old, new):
    p = Path(path)
    s = p.read_text(encoding="utf-8")
    if old not in s:
        raise SystemExit(f"expected text missing in {path}: {old[:160]!r}")
    if s.count(old) != 1:
        raise SystemExit(f"expected one occurrence in {path}, got {s.count(old)}")
    p.write_text(s.replace(old, new, 1), encoding="utf-8")

# Public v2 FailureEvidence must be independently canonicalizable by the
# receiver. The producer's full public-package allowlist is not part of an
# ObservationBatch or receipt, so retaining node_modules/<public-name> makes
# canonicality depend on client-only context. Keep Sanitize() behavior for its
# other callers, but public v2 evidence always strips node_modules paths.
replace_once(
    "internal/sanitizer/sanitizer.go",
    "func SanitizeFailure(raw string, stage domain.Stage, term domain.FailureTermination, publicPkgs []string) domain.FailureEvidence {\n\tsan := Sanitize(raw, stage, publicPkgs)",
    "func SanitizeFailure(raw string, stage domain.Stage, term domain.FailureTermination, _ []string) domain.FailureEvidence {\n\t// Public FailureEvidence must be canonical without trusting producer-only\n\t// allowlists. Package identity already travels separately as structured\n\t// PURLs/receipt data, so node_modules paths are normalized here.\n\tsan := Sanitize(raw, stage, nil)"
)
replace_once(
    "internal/sanitizer/sanitizer.go",
    "\t\tf.Fingerprint = domain.SHA256Hex([]byte(\"v2|\" + string(stage) + \"|\" + term.FingerprintCoordinate() + \"|\" + san.Code + \"|\" + summary))",
    "\t\tf.Fingerprint = domain.FailureFingerprint(stage, term, san.Code, summary)"
)

# One canonical v2 fingerprint constructor shared by all producers and both
# server validation paths.
replace_once(
    "internal/domain/failure.go",
    "// FailureEnvironmentVariant is one exact recorded environment bucket inside\n",
    "// FailureFingerprint returns the canonical v2 cluster identity for modern\n// failure evidence. Package/version and exact environment are intentionally\n// outside this hash and remain structured dimensions beside the cluster.\nfunc FailureFingerprint(stage Stage, term FailureTermination, errorCode, errorSummary string) string {\n\treturn SHA256Hex([]byte(\"v2|\" + string(stage) + \"|\" + term.FingerprintCoordinate() + \"|\" + errorCode + \"|\" + errorSummary))\n}\n\n// FailureEnvironmentVariant is one exact recorded environment bucket inside\n"
)

# Remove the earlier root-package allowlist workaround. It was incomplete for
# dependency package names and impossible to mirror for receipts. After the
# producer contract above, canonical validation deliberately needs no allowlist.
replace_once(
    "internal/serverstore/validate.go",
    "\tif err := validFailureEvidence(b, p.Name); err != nil {",
    "\tif err := validFailureEvidence(b); err != nil {"
)
replace_once(
    "internal/serverstore/validate.go",
    "func validFailureEvidence(b domain.ObservationBatch, publicPackage string) error {",
    "func validFailureEvidence(b domain.ObservationBatch) error {"
)
replace_once(
    "internal/serverstore/validate.go",
    "\t\t// Producers intentionally preserve a public package token from\n\t\t// node_modules/<name>. Revalidation must use the same allowlist or a\n\t\t// producer-canonical summary becomes <path> here and is rejected.\n\t\tpublicNames := []string(nil)\n\t\tif publicPackage != \"\" {\n\t\t\tpublicNames = []string{publicPackage}\n\t\t}\n\t\tcanonical := sanitizer.PublicErrorSummary(sanitizer.Sanitize(b.ErrorSummary, b.Stage, publicNames).Template)",
    "\t\tcanonical := sanitizer.PublicErrorSummary(sanitizer.Sanitize(b.ErrorSummary, b.Stage, nil).Template)"
)
replace_once(
    "internal/serverstore/validate.go",
    "\t\texpected := domain.SHA256Hex([]byte(\"v2|\" + string(b.Stage) + \"|\" + term.FingerprintCoordinate() + \"|\" + b.ErrorCode + \"|\" + b.ErrorSummary))",
    "\t\texpected := domain.FailureFingerprint(b.Stage, term, b.ErrorCode, b.ErrorSummary)"
)

# Receipt validation uses the exact same fingerprint constructor.
replace_once(
    "internal/httpapi/verifications.go",
    "\t\t\twant := domain.SHA256Hex([]byte(\"v2|\" + strings.ToUpper(stage) + \"|\" + term.FingerprintCoordinate() + \"|\" + failure.ErrorCode + \"|\" + failure.ErrorSummary))",
    "\t\t\twant := domain.FailureFingerprint(domain.Stage(strings.ToUpper(stage)), term, failure.ErrorCode, failure.ErrorSummary)"
)

# Replace the wrong regression contract that expected node_modules/react to
# survive in public FailureEvidence.
p = Path("internal/serverstore/failure_evidence_review_test.go")
s = p.read_text(encoding="utf-8")
s = s.replace(
    "func TestValidateBatchPreservesProducerAllowedPublicNodeModuleName(t *testing.T) {",
    "func TestValidateBatchAcceptsCanonicalFailureWithoutNodeModuleLeak(t *testing.T) {",
)
s = s.replace(
    "\tif !strings.Contains(f.ErrorSummary, \"node_modules/react\") {\n\t\tt.Fatalf(\"fixture did not preserve public package token: %q\", f.ErrorSummary)\n\t}",
    "\tif strings.Contains(f.ErrorSummary, \"node_modules/react\") {\n\t\tt.Fatalf(\"public failure evidence retained a client-only package path token: %q\", f.ErrorSummary)\n\t}\n\tif !strings.Contains(f.ErrorSummary, \"<path>\") {\n\t\tt.Fatalf(\"node_modules path was not normalized: %q\", f.ErrorSummary)\n\t}",
)
s = s.replace("producer-canonical summary was rejected", "canonical v2 summary was rejected")
p.write_text(s, encoding="utf-8")

# Receipt regression proves verifier callers may pass public names but the wire
# evidence remains independently revalidatable by the server.
p = Path("internal/httpapi/failure_evidence_test.go")
s = p.read_text(encoding="utf-8")
s = s.replace(
    'failure := sanitizer.SanitizeFailure("connection refused 127.0.0.1:5432", domain.StageContract,\n\t\tdomain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &code}, nil)',
    'failure := sanitizer.SanitizeFailure("at render (/tmp/app/node_modules/react/index.js:42:7): connection refused 127.0.0.1:5432", domain.StageContract,\n\t\tdomain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &code}, []string{"react"})\n\tif strings.Contains(failure.ErrorSummary, "node_modules/react") {\n\t\tt.Fatalf("receipt failure retained client-only package path token: %q", failure.ErrorSummary)\n\t}',
)
p.write_text(s, encoding="utf-8")
