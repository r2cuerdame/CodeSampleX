package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func signedTestManifest(t *testing.T, mutate func(*Manifest)) ([]byte, ed25519.PublicKey, Manifest) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	m := Manifest{
		Schema: 1, Channel: "stable", Sequence: 12, Version: "v1.2.3",
		PublishedAt: now.Add(-time.Hour), ExpiresAt: now.Add(24 * time.Hour),
		Assets: []Asset{{OS: "windows", Arch: "amd64", Size: 3,
			URL:    "https://github.com/r2cuerdame/CodeSampleX/releases/download/v1.2.3/csx-windows-amd64.exe",
			SHA256: strings.Repeat("a", 64)}},
	}
	if mutate != nil {
		mutate(&m)
	}
	payload, _ := json.Marshal(m)
	env, _ := json.Marshal(Envelope{Payload: base64.StdEncoding.EncodeToString(payload), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))})
	return env, pub, m
}

func TestVerifyEnvelopeAuthenticatesStrictSignedPayload(t *testing.T) {
	raw, pub, want := signedTestManifest(t, nil)
	got, err := VerifyEnvelope(raw, pub, time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC), "stable")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != want.Version || got.Sequence != want.Sequence {
		t.Fatalf("got %+v want %+v", got, want)
	}

	var env Envelope
	_ = json.Unmarshal(raw, &env)
	payload, _ := base64.StdEncoding.DecodeString(env.Payload)
	payload[len(payload)-2] ^= 1
	env.Payload = base64.StdEncoding.EncodeToString(payload)
	tampered, _ := json.Marshal(env)
	if _, err := VerifyEnvelope(tampered, pub, time.Now(), "stable"); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("tampered payload error = %v", err)
	}
}

func TestVerifyEnvelopeFailsClosed(t *testing.T) {
	now := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*Manifest){
		"expired":              func(m *Manifest) { m.ExpiresAt = now.Add(-time.Second) },
		"future":               func(m *Manifest) { m.PublishedAt = now.Add(time.Hour); m.ExpiresAt = now.Add(2 * time.Hour) },
		"overlong validity":    func(m *Manifest) { m.ExpiresAt = m.PublishedAt.Add(90*24*time.Hour + time.Second) },
		"noncanonical minimum": func(m *Manifest) { m.MinUpdaterVersion = "v1.2" },
		"prerelease":           func(m *Manifest) { m.Version = "v1.2.3-rc.1" },
		"duplicate target":     func(m *Manifest) { m.Assets = append(m.Assets, m.Assets[0]) },
		"wrong filename": func(m *Manifest) {
			m.Assets[0].URL = "https://github.com/r2cuerdame/CodeSampleX/releases/download/v1.2.3/other.exe"
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw, pub, _ := signedTestManifest(t, mutate)
			if _, err := VerifyEnvelope(raw, pub, now, "stable"); err == nil {
				t.Fatal("invalid signed manifest was accepted")
			}
		})
	}
}

func TestVerifyEnvelopeRejectsUnknownAndOversizeFields(t *testing.T) {
	raw, pub, _ := signedTestManifest(t, nil)
	var env map[string]any
	_ = json.Unmarshal(raw, &env)
	env["unexpected"] = true
	unknown, _ := json.Marshal(env)
	if _, err := VerifyEnvelope(unknown, pub, time.Now(), "stable"); err == nil {
		t.Fatal("unknown envelope field accepted")
	}

	oversize := []byte(`{"payload":"` + strings.Repeat("A", base64.StdEncoding.EncodedLen(maxSignedPayloadBytes)+4) + `","signature":"AA=="}`)
	if _, err := VerifyEnvelope(oversize, pub, time.Now(), "stable"); err == nil {
		t.Fatal("oversize payload accepted")
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{{"v1.2.0", "1.1.9", 1}, {"1.2", "1.2.0", 0}, {"1.2.3", "1.3", -1}} {
		got, err := CompareVersions(tc.a, tc.b)
		if err != nil || got != tc.want {
			t.Errorf("CompareVersions(%q,%q)=%d,%v want %d", tc.a, tc.b, got, err, tc.want)
		}
	}
	if _, err := CompareVersions("dev (git)", "1.0.0"); err == nil {
		t.Fatal("dev version accepted")
	}
}
