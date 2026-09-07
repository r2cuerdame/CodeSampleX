package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	csxupdate "github.com/r2cuerdame/codesamplex/internal/update"
)

func TestParseSigningKeyRejectsCorruptedPrivateSuffix(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	key := ed25519.NewKeyFromSeed(seed)
	key[len(key)-1] ^= 1
	if _, err := parseSigningKey(key); err == nil {
		t.Fatal("corrupted 64-byte private key accepted")
	}
	if _, err := parseSigningKey(seed); err != nil {
		t.Fatalf("valid seed rejected: %v", err)
	}
}

func TestReleaseDirectoryBindsEverySignedPayloadAndLauncher(t *testing.T) {
	for _, target := range []string{"valid", "csx-windows-amd64.exe", "csx-launcher-windows-amd64.exe", "csx-linux-arm64", "sequence", "missing bootstrap"} {
		t.Run(target, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range []string{"csx-darwin-amd64", "csx-darwin-arm64", "csx-linux-amd64", "csx-linux-arm64", "csx-windows-amd64.exe", "csx-windows-arm64.exe", "csx-launcher-windows-amd64.exe", "csx-launcher-windows-arm64.exe"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			pub, key, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			m := csxupdate.Manifest{Schema: 1, Channel: "stable", Version: "v1.2.3", Sequence: 42, PublishedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Assets: assets(dir, "v1.2.3")}
			stablePath := filepath.Join(dir, "csx-update-stable.json")
			if err := writeManifest(stablePath, m, key, now); err != nil {
				t.Fatal(err)
			}
			// Legacy updater clients reject unknown fields. This envelope must
			// remain free of launcher metadata even when bootstrap signs it.
			for _, a := range m.Assets {
				if a.LauncherURL != "" {
					t.Fatal("legacy stable schema changed")
				}
			}
			for i, a := range m.Assets {
				if a.OS != "windows" {
					continue
				}
				name := "csx-launcher-windows-" + a.Arch + ".exe"
				sum := sha256.Sum256([]byte(name))
				m.Assets[i].LauncherURL = "https://github.com/r2cuerdame/CodeSampleX/releases/download/v1.2.3/" + name
				m.Assets[i].LauncherSize, m.Assets[i].LauncherSHA256 = int64(len(name)), hex.EncodeToString(sum[:])
			}
			if target == "sequence" {
				m.Sequence++
			}
			if target != "missing bootstrap" {
				if err := writeManifest(filepath.Join(dir, "csx-bootstrap-stable.json"), m, key, now); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(target, "csx-") {
				if err := os.WriteFile(filepath.Join(dir, target), []byte("different release bytes"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err = verifyReleaseDirectory(dir, "v1.2.3", pub, now)
			if (err == nil) != (target == "valid") {
				t.Fatalf("verify release: %v", err)
			}
		})
	}
}

func TestGuardReleaseOrder(t *testing.T) {
	for _, tc := range []struct {
		candidate string
		latest    string
		wantErr   bool
	}{
		{"v1.2.4", "v1.2.3", false},
		{"v1.2.3", "v1.2.3", false},
		{"v1.2.2", "v1.2.3", true},
		{"v1.2", "v1.1.9", true},
		{"v1.2.3-rc.1", "v1.2.2", true},
		{"v01.2.3", "v1.2.2", true},
	} {
		if err := guardRelease(tc.candidate, tc.latest); (err != nil) != tc.wantErr {
			t.Errorf("guardRelease(%q,%q) error=%v wantErr=%t", tc.candidate, tc.latest, err, tc.wantErr)
		}
	}
}
