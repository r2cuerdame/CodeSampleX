package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// LOCAL ONLY promises that nothing about your projects leaves. A shard
// request names a package — GET /v1/shards/npm/left-pad/1, one per
// dependency, from one address — so warming from the local inventory sent
// the whole dependency tree to the server, which is exactly what the
// contract screen lists under what a COMMUNITY member contributes.
func TestLocalOnlyNeverWarmsFromYourOwnPackages(t *testing.T) {
	home := newTestHome(t, func(c *config.Config) {
		c.Mode = config.ModeLocalOnly
		c.PinnedPackages = []string{"pkg:npm/react@19.2.8"}
	})
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()

	// A package the scan found in the user's own project.
	if err := d.DB.UpsertPackage(ctx,
		domain.PURL{Ecosystem: "npm", Name: "left-pad", Version: "1.3.0"}, "PUBLIC"); err != nil {
		t.Fatal(err)
	}

	for _, k := range d.warmKeyList(ctx) {
		if strings.Contains(k, "left-pad") {
			t.Errorf("local-only warmed %q — the dependency inventory left the machine", k)
		}
	}

	// Community mode is what the inventory is for, and still uses it.
	home2 := newTestHome(t, func(c *config.Config) { c.Mode = config.ModeCommunity })
	d2, err := New(home2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d2.Close() })
	if err := d2.DB.UpsertPackage(ctx,
		domain.PURL{Ecosystem: "npm", Name: "left-pad", Version: "1.3.0"}, "PUBLIC"); err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, k := range d2.warmKeyList(ctx) {
		if strings.Contains(k, "left-pad") {
			saw = true
		}
	}
	if !saw {
		t.Error("community mode stopped warming from the project inventory")
	}
}
