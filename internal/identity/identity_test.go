package identity

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

func TestLoadOrCreatePersists(t *testing.T) {
	home := t.TempDir()
	id1, err := LoadOrCreate(home)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	path := filepath.Join(home, "identity.json")
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("identity.json mode = %o, want 600", perm)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("Unmarshal identity.json: %v", err)
	}
	if m["schemaVersion"] != float64(1) {
		t.Errorf("schemaVersion = %v, want 1", m["schemaVersion"])
	}
	for _, key := range []string{"ed25519Priv", "anonSeed"} {
		s, ok := m[key].(string)
		if !ok || s == "" {
			t.Fatalf("identity.json field %q missing or not a string", key)
		}
		if _, err := base64.StdEncoding.DecodeString(s); err != nil {
			t.Errorf("field %q is not base64: %v", key, err)
		}
	}
	seed, _ := base64.StdEncoding.DecodeString(m["anonSeed"].(string))
	if len(seed) != 32 {
		t.Errorf("anonSeed length = %d, want 32", len(seed))
	}

	id2, err := LoadOrCreate(home)
	if err != nil {
		t.Fatalf("LoadOrCreate (second): %v", err)
	}
	if id1.PeerID() != id2.PeerID() {
		t.Errorf("PeerID changed across loads: %q vs %q", id1.PeerID(), id2.PeerID())
	}
	if id1.PubkeyB64() != id2.PubkeyB64() {
		t.Error("PubkeyB64 changed across loads")
	}
	if id1.AnonID("2026-08-13") != id2.AnonID("2026-08-13") {
		t.Error("AnonID changed across loads for same epoch")
	}
}

func TestPeerIDFormat(t *testing.T) {
	id, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := regexp.MatchString(`^ed25519:[0-9a-f]{16}$`, id.PeerID()); !ok {
		t.Errorf("PeerID = %q, want ed25519:<16 hex>", id.PeerID())
	}
}

func TestAnonIDRotation(t *testing.T) {
	id, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := id.AnonID("2026-08-13")
	b := id.AnonID("2026-08-13")
	c := id.AnonID("2026-08-14")
	if a != b {
		t.Errorf("AnonID unstable within epoch: %q vs %q", a, b)
	}
	if a == c {
		t.Errorf("AnonID did not rotate across epochs: %q", a)
	}
	if ok, _ := regexp.MatchString(`^[0-9a-f]{16}$`, a); !ok {
		t.Errorf("AnonID = %q, want 16 hex chars", a)
	}
}

func TestAnonIDDiffersPerIdentity(t *testing.T) {
	id1, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id2, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if id1.AnonID("2026-08-13") == id2.AnonID("2026-08-13") {
		t.Error("distinct identities produced the same AnonID")
	}
}

func TestProjectBucketRotation(t *testing.T) {
	id, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := `C:\work\projA`
	a := id.ProjectBucket(p, "2026-08")
	b := id.ProjectBucket(p, "2026-08")
	c := id.ProjectBucket(p, "2026-09")
	other := id.ProjectBucket(`C:\work\projB`, "2026-08")
	if a != b {
		t.Errorf("ProjectBucket unstable within epoch: %q vs %q", a, b)
	}
	if a == c {
		t.Errorf("ProjectBucket did not rotate across months: %q", a)
	}
	if a == other {
		t.Error("distinct projects produced the same bucket")
	}
	if ok, _ := regexp.MatchString(`^[0-9a-f]{12}$`, a); !ok {
		t.Errorf("ProjectBucket = %q, want 12 hex chars", a)
	}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	id, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte(`{"schemaVersion":1,"sampleId":"sha256:abc"}`)
	sig := id.Sign(msg)
	if _, err := base64.StdEncoding.DecodeString(sig); err != nil {
		t.Fatalf("signature is not base64: %v", err)
	}
	if !Verify(id.PubkeyB64(), sig, msg) {
		t.Fatal("Verify failed for a valid signature")
	}
}

func TestVerifyRejectsTamper(t *testing.T) {
	id, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("original message")
	sig := id.Sign(msg)
	if Verify(id.PubkeyB64(), sig, []byte("tampered message")) {
		t.Error("Verify accepted a tampered message")
	}
	other, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if Verify(other.PubkeyB64(), sig, msg) {
		t.Error("Verify accepted a signature under the wrong pubkey")
	}
	if Verify(id.PubkeyB64(), "!!!notbase64", msg) {
		t.Error("Verify accepted malformed base64 signature")
	}
	if Verify("!!!notbase64", sig, msg) {
		t.Error("Verify accepted malformed base64 pubkey")
	}
	if Verify(base64.StdEncoding.EncodeToString([]byte("short")), sig, msg) {
		t.Error("Verify accepted a wrong-length pubkey")
	}
}

func TestLoadCorruptIdentityErrors(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "identity.json"), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(home); err == nil {
		t.Fatal("LoadOrCreate on corrupt file: want error, got nil")
	}
}
