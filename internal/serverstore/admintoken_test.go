package serverstore

import (
	"context"
	"testing"
	"time"
)

func adminRow(id, hash string, issued time.Time, expires time.Time) AdminTokenRow {
	return AdminTokenRow{TokenHash: hash, TokenID: id, Label: id, IssuedAt: issued, ExpiresAt: expires}
}

// A token with an expiry stops working at it; a token issued without one keeps
// working, because a farm's credential has to outlive any session. Both halves
// matter: the unlimited option is the reason this table exists, and an expiry
// that silently did nothing would be worse than not offering one.
func TestAdminTokenHonoursExpiryAndUnlimited(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	issued := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	if err := store.IssueAdminTokens(ctx, []AdminTokenRow{
		adminRow("bounded", "hash-bounded", issued, issued.Add(24*time.Hour)),
		adminRow("forever", "hash-forever", issued, time.Time{}),
	}); err != nil {
		t.Fatal(err)
	}

	within := issued.Add(time.Hour)
	if _, ok, err := store.ResolveAdminToken(ctx, "hash-bounded", "10.0.0.1", within); err != nil || !ok {
		t.Errorf("bounded token inside its window: ok=%v err=%v", ok, err)
	}
	past := issued.Add(48 * time.Hour)
	if _, ok, err := store.ResolveAdminToken(ctx, "hash-bounded", "10.0.0.1", past); err != nil || ok {
		t.Errorf("bounded token after expiry: ok=%v err=%v, want ok=false", ok, err)
	}
	distant := issued.Add(5 * 365 * 24 * time.Hour)
	if _, ok, err := store.ResolveAdminToken(ctx, "hash-forever", "10.0.0.1", distant); err != nil || !ok {
		t.Errorf("unlimited token five years on: ok=%v err=%v, want ok=true", ok, err)
	}
}

// A token that cannot expire is only observable through its use, so resolving
// records when and from where. Without this an operator has no way to notice
// a permanent credential is being used by somebody else.
func TestAdminTokenRecordsEveryUse(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	issued := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	if err := store.IssueAdminTokens(ctx, []AdminTokenRow{adminRow("farm", "hash-farm", issued, time.Time{})}); err != nil {
		t.Fatal(err)
	}

	used := issued.Add(90 * time.Minute)
	if _, ok, err := store.ResolveAdminToken(ctx, "hash-farm", "203.0.113.7", used); err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	rows, err := store.ListAdminTokens(ctx, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list = %+v err=%v", rows, err)
	}
	if !rows[0].LastUsedAt.Equal(used) {
		t.Errorf("lastUsedAt = %s, want %s", rows[0].LastUsedAt, used)
	}
	if rows[0].LastUsedIP != "203.0.113.7" {
		t.Errorf("lastUsedIP = %q, want 203.0.113.7", rows[0].LastUsedIP)
	}
}

// Revoking is the only way to stop an unlimited token, so it has to be exact:
// the named one dies and its neighbours keep working.
func TestAdminTokenRevokeIsIndividual(t *testing.T) {
	store := NewFake()
	ctx := context.Background()
	issued := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	if err := store.IssueAdminTokens(ctx, []AdminTokenRow{
		adminRow("one", "hash-one", issued, time.Time{}),
		adminRow("two", "hash-two", issued, time.Time{}),
	}); err != nil {
		t.Fatal(err)
	}
	now := issued.Add(time.Hour)
	if revoked, err := store.RevokeAdminToken(ctx, "one", now); err != nil || !revoked {
		t.Fatalf("revoke = %v err=%v", revoked, err)
	}
	if _, ok, _ := store.ResolveAdminToken(ctx, "hash-one", "10.0.0.1", now); ok {
		t.Error("revoked token still resolves")
	}
	if _, ok, _ := store.ResolveAdminToken(ctx, "hash-two", "10.0.0.1", now); !ok {
		t.Error("revoking one token killed another")
	}
	if revoked, err := store.RevokeAdminToken(ctx, "missing", now); err != nil || revoked {
		t.Errorf("revoking an unknown id = %v err=%v, want false", revoked, err)
	}
}

// An unknown digest must not resolve, which is the whole point of storing only
// digests.
func TestAdminTokenRejectsUnknownDigest(t *testing.T) {
	store := NewFake()
	if _, ok, err := store.ResolveAdminToken(context.Background(), "hash-nobody-issued", "10.0.0.1", time.Now()); err != nil || ok {
		t.Errorf("unknown digest: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}
