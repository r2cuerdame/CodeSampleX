package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

// The ingest endpoint answers 202 with {accepted, rejected:[{index,
// reason}]} — a partial success is the NORMAL reply, not an error case.
// The client treated every 2xx as total success, and the rows had already
// been marked uploaded before the POST, so a refused batch was gone: not
// retried, not reported, and counted as delivered in the number the user
// reads.
//
// The reason the server took the trouble to send is the only thing that
// could have told anyone why.
func TestRefusedBatchesAreCountedAndReported(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Batches []json.RawMessage `json:"batches"`
		}
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusAccepted)
		// Everything refused, with a reason retrying cannot fix.
		var parts []string
		for i := range body.Batches {
			parts = append(parts, fmt.Sprintf(`{"index":%d,"reason":"unknown package"}`, i))
		}
		fmt.Fprintf(w, `{"accepted":0,"rejected":[%s]}`, strings.Join(parts, ","))
	}))
	defer srv.Close()

	cfg := communityCfg(srv.URL)
	rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}
	b := &Batcher{DB: db, Ident: ident, Cfg: cfg}
	if err := rec.RecordRun(ctx, t.TempDir(), fakeScanResult(), knownProfile(), 0, ""); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	n, err := b.Upload(ctx, srv.Client(), srv.URL)
	if n != 0 {
		t.Errorf("reported %d batches uploaded, want 0 — the server accepted none", n)
	}
	if err == nil {
		t.Fatal("a fully refused upload reported no problem at all")
	}
	if !strings.Contains(err.Error(), "unknown package") {
		t.Errorf("the server's reason never reached the user: %v", err)
	}
}

// When an ingest upload chunk (up to 500 batches) produces many rejections,
// the server's 202 response body ({accepted, rejected:[...]}) can span tens
// of kilobytes. If the response reader truncates the body at a small limit,
// JSON decoding fails and the fallback treats the chunk as wholly accepted,
// falsely reporting refused evidence as uploaded and silently dropping the
// server's rejection reasons.
func TestPartialRefusalAckExceedingBufferPreservesRejectionsAndCounts(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Batches []json.RawMessage `json:"batches"`
		}
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusAccepted)

		// Accept 20 batches, reject the rest (e.g. 480 batches).
		// With ~60 bytes per rejection entry, 480 rejections span ~28 KB.
		acceptedCount := 20
		var rejections []string
		for i := acceptedCount; i < len(body.Batches); i++ {
			rejections = append(rejections, fmt.Sprintf(`{"index":%d,"reason":"package is not public (UNKNOWN)"}`, i))
		}
		fmt.Fprintf(w, `{"accepted":%d,"rejected":[%s]}`, acceptedCount, strings.Join(rejections, ","))
	}))
	defer srv.Close()

	cfg := communityCfg(srv.URL)
	rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}
	b := &Batcher{DB: db, Ident: ident, Cfg: cfg}

	pkgs := make([]scanner.ResolvedPackage, 500)
	for i := 0; i < 500; i++ {
		pkgs[i] = scanner.ResolvedPackage{
			PURL:       domain.PURL{Ecosystem: "npm", Name: fmt.Sprintf("pkg-%d", i), Version: "1.0.0"},
			Publicness: scanner.PublicnessPublic,
			Direct:     true,
			Source:     "package-lock.json",
		}
	}
	scan := &scanner.ScanResult{
		Packages: pkgs,
		Env:      testEnvFP(),
	}

	if err := rec.RecordRun(ctx, t.TempDir(), scan, knownProfile(), 0, ""); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	n, err := b.Upload(ctx, srv.Client(), srv.URL)
	if n != 20 {
		t.Errorf("reported %d batches uploaded, want 20", n)
	}
	if err == nil {
		t.Fatal("partially refused upload reported no problem at all")
	}
	if !strings.Contains(err.Error(), "480 batches") {
		t.Errorf("the rejection count never reached the user: %v", err)
	}
	if !strings.Contains(err.Error(), "package is not public (UNKNOWN)") {
		t.Errorf("the server's reason never reached the user: %v", err)
	}
}
