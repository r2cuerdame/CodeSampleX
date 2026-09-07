package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func bootstrapPair(t *testing.T) (Manifest, Manifest, ed25519.PrivateKey, time.Time) {
	t.Helper()
	_, _, stable := signedTestManifest(t, nil)
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := stable
	bootstrap.Assets = append([]Asset(nil), stable.Assets...)
	bootstrap.Assets[0].LauncherURL = "https://github.com/r2cuerdame/CodeSampleX/releases/download/v1.2.3/csx-launcher-windows-amd64.exe"
	bootstrap.Assets[0].LauncherSize = 4
	bootstrap.Assets[0].LauncherSHA256 = strings.Repeat("b", 64)
	return stable, bootstrap, key, stable.PublishedAt.Add(time.Hour)
}

func signBootstrapFixture(t *testing.T, m Manifest, key ed25519.PrivateKey) []byte {
	t.Helper()
	payload, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(Envelope{Payload: base64.StdEncoding.EncodeToString(payload), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, payload))})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBootstrapReleaseRejectsMixedSignedIdentities(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"sequence", func(m *Manifest) { m.Sequence++ }},
		{"timestamp", func(m *Manifest) { m.PublishedAt = m.PublishedAt.Add(time.Second) }},
		{"payload hash", func(m *Manifest) { m.Assets[0].SHA256 = strings.Repeat("c", 64) }},
		{"payload size", func(m *Manifest) { m.Assets[0].Size++ }},
		{"missing launcher", func(m *Manifest) {
			m.Assets[0].LauncherURL = ""
			m.Assets[0].LauncherSize = 0
			m.Assets[0].LauncherSHA256 = ""
		}},
		{"different release URL", func(m *Manifest) {
			m.Assets[0].LauncherURL = strings.ReplaceAll(m.Assets[0].LauncherURL, "v1.2.3", "v1.2.4")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stable, bootstrap, key, now := bootstrapPair(t)
			tc.mutate(&bootstrap)
			if _, err := VerifyBootstrapRelease(signBootstrapFixture(t, stable, key), signBootstrapFixture(t, bootstrap, key), key.Public().(ed25519.PublicKey), now, stable.Version); err == nil {
				t.Fatal("mixed signed release accepted")
			}
		})
	}
}

func TestBootstrapReleaseSnapshotDoesNotDependOnLatest(t *testing.T) {
	stable, bootstrap, key, now := bootstrapPair(t)
	pub := key.Public().(ed25519.PublicKey)
	stableRaw, bootstrapRaw := signBootstrapFixture(t, stable, key), signBootstrapFixture(t, bootstrap, key)
	got, err := VerifyBootstrapRelease(stableRaw, bootstrapRaw, pub, now, stable.Version)
	if err != nil || got.Sequence != stable.Sequence {
		t.Fatalf("server-pinned signed snapshot rejected: %+v, %v", got, err)
	}
	if _, err := VerifyBootstrapRelease(stableRaw, bootstrapRaw, pub, now, "v1.2.4"); err == nil {
		t.Fatal("different running payload version accepted")
	}
	bootstrapRaw[len(bootstrapRaw)-5] ^= 1
	if _, err := VerifyBootstrapRelease(stableRaw, bootstrapRaw, pub, now, stable.Version); err == nil {
		t.Fatal("tampered bootstrap signature accepted")
	}
}
