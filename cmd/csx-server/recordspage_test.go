package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/web"
)

// Any browser could take the site's records page down with a URL:
// ?page=9223372036854775807 survived Atoi, (page-1)*perPage overflowed to a
// negative offset, and the store sliced with it. The website routes were
// mounted bare — only /v1 had the recover guard — so net/http dropped the
// connection and the visitor got no response at all, not even a 500.
func TestDeepRecordsPageDoesNotKillTheConnection(t *testing.T) {
	srv := httptest.NewServer(BuildMux(serverstore.ServerConfig{}, serverstore.NewFake()))
	defer srv.Close()

	for _, q := range []string{
		"/records?page=9223372036854775807",
		"/records?page=99999999999999999999", // does not even parse
		"/records?page=-5",
		"/records?page=2",
	} {
		resp, err := http.Get(srv.URL + q)
		if err != nil {
			t.Fatalf("GET %s: no response at all: %v", q, err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			t.Errorf("GET %s: status %d", q, resp.StatusCode)
		}
	}
}

// The store itself must not slice with a negative offset either, whatever
// reaches it.
func TestRecordPackagesClampsANegativeOffset(t *testing.T) {
	w := &webStore{s: serverstore.NewFake()}
	if _, _, err := w.RecordPackages(context.Background(), web.RecordFilter{}, -80, 40); err != nil {
		t.Fatalf("negative offset: %v", err)
	}
}

func TestRecordSnapshotFiltersRecordedEnvironmentAndBasis(t *testing.T) {
	raw := `{"rows":[{"envBucket":{"schemaVersion":1,"ecosystem":"npm","os":"linux","arch":"amd64","runtime":"node","runtimeVersion":"22.18"},"byStage":{"PROJECT_COMPILE":{"pass":4,"fail":0},"CONTRACT":{"pass":1,"fail":0}}}]}`
	for _, filter := range []web.RecordFilter{
		{OS: "linux"},
		{Runtime: "node"},
		{Basis: "observed"},
		{Basis: "verified"},
		{OS: "linux", Runtime: "node", Basis: "verified"},
	} {
		if !recordSnapshotMatches(raw, filter) {
			t.Errorf("filter %+v did not match its recorded row", filter)
		}
	}
	for _, filter := range []web.RecordFilter{{OS: "windows"}, {Runtime: "python"}} {
		if recordSnapshotMatches(raw, filter) {
			t.Errorf("filter %+v matched an unrecorded dimension", filter)
		}
	}
	if recordSnapshotMatches(`{"rows":[{"envLabel":"node 22 · linux","byStage":{"CONTRACT":{"pass":1}}}]}`, web.RecordFilter{OS: "linux"}) {
		t.Error("presentation-only envLabel was treated as structured OS evidence")
	}
}
