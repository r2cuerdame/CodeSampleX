package main

import (
	"crypto/ed25519"
	"testing"
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
