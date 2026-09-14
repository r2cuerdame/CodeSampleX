package identity

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"time"
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

func TestPresenceTokensDeterminismAndRotation(t *testing.T) {
	id, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// 2026-09-12 12:00:00 UTC: unixDay = 1789171200 / 86400 = 20708
	// 20708 / 7 = 2958
	// 20708 / 30 = 690
	t1 := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	// Same day later in the day:
	t1Later := time.Date(2026, 9, 12, 23, 59, 59, 0, time.UTC)
	// Next day: 2026-09-13: unixDay = 20709. 20709 / 7 = 2958. 20709 / 30 = 690.
	t2 := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	// Next 7d epoch boundary: unixDay = 2959 * 7 = 20713 (2026-09-17)
	tNext7d := time.Unix(20713*86400+10, 0).UTC()
	// Next 30d epoch boundary: unixDay = 691 * 30 = 20730 (2026-10-04)
	tNext30d := time.Unix(20730*86400+10, 0).UTC()

	tok1 := id.PresenceTokens(t1)
	tok1Later := id.PresenceTokens(t1Later)
	tok2 := id.PresenceTokens(t2)
	tokNext7d := id.PresenceTokens(tNext7d)
	tokNext30d := id.PresenceTokens(tNext30d)

	// Determinism within the same epoch
	if tok1 != tok1Later {
		t.Errorf("PresenceTokens unstable within same day: %+v vs %+v", tok1, tok1Later)
	}

	// 1d rotation across UTC midnight
	if tok1.Epoch1d == tok2.Epoch1d {
		t.Errorf("1d epoch failed to advance across midnight: %q", tok1.Epoch1d)
	}
	if tok1.Token1d == tok2.Token1d {
		t.Errorf("1d token failed to rotate across midnight: %q", tok1.Token1d)
	}

	// 7d stability inside the 7-day block, rotation across boundary
	if tok1.Epoch7d != tok2.Epoch7d || tok1.Token7d != tok2.Token7d {
		t.Errorf("7d token unexpectedly rotated across 1 day: %q vs %q", tok1.Token7d, tok2.Token7d)
	}
	if tok1.Epoch7d == tokNext7d.Epoch7d || tok1.Token7d == tokNext7d.Token7d {
		t.Errorf("7d token failed to rotate across 7d boundary: %q vs %q", tok1.Token7d, tokNext7d.Token7d)
	}

	// 30d stability inside the 30-day block, rotation across boundary
	if tok1.Epoch30d != tok2.Epoch30d || tok1.Token30d != tok2.Token30d {
		t.Errorf("30d token unexpectedly rotated across 1 day: %q vs %q", tok1.Token30d, tok2.Token30d)
	}
	if tok1.Epoch30d == tokNext30d.Epoch30d || tok1.Token30d == tokNext30d.Token30d {
		t.Errorf("30d token failed to rotate across 30d boundary: %q vs %q", tok1.Token30d, tokNext30d.Token30d)
	}

	// Token format: 32 hex chars
	hexRegex := regexp.MustCompile(`^[0-9a-f]{32}$`)
	for _, tok := range []string{tok1.Token1d, tok1.Token7d, tok1.Token30d} {
		if !hexRegex.MatchString(tok) {
			t.Errorf("token %q does not match 32 hex format", tok)
		}
	}
}

func TestPresenceTokensDistinctPerIdentity(t *testing.T) {
	id1, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id2, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	tok1 := id1.PresenceTokens(now)
	tok2 := id2.PresenceTokens(now)

	if tok1.Token1d == tok2.Token1d {
		t.Errorf("two distinct identities produced same Token1d: %q", tok1.Token1d)
	}
	if tok1.Token7d == tok2.Token7d {
		t.Errorf("two distinct identities produced same Token7d: %q", tok1.Token7d)
	}
	if tok1.Token30d == tok2.Token30d {
		t.Errorf("two distinct identities produced same Token30d: %q", tok1.Token30d)
	}
}

func TestPresenceTokensDomainSeparation(t *testing.T) {
	id, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Even if epoch string happened to be identical, domain separation must ensure tokens differ
	sameEpoch := "2026-09-12"
	t1 := id.PresenceToken1d(sameEpoch)
	t7 := id.PresenceToken7d(sameEpoch)
	t30 := id.PresenceToken30d(sameEpoch)

	if t1 == t7 || t1 == t30 || t7 == t30 {
		t.Errorf("presence tokens lack domain separation: 1d=%q 7d=%q 30d=%q", t1, t7, t30)
	}
}

func TestPresenceTokensNoSeedExposure(t *testing.T) {
	home := t.TempDir()
	id, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	// Read raw anonSeed from identity.json
	raw, err := os.ReadFile(filepath.Join(home, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f identityFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	rawSeed, err := base64.StdEncoding.DecodeString(f.AnonSeed)
	if err != nil {
		t.Fatal(err)
	}
	hexSeed := hex.EncodeToString(rawSeed)

	now := time.Now().UTC()
	toks := id.PresenceTokens(now)

	// Ensure seed is nowhere in tokens
	for _, tok := range []string{toks.Token1d, toks.Token7d, toks.Token30d} {
		if bytes.Contains([]byte(tok), []byte(hexSeed)) || bytes.Contains([]byte(hexSeed), []byte(tok)) {
			t.Fatalf("token %q exposed raw anonSeed %q", tok, hexSeed)
		}
		if tok == f.AnonSeed || tok == hexSeed {
			t.Fatalf("token equals seed")
		}
	}
}
