package serverstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// The findings page asks one question — which published samples state a
// belief a contract then measured — and it must ask it of the whole corpus.
// It used to ask it of the newest 2,000 verified samples, so the public count
// fell from 543 to 250 as the corpus grew past that line.
//
// The replacement pages a store-side filter, which puts the answer in two
// places: a PostgreSQL predicate over the manifest JSONB, and the Fake every
// handler test runs against. A test that proves something about the Fake is
// worth nothing if the two disagree, so the same script runs through both and
// the pages are compared line for line.

// beliefStore is the slice of a store this parity check needs.
type beliefStore interface {
	SaveSample(context.Context, SampleRow) error
	SaveReceipt(context.Context, ReceiptRow) error
	SetSampleQuarantine(context.Context, string, bool, string) error
	ListVerifiedBeliefSamples(context.Context, SampleCursor, int) ([]SampleRow, error)
}

// beliefSeed is one sample the script publishes.
type beliefSeed struct {
	id string
	// offset from the script's start; two seeds may share one, because
	// production timestamps tie and the cursor has to survive it.
	at time.Duration
	// believed empty means the manifest declares none.
	believed string
	// verified false means a source-only upload: published, never proved.
	verified    bool
	quarantined bool
}

func beliefScript() []beliefSeed {
	return []beliefSeed{
		// The oldest entries are the ones a recency window drops first.
		{id: "sha256:belief-oldest", at: 0, believed: "axios retries automatically", verified: true},
		{id: "sha256:belief-old-tie-a", at: time.Minute, believed: "ms(604800000) returns 1w", verified: true},
		{id: "sha256:belief-old-tie-b", at: time.Minute, believed: "the parser accepts trailing commas", verified: true},
		{id: "sha256:plain-old", at: 2 * time.Minute, verified: true},
		// Published but never proved: prose on a source-only upload is not a
		// measured finding, in either store.
		{id: "sha256:belief-source-only", at: 3 * time.Minute, believed: "reqwest follows redirects forever"},
		// Taken down. The only thing that may remove a finding.
		{id: "sha256:belief-quarantined", at: 4 * time.Minute, believed: "tar preserves ownership", verified: true, quarantined: true},
		{id: "sha256:plain-new", at: 5 * time.Minute, verified: true},
		{id: "sha256:belief-newest", at: 6 * time.Minute, believed: "the pool closes idle connections", verified: true},
	}
}

func beliefManifest(believed string) string {
	if believed == "" {
		return `{"schemaVersion":1,"packages":["pkg:npm/axios@1.12.0"],` +
			`"case":{"schemaVersion":1,"kind":"HOW","goal":"g",` +
			`"packages":["pkg:npm/axios@1.12.0"],"contract":["it returns"]}}`
	}
	return fmt.Sprintf(`{"schemaVersion":1,"packages":["pkg:npm/axios@1.12.0"],`+
		`"case":{"schemaVersion":1,"kind":"HOW","goal":"g",`+
		`"packages":["pkg:npm/axios@1.12.0"],"believed":%q,"contract":["it returns"]}}`, believed)
}

// seedBeliefScript publishes the script. stamp sets created_at where the
// store cannot take it from the row, which is how PostgreSQL behaves: the
// column has a now() default and SaveSample never sends one.
func seedBeliefScript(t *testing.T, store beliefStore, start time.Time,
	stamp func(id string, at time.Time)) {
	t.Helper()
	ctx := context.Background()
	for _, s := range beliefScript() {
		at := start.Add(s.at)
		if err := store.SaveSample(ctx, SampleRow{
			SampleID: s.id, ManifestJSON: beliefManifest(s.believed),
			Status: "PUBLISHED", CreatedAt: at,
		}); err != nil {
			t.Fatalf("save %s: %v", s.id, err)
		}
		if stamp != nil {
			stamp(s.id, at)
		}
		if s.verified {
			if err := store.SaveReceipt(ctx, ReceiptRow{
				ReceiptID: "receipt-" + s.id, SampleID: s.id,
				ContractResult: "PASS", ReceiptJSON: `{}`,
			}); err != nil {
				t.Fatalf("save receipt %s: %v", s.id, err)
			}
		}
		if s.quarantined {
			if err := store.SetSampleQuarantine(ctx, s.id, true, "takedown"); err != nil {
				t.Fatalf("quarantine %s: %v", s.id, err)
			}
		}
	}
}

// pageBeliefSamples walks the whole eligible set the way the server does and
// returns one line per read, so a difference names the page it happened on.
func pageBeliefSamples(t *testing.T, store beliefStore, perPage int) []string {
	t.Helper()
	ctx := context.Background()
	var out []string
	var cursor SampleCursor
	for page := 1; ; page++ {
		rows, err := store.ListVerifiedBeliefSamples(ctx, cursor, perPage)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, strings.TrimPrefix(r.SampleID, "sha256:"))
		}
		out = append(out, fmt.Sprintf("page %d: %s", page, strings.Join(ids, ",")))
		if len(rows) < perPage {
			return out
		}
		cursor = CursorFor(rows[len(rows)-1])
		if page > 20 {
			t.Fatal("paging did not terminate")
		}
	}
}

func TestBeliefSamplesFakeMatchesPostgres(t *testing.T) {
	start := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)

	fake := NewFake()
	seedBeliefScript(t, fake, start, nil)

	pg := openTestPG(t) // skips when CSX_TEST_DSN is unset
	ctx := context.Background()
	seedBeliefScript(t, pg, start, func(id string, at time.Time) {
		if err := pg.withConn(ctx, func(c *pgx.Conn) error {
			_, err := c.Exec(ctx, `UPDATE samples SET created_at=$2 WHERE sample_id=$1`, id, at)
			return err
		}); err != nil {
			t.Fatalf("stamp %s: %v", id, err)
		}
	})

	// Two rows share a timestamp and the page size splits the tie, which is
	// where a cursor that compares only the clock loses or repeats a row.
	for _, perPage := range []int{1, 2, 3, 10} {
		fakePages := pageBeliefSamples(t, fake, perPage)
		pgPages := pageBeliefSamples(t, pg, perPage)
		if len(fakePages) != len(pgPages) {
			t.Fatalf("perPage=%d: fake read %d pages, postgres read %d\nfake: %v\npg:   %v",
				perPage, len(fakePages), len(pgPages), fakePages, pgPages)
		}
		for i := range fakePages {
			if fakePages[i] != pgPages[i] {
				t.Errorf("perPage=%d:\n fake: %s\n  pg:  %s", perPage, fakePages[i], pgPages[i])
			}
		}
	}

	// And what both of them say is the whole eligible set, once: every
	// verified sample that declares a belief and has not been taken down.
	want := "belief-newest,belief-old-tie-b,belief-old-tie-a,belief-oldest"
	got := strings.TrimPrefix(pageBeliefSamples(t, pg, 10)[0], "page 1: ")
	if got != want {
		t.Errorf("postgres eligible set = %q, want %q", got, want)
	}
}
