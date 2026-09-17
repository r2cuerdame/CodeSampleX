package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// failingSnapshotStore refuses only snapshot reads, so the failure lands on
// the package endpoint's snapshot summary and on nothing before it.
type failingSnapshotStore struct {
	serverstore.Store
	err error
}

func (s *failingSnapshotStore) GetSnapshot(context.Context, string, string) (string, bool, error) {
	return "", false, s.err
}

func seedRegistryPackage(t *testing.T, store *serverstore.Fake, snapshot string) {
	t.Helper()
	ctx := context.Background()
	if err := store.UpsertPackage(ctx, serverstore.PackageRow{
		PURL: "pkg:npm/axios@1.12.0", Ecosystem: "npm", Name: "axios", Version: "1.12.0",
		Major: "1", Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
	}
	if snapshot != "" {
		if err := store.PutSnapshot(ctx, "pkg:npm/axios@1.12.0", "", snapshot); err != nil {
			t.Fatal(err)
		}
	}
}

// GET /v1/registry/packages/{purl} answered 200 with a null snapshotSummary
// whenever the snapshot read failed, so a busy pool read exactly like a
// package that has no evidence yet -- and a client would cache that. The
// symbol endpoint already reports the store's refusal for what it is; the
// package endpoint must tell the same truth.
func TestRegistryPackageSnapshotFailureIsNotANullSummary(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		status     int
		want       string
		retryAfter string
	}{
		{name: "pool busy", err: serverstore.ErrPoolBusy, status: http.StatusServiceUnavailable, want: "database busy", retryAfter: "2"},
		{name: "other failure", err: errors.New("snapshot table gone"), status: http.StatusInternalServerError, want: "snapshot lookup failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, store, _ := newTestServer(t, func(d *Deps) {
				d.Store = &failingSnapshotStore{Store: d.Store, err: tc.err}
			})
			seedRegistryPackage(t, store, `{"purl":"pkg:npm/axios@1.12.0","symbol":"","rows":[]}`)

			var out map[string]any
			resp := getJSON(t, srv.URL+"/v1/registry/packages/pkg:npm%2Faxios@1.12.0", &out)
			if resp.StatusCode != tc.status || out["error"] != tc.want {
				t.Fatalf("status = %d body = %v, want %d %q", resp.StatusCode, out, tc.status, tc.want)
			}
			if _, present := out["snapshotSummary"]; present {
				t.Fatalf("a refused read still produced a package document: %v", out)
			}
			if got := resp.Header.Get("Retry-After"); got != tc.retryAfter {
				t.Fatalf("Retry-After = %q, want %q", got, tc.retryAfter)
			}
		})
	}
}

// symbols comes from the package_symbols read model (CSX-452), not from
// recomputing corpus-wide receipt attribution on this request. A purl the
// Builder has not published a row for yet answers an empty list, not an
// error -- the same "nothing materialized yet" contract GetSnapshot already
// has for the snapshot summary.
func TestRegistryPackageSymbolsComeFromThePackageSymbolsReadModel(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	seedRegistryPackage(t, store, "")
	if err := store.PutPackageSymbols(context.Background(), []serverstore.PackageSymbolsRow{
		{PURL: "pkg:npm/axios@1.12.0", Symbols: []string{"axios.post", "axios.get"}},
	}); err != nil {
		t.Fatal(err)
	}

	var out struct {
		Symbols     []string   `json:"symbols"`
		GeneratedAt *time.Time `json:"generatedAt"`
	}
	resp := getJSON(t, srv.URL+"/v1/registry/packages/pkg:npm%2Faxios@1.12.0", &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	want := []string{"axios.post", "axios.get"}
	if len(out.Symbols) != len(want) || out.Symbols[0] != want[0] || out.Symbols[1] != want[1] {
		t.Fatalf("symbols = %v, want %v", out.Symbols, want)
	}
	// generatedAt is the freshness contract (CSX-452): it must match what
	// PutPackageSymbols wrote (the fake store's clock, testNow).
	if out.GeneratedAt == nil || !out.GeneratedAt.Equal(testNow) {
		t.Fatalf("generatedAt = %v, want %v", out.GeneratedAt, testNow)
	}
}

func TestRegistryPackageSymbolsAreEmptyBeforeAnyBuilderPass(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	seedRegistryPackage(t, store, "")

	var out struct {
		Symbols     []string   `json:"symbols"`
		GeneratedAt *time.Time `json:"generatedAt"`
	}
	resp := getJSON(t, srv.URL+"/v1/registry/packages/pkg:npm%2Faxios@1.12.0", &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(out.Symbols) != 0 {
		t.Fatalf("symbols = %v, want empty before any package_symbols row exists", out.Symbols)
	}
	// A purl the Builder has never published for answers found=false from
	// GetPackageSymbols -- generatedAt is absent from the response, not a
	// zero-valued timestamp standing in for "unknown".
	if out.GeneratedAt != nil {
		t.Fatalf("generatedAt = %v, want absent before any package_symbols row exists", *out.GeneratedAt)
	}
}

// The healthy answer is unchanged: the package-level snapshot is the
// summary, and a package without one still answers 200 with null.
func TestRegistryPackageSnapshotSummaryIsThePackageLevelSnapshot(t *testing.T) {
	for _, snapshot := range []string{`{"purl":"pkg:npm/axios@1.12.0","symbol":"","rows":[{"i":1}]}`, ""} {
		srv, store, _ := newTestServer(t, nil)
		seedRegistryPackage(t, store, snapshot)

		var out struct {
			Publicness      string         `json:"publicness"`
			SnapshotSummary map[string]any `json:"snapshotSummary"`
		}
		resp := getJSON(t, srv.URL+"/v1/registry/packages/pkg:npm%2Faxios@1.12.0", &out)
		if resp.StatusCode != http.StatusOK || out.Publicness != "PUBLIC" {
			t.Fatalf("status = %d publicness = %q, want 200 PUBLIC", resp.StatusCode, out.Publicness)
		}
		if (snapshot == "") != (out.SnapshotSummary == nil) {
			t.Fatalf("snapshot %q: summary = %v", snapshot, out.SnapshotSummary)
		}
		if snapshot != "" && out.SnapshotSummary["symbol"] != "" {
			t.Fatalf("summary is not the package-level snapshot: %v", out.SnapshotSummary)
		}
	}
}
