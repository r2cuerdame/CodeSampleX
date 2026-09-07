//go:build windows

package update

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBootstrapLauncherWithLocalManifest(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("LOCALAPPDATA", tempDir)

	root := filepath.Join(tempDir, "csx")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}

	payloadBytes := []byte("fake-csx-binary-content")
	staged := filepath.Join(root, "csx-payload.new.exe")
	if err := os.WriteFile(staged, payloadBytes, 0o700); err != nil {
		t.Fatal(err)
	}

	h := sha256.Sum256(payloadBytes)
	payloadSHA256 := hex.EncodeToString(h[:])

	rawManifest, pub, _ := signedTestManifest(t, func(man *Manifest) {
		man.Version = "v1.0.0"
		man.PublishedAt = time.Now().UTC().Add(-time.Hour)
		man.ExpiresAt = time.Now().UTC().Add(24 * time.Hour)
		man.Assets = []Asset{{
			OS:     "windows",
			Arch:   runtime.GOARCH,
			URL:    "https://github.com/r2cuerdame/CodeSampleX/releases/download/v1.0.0/csx-windows-" + runtime.GOARCH + ".exe",
			Size:   int64(len(payloadBytes)),
			SHA256: payloadSHA256,
		}}
	})

	origPub := PublicKeyBase64
	PublicKeyBase64 = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { PublicKeyBase64 = origPub })

	manifestFile := filepath.Join(root, "csx-manifest.new.json")
	if err := os.WriteFile(manifestFile, rawManifest, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CSX_UPDATE_MANIFEST_FILE", manifestFile)

	active, err := BootstrapLauncher(context.Background(), root, staged, "", "v1.0.0")
	if err != nil {
		t.Fatalf("BootstrapLauncher failed with local manifest: %v", err)
	}
	if active.Current.Version != "v1.0.0" || active.Current.SHA256 != payloadSHA256 {
		t.Fatalf("unexpected active descriptor: %+v", active.Current)
	}
}

func TestBootstrapLauncherPicksUpStagedManifestWithoutEnvVar(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("LOCALAPPDATA", tempDir)

	root := filepath.Join(tempDir, "csx")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}

	payloadBytes := []byte("fake-csx-binary-content")
	staged := filepath.Join(root, "csx-payload.new.exe")
	if err := os.WriteFile(staged, payloadBytes, 0o700); err != nil {
		t.Fatal(err)
	}

	h := sha256.Sum256(payloadBytes)
	payloadSHA256 := hex.EncodeToString(h[:])

	rawManifest, pub, _ := signedTestManifest(t, func(man *Manifest) {
		man.Version = "v1.0.0"
		man.PublishedAt = time.Now().UTC().Add(-time.Hour)
		man.ExpiresAt = time.Now().UTC().Add(24 * time.Hour)
		man.Assets = []Asset{{
			OS:     "windows",
			Arch:   runtime.GOARCH,
			URL:    "https://github.com/r2cuerdame/CodeSampleX/releases/download/v1.0.0/csx-windows-" + runtime.GOARCH + ".exe",
			Size:   int64(len(payloadBytes)),
			SHA256: payloadSHA256,
		}}
	})

	origPub := PublicKeyBase64
	PublicKeyBase64 = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { PublicKeyBase64 = origPub })

	manifestFile := filepath.Join(root, "csx-manifest.new.json")
	if err := os.WriteFile(manifestFile, rawManifest, 0o600); err != nil {
		t.Fatal(err)
	}

	// Deliberately do NOT set CSX_UPDATE_MANIFEST_FILE or CSX_UPDATE_MANIFEST_URL.
	// BootstrapLauncher must detect csx-manifest.new.json staged in root.
	t.Setenv("CSX_UPDATE_MANIFEST_FILE", "")
	t.Setenv("CSX_UPDATE_MANIFEST_URL", "")

	active, err := BootstrapLauncher(context.Background(), root, staged, "", "v1.0.0")
	if err != nil {
		t.Fatalf("BootstrapLauncher failed to pick up candidate manifest: %v", err)
	}
	if active.Current.Version != "v1.0.0" || active.Current.SHA256 != payloadSHA256 {
		t.Fatalf("unexpected active descriptor: %+v", active.Current)
	}
}

func TestBootstrapLauncherVersionMismatchFailsClosed(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("LOCALAPPDATA", tempDir)

	root := filepath.Join(tempDir, "csx")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}

	payloadBytes := []byte("fake-csx-binary-content")
	staged := filepath.Join(root, "csx-payload.new.exe")
	if err := os.WriteFile(staged, payloadBytes, 0o700); err != nil {
		t.Fatal(err)
	}

	h := sha256.Sum256(payloadBytes)
	payloadSHA256 := hex.EncodeToString(h[:])

	rawManifest, pub, _ := signedTestManifest(t, func(man *Manifest) {
		man.Version = "v1.0.1" // manifest says v1.0.1
		man.PublishedAt = time.Now().UTC().Add(-time.Hour)
		man.ExpiresAt = time.Now().UTC().Add(24 * time.Hour)
		man.Assets = []Asset{{
			OS:     "windows",
			Arch:   runtime.GOARCH,
			URL:    "https://github.com/r2cuerdame/CodeSampleX/releases/download/v1.0.1/csx-windows-" + runtime.GOARCH + ".exe",
			Size:   int64(len(payloadBytes)),
			SHA256: payloadSHA256,
		}}
	})

	origPub := PublicKeyBase64
	PublicKeyBase64 = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { PublicKeyBase64 = origPub })

	manifestFile := filepath.Join(root, "csx-manifest.new.json")
	if err := os.WriteFile(manifestFile, rawManifest, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CSX_UPDATE_MANIFEST_FILE", manifestFile)

	// currentVersion is v1.0.0, but manifest is v1.0.1
	_, err := BootstrapLauncher(context.Background(), root, staged, "", "v1.0.0")
	if err == nil {
		t.Fatal("BootstrapLauncher succeeded despite version mismatch")
	}
	if !strings.Contains(err.Error(), "does not match the signed stable release") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBootstrapLauncherDigestMismatchFailsClosed(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("LOCALAPPDATA", tempDir)

	root := filepath.Join(tempDir, "csx")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}

	payloadBytes := []byte("fake-csx-binary-content")
	staged := filepath.Join(root, "csx-payload.new.exe")
	if err := os.WriteFile(staged, payloadBytes, 0o700); err != nil {
		t.Fatal(err)
	}

	rawManifest, pub, _ := signedTestManifest(t, func(man *Manifest) {
		man.Version = "v1.0.0"
		man.PublishedAt = time.Now().UTC().Add(-time.Hour)
		man.ExpiresAt = time.Now().UTC().Add(24 * time.Hour)
		man.Assets = []Asset{{
			OS:     "windows",
			Arch:   runtime.GOARCH,
			URL:    "https://github.com/r2cuerdame/CodeSampleX/releases/download/v1.0.0/csx-windows-" + runtime.GOARCH + ".exe",
			Size:   int64(len(payloadBytes)),
			SHA256: strings.Repeat("f", 64), // mismatched hash
		}}
	})

	origPub := PublicKeyBase64
	PublicKeyBase64 = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { PublicKeyBase64 = origPub })

	manifestFile := filepath.Join(root, "csx-manifest.new.json")
	if err := os.WriteFile(manifestFile, rawManifest, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CSX_UPDATE_MANIFEST_FILE", manifestFile)

	_, err := BootstrapLauncher(context.Background(), root, staged, "", "v1.0.0")
	if err == nil {
		t.Fatal("BootstrapLauncher succeeded despite digest mismatch")
	}
	if !strings.Contains(err.Error(), "does not match the signed manifest") {
		t.Fatalf("unexpected error: %v", err)
	}
}
