package compatibility

import (
	"context"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// confirmingProbeStore is the production store with the registry check
// landing in the one place it can hurt: after the builder has asked which
// releases exist and before it writes the ones it believes are absent.
type confirmingProbeStore struct {
	*serverstore.PG
	confirm func()
}

func (s *confirmingProbeStore) ExistingPackagePURLs(ctx context.Context, purls []string) (map[string]bool, error) {
	known, err := s.PG.ExistingPackagePURLs(ctx, purls)
	if err == nil {
		s.confirm()
	}
	return known, err
}

// The builder registers receipt-only releases as UNKNOWN. It decides which
// ones from a membership probe and writes a page later; if the registry
// check confirms one of them PUBLIC in between, the write must not undo
// the confirmation. Through UpsertPackage's conflict rule it did: the row
// went back to UNKNOWN with no checked_at, and the registry would never
// look at it again (#174 review). This plays the interleaving for real
// against PostgreSQL.
func TestBuilderRegistrationKeepsAConcurrentlyConfirmedReleasePublic(t *testing.T) {
	pg, _ := openBuilderTestPG(t)
	ctx := context.Background()
	p, err := domain.ParsePURL("pkg:npm/confirmed-meanwhile@1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	checked := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	store := &confirmingProbeStore{PG: pg, confirm: func() {
		if err := pg.UpsertPackage(ctx, serverstore.PackageRow{
			PURL: p.String(), Ecosystem: p.Ecosystem, Name: p.Name, Version: p.Version,
			Major: p.Major(), Publicness: "PUBLIC", CheckedAt: checked,
		}); err != nil {
			t.Fatal(err)
		}
	}}
	samples := []sampleData{{
		row:      serverstore.SampleRow{SampleID: "sha256:confirmed-meanwhile"},
		receipts: []ReceiptInfo{{ResolvedPackages: []domain.PURL{p}}},
	}}
	if err := (&Builder{Store: store}).ensureReceiptPackages(ctx, samples); err != nil {
		t.Fatalf("ensureReceiptPackages: %v", err)
	}

	got, ok, err := pg.GetPackage(ctx, p.String())
	if err != nil || !ok {
		t.Fatalf("GetPackage ok=%t err=%v", ok, err)
	}
	if got.Publicness != "PUBLIC" || !got.CheckedAt.Equal(checked) {
		t.Fatalf("registration downgraded the confirmed release: %+v", got)
	}
}
