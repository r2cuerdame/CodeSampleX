package web

import (
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/buildinfo"
)

const pulseSnippet = `<script async src="/static/pulse.js`

// TestPurplePulseScriptRenderedOnAllPages verifies that all reachable pages
// include the PurplePulse telemetry script tag with the canonical project ID.
func TestPurplePulseScriptRenderedOnAllPages(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	pages := []string{
		"/",
		"/findings",
		"/samples",
		"/compatibility",
		"/gaps",
		"/dependencies",
		"/features",
	}

	for _, path := range pages {
		body := get(t, mux, path).Body.String()
		if !strings.Contains(body, pulseSnippet) {
			t.Errorf("%s is missing the PurplePulse telemetry snippet", path)
		}
		if n := strings.Count(body, `id="purple-pulse"`); n != 1 {
			t.Errorf("%s has %d purple-pulse tags, want exactly 1", path, n)
		}
		if !strings.Contains(body, `data-project-id="pp_codesamplex_f2f2ab10"`) {
			t.Errorf("%s is missing project ID attribute", path)
		}
	}
}

// TestPurplePulseStaticAssetServed verifies that /static/pulse.js is served
// with proper content type, cache headers, and support for conditional ETags.
func TestPurplePulseStaticAssetServed(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rev := testBuild().Revision
	if len(rev) < 7 {
		t.Fatalf("test build has no revision: %q", rev)
	}
	short := rev[:7]

	// 1. Versioned static asset receives immutable year-long caching.
	versioned := get(t, mux, "/static/pulse.js?v="+short)
	if versioned.Code != http.StatusOK {
		t.Fatalf("versioned asset status %d", versioned.Code)
	}
	cc := versioned.Header().Get("Cache-Control")
	if !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=31536000") {
		t.Errorf("versioned /static/pulse.js missing immutable 1y cache header: %q", cc)
	}
	if !strings.Contains(versioned.Body.String(), "pp_codesamplex_f2f2ab10") {
		t.Errorf("versioned /static/pulse.js body missing project ID")
	}

	// 2. Unversioned static asset receives shorter validator caching.
	plain := get(t, mux, "/static/pulse.js")
	if plain.Code != http.StatusOK {
		t.Fatalf("plain asset status %d", plain.Code)
	}
	etag := plain.Header().Get("ETag")
	if etag == "" {
		t.Fatal("/static/pulse.js missing ETag")
	}

	// 3. Revalidation with If-None-Match returns 304 Not Modified.
	reval := get(t, mux, "/static/pulse.js", "If-None-Match", etag)
	if reval.Code != http.StatusNotModified {
		t.Errorf("revalidation returned %d, want 304", reval.Code)
	}
	if reval.Body.Len() != 0 {
		t.Errorf("304 response carried non-empty body: %d bytes", reval.Body.Len())
	}
}

// TestPurplePulseBuildAttributes verifies that server version and environment
// are rendered as data attributes and cache-busting tokens when build information is known.
func TestPurplePulseBuildAttributes(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) {
		b := testBuild()
		b.Version = "v0.9.99"
		b.Environment = "staging"
		d.Build = b
	})

	body := get(t, mux, "/").Body.String()
	wantAttrs := []string{
		`data-project-id="pp_codesamplex_f2f2ab10"`,
		`data-version="v0.9.99"`,
		`data-env="staging"`,
		`src="/static/pulse.js?v=` + testBuild().ShortRevision() + `"`,
	}
	for _, want := range wantAttrs {
		if !strings.Contains(body, want) {
			t.Errorf("rendered page missing attribute %s", want)
		}
	}
}

// TestPurplePulseUnstampedBuildRendersCleanly verifies that an unstamped build
// renders the script tag without error or empty attributes and without a version token.
func TestPurplePulseUnstampedBuildRendersCleanly(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) {
		d.Build = buildinfo.Info{}
	})

	body := get(t, mux, "/").Body.String()
	if !strings.Contains(body, `src="/static/pulse.js"`) {
		t.Errorf("unstamped build should reference unversioned /static/pulse.js")
	}
	if strings.Contains(body, `data-version=""`) {
		t.Errorf("unstamped build rendered empty data-version attribute")
	}
	if strings.Contains(body, `data-env=""`) {
		t.Errorf("unstamped build rendered empty data-env attribute")
	}
}

// TestPurplePulseClientJSExecution runs the Node.js regression suite for the
// client-side telemetry script, verifying storage fallback, daily rate limiting,
// payload schema, and test-environment guards.
func TestPurplePulseClientJSExecution(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available: skipping JS execution assertions")
	}

	cmd := exec.Command(node, filepath.Join(".", "pulse_test.js"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pulse_test.js failed: %v\nOutput:\n%s", err, string(out))
	}
}
