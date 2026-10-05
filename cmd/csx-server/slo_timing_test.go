package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The four SLO routes must expose the application's time to first byte so
// the public probe can separate server work from edge/network delay.
func TestSLORoutesExposeApplicationTiming(t *testing.T) {
	mux := BuildMux(serverstore.ServerConfig{}, serverstore.NewFake())
	for path, phase := range map[string]string{
		"/healthz":             "csx_probe",
		"/version":             "",
		"/v1/stats":            "csx_stats",
		"/v1/shards/npm/zod/3": "csx_shard",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			got := rec.Header().Get("Server-Timing")
			if !strings.HasPrefix(got, "csx_app;dur=") {
				t.Fatalf("%s Server-Timing = %q, want application duration", path, got)
			}
			app := strings.TrimPrefix(strings.SplitN(got, ",", 2)[0], "csx_app;dur=")
			if value, err := strconv.ParseFloat(app, 64); err != nil || value < 0 {
				t.Fatalf("%s application duration %q is not a nonnegative number", path, app)
			}
			if phase != "" && !strings.Contains(got, phase+";dur=") {
				t.Fatalf("%s Server-Timing = %q, want %s phase", path, got, phase)
			}
		})
	}
}
