package httpapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// tarGz builds an artifact from name → content.
func tarGzOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// checkDeclaredVersions exists to refuse a manifest that names a version its
// own lockfile never resolved — "evidence about a version it had never been
// run on, which is the one thing this whole network exists not to do".
//
// It could not fire on any upload ever made. The tar entry is drained once
// into content, and the lockfile branch read the SAME reader a second time:
// zero bytes, nil error, every lockfile stored as "". The map was non-empty
// so the guard did not early-return either — it simply never found a
// version. Only its own unit tests, which build the map by hand, ever
// exercised it.
func TestALockfileThatContradictsTheManifestIsRefused(t *testing.T) {
	manifest := domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{
			SchemaVersion: 1, Kind: "HOW", Goal: "post json",
			Packages: []string{"pkg:npm/axios@1.12.0"}, Contract: []string{"posts"},
		},
		Packages:        []string{"pkg:npm/axios@1.12.0"},
		ContractCommand: []string{"node", "test/contract.mjs"},
		Environment:     domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm"},
	}
	art := tarGzOf(t, map[string]string{
		"csx.json": string(domain.MustCanonicalJSON(manifest)),
		// The lockfile resolves a DIFFERENT version than the manifest declares.
		"package-lock.json": `{"lockfileVersion":3,"packages":{"node_modules/axios":{"version":"1.19.0"}}}`,
	})

	err := checkArtifactStatic(art, manifest)
	if err == nil {
		t.Fatal("a manifest declaring axios@1.12.0 was accepted with a lockfile resolving 1.19.0")
	}
	if !strings.Contains(err.Error(), "1.12.0") && !strings.Contains(err.Error(), "axios") {
		t.Errorf("the refusal does not say which version it disbelieved: %v", err)
	}
}

// The honest case still passes: manifest and lockfile agree.
func TestAnAgreeingLockfileIsAccepted(t *testing.T) {
	manifest := domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{
			SchemaVersion: 1, Kind: "HOW", Goal: "post json",
			Packages: []string{"pkg:npm/axios@1.12.0"}, Contract: []string{"posts"},
		},
		Packages:        []string{"pkg:npm/axios@1.12.0"},
		ContractCommand: []string{"node", "test/contract.mjs"},
		Environment:     domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm"},
	}
	art := tarGzOf(t, map[string]string{
		"csx.json":          string(domain.MustCanonicalJSON(manifest)),
		"package-lock.json": `{"lockfileVersion":3,"packages":{"node_modules/axios":{"version":"1.12.0"}}}`,
	})
	if err := checkArtifactStatic(art, manifest); err != nil {
		t.Fatalf("an agreeing lockfile was refused: %v", err)
	}
}
