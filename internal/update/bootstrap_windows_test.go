//go:build windows

package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	csxlauncher "github.com/r2cuerdame/codesamplex/internal/launcher"
)

func TestBootstrapRejectsPayloadOrLauncherBeforeChangingPointer(t *testing.T) {
	for _, target := range []string{"payload", "launcher", "version", "installed newer", "corrupt pointer", "launcher selftest"} {
		t.Run(target, func(t *testing.T) {
			local := t.TempDir()
			t.Setenv("LOCALAPPDATA", local)
			root := filepath.Join(local, "csx")
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			stable, bootstrap, key, _ := bootstrapPair(t)
			now := time.Now().UTC()
			stable.PublishedAt, stable.ExpiresAt = now.Add(-time.Hour), now.Add(time.Hour)
			stable.Assets[0].Arch = runtime.GOARCH
			stable.Assets[0].URL = strings.ReplaceAll(stable.Assets[0].URL, "amd64", runtime.GOARCH)
			payload, launcher := []byte(fixtureStagedPayload), []byte(fixtureStableLauncher)
			sum := sha256.Sum256(payload)
			stable.Assets[0].Size, stable.Assets[0].SHA256 = int64(len(payload)), hex.EncodeToString(sum[:])
			bootstrap.PublishedAt, bootstrap.ExpiresAt = stable.PublishedAt, stable.ExpiresAt
			bootstrap.Assets[0] = stable.Assets[0]
			bootstrap.Assets[0].LauncherURL = strings.ReplaceAll(stable.Assets[0].URL, "/csx-windows-", "/csx-launcher-windows-")
			sum = sha256.Sum256(launcher)
			bootstrap.Assets[0].LauncherSize, bootstrap.Assets[0].LauncherSHA256 = int64(len(launcher)), hex.EncodeToString(sum[:])
			oldKey := PublicKeyBase64
			PublicKeyBase64 = base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
			t.Cleanup(func() { PublicKeyBase64 = oldKey })
			if target == "payload" {
				payload[0] ^= 1
			}
			if target == "launcher" {
				launcher[0] ^= 1
			}
			for name, raw := range map[string][]byte{
				"csx-payload.new.exe": payload, "csx-launcher.new.exe": launcher,
				"csx-manifest.new.json":  signBootstrapFixture(t, stable, key),
				"csx-bootstrap.new.json": signBootstrapFixture(t, bootstrap, key),
			} {
				if err := os.WriteFile(filepath.Join(root, name), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			version := stable.Version
			if target == "version" {
				version = "v1.2.4"
			}
			if target == "installed newer" {
				oldPayload, err := csxlauncher.PayloadPath(root, "v1.2.4")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(oldPayload), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(oldPayload, payload, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := csxlauncher.Write(root, csxlauncher.Active{Schema: csxlauncher.Schema, Current: csxlauncher.Descriptor{Version: "v1.2.4", SHA256: stable.Assets[0].SHA256, Sequence: stable.Sequence + 1}}); err != nil {
					t.Fatal(err)
				}
			}
			if target == "corrupt pointer" {
				if err := os.WriteFile(filepath.Join(root, "active.json"), []byte("invalid pointer"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := BootstrapLauncher(context.Background(), root, filepath.Join(root, "csx-payload.new.exe"), "", version)
			if err == nil {
				t.Fatal("mixed installer promoted")
			}
			wantError := map[string]string{"launcher selftest": "signed installer launcher self-test failed", "installed newer": "refused to downgrade", "corrupt pointer": "existing launcher state is unreadable"}[target]
			if wantError != "" && !strings.Contains(err.Error(), wantError) {
				t.Fatalf("bootstrap failed for the wrong reason: %v; want %q", err, wantError)
			}
			if target == "installed newer" {
				a, err := csxlauncher.Read(root)
				if err != nil || a.Current.Version != "v1.2.4" || a.Current.Sequence != stable.Sequence+1 {
					t.Fatalf("installed pointer changed: %+v, %v", a, err)
				}
				return
			}
			if target == "corrupt pointer" {
				raw, err := os.ReadFile(filepath.Join(root, "active.json"))
				if err != nil || string(raw) != "invalid pointer" {
					t.Fatalf("corrupted pointer overwritten: %q, %v", raw, err)
				}
				return
			}
			if _, err := os.Stat(filepath.Join(root, "active.json")); !os.IsNotExist(err) {
				t.Fatalf("failed bootstrap changed active pointer: %v", err)
			}
		})
	}
}
