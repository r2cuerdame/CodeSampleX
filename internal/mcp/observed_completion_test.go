package mcp

import (
	"context"
	"errors"
	"fmt"
	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type observedBlockedTransport struct {
	entered chan struct{}
	release chan struct{}
}

func (r *observedBlockedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	select {
	case r.entered <- struct{}{}:
	default:
	}
	<-r.release // Deliberately ignores cancellation, like a stuck local/proxy call.
	return nil, errors.New("offline registry fixture")
}
func TestHelperSevenSecondAndroidFailure(t *testing.T) {
	if os.Getenv("CSX_TEST_ANDROID_FAILURE") != "1" {
		return
	}
	time.Sleep(7 * time.Second)
	fmt.Fprintln(os.Stderr, "SDK location not found. Define ANDROID_HOME or sdk.dir in local.properties")
	os.Exit(1)
}
func TestFailedChildDoesNotWaitOnUnboundedRegistryScan(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"package.json":      `{"dependencies":{"axios":"1.12.0"}}`,
		"package-lock.json": `{"name":"fixture","lockfileVersion":3,"packages":{"":{"dependencies":{"axios":"1.12.0"}},"node_modules/axios":{"version":"1.12.0"}}}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := localdb.Open(filepath.Join(t.TempDir(), "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	transport := &observedBlockedTransport{entered: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(transport.release)
	t.Setenv("CSX_TEST_ANDROID_FAILURE", "1")
	started := time.Now()
	code, _, result, sanitized, output, err := runObserved(context.Background(), db, nil, cfg, &http.Client{Transport: transport}, nil, []string{os.Args[0], "-test.run=^TestHelperSevenSecondAndroidFailure$"}, dir)
	if err != nil || code != 1 || result != "FAIL" || !strings.Contains(output.Stderr, "SDK location not found") {
		t.Fatalf("child failure lost: %d %s %v %s", code, result, err, output.Stderr)
	}
	if elapsed := time.Since(started); elapsed > 14*time.Second {
		t.Fatalf("completed child was held for %s", elapsed)
	}
	select {
	case <-transport.entered:
	default:
		t.Fatal("registry fixture was not exercised")
	}
	if !strings.Contains(strings.Join(sanitized, " "), "observation incomplete") {
		t.Fatalf("missing partial observation notice: %v", sanitized)
	}
}
