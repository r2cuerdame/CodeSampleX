package serverstore

import (
	"context"
	"testing"
)

type packageStagePassStore interface {
	UpsertPackage(context.Context, PackageRow) error
	PutSnapshot(context.Context, string, string, string) error
	PackageStagePasses(context.Context, string, string, string) (map[string]int64, error)
}

func assertPackageStagePasses(t *testing.T, store packageStagePassStore) {
	t.Helper()
	ctx := t.Context()
	packages := []PackageRow{
		{PURL: "pkg:npm/boundary-package@1.5.0", Ecosystem: "npm", Name: "boundary-package", Version: "1.5.0", Publicness: "PUBLIC"},
		{PURL: "pkg:npm/boundary-package@1.1.0", Ecosystem: "npm", Name: "boundary-package", Version: "1.1.0", Publicness: "PUBLIC"},
		{PURL: "pkg:npm/other-package@1.1.0", Ecosystem: "npm", Name: "other-package", Version: "1.1.0", Publicness: "PUBLIC"},
	}
	for _, pkg := range packages {
		if err := store.UpsertPackage(ctx, pkg); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutSnapshot(ctx, packages[0].PURL, "", `{"rows":[{"byStage":{"PROJECT_TEST":{"pass":0}}}]}`); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSnapshot(ctx, packages[1].PURL, "", `{"rows":[{"byStage":{"PROJECT_TEST":{"pass":3}}},{"byStage":{"PROJECT_TEST":{"pass":4}}}]}`); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSnapshot(ctx, packages[1].PURL, "some.symbol", `{"rows":[{"byStage":{"PROJECT_TEST":{"pass":100}}}]}`); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSnapshot(ctx, packages[2].PURL, "", `{"rows":[{"byStage":{"PROJECT_TEST":{"pass":200}}}]}`); err != nil {
		t.Fatal(err)
	}

	got, err := store.PackageStagePasses(ctx, "npm", "boundary-package", "PROJECT_TEST")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["1.1.0"] != 7 {
		t.Fatalf("stage passes = %v, want only package-level 1.1.0=7", got)
	}
}

func TestPackageStagePassesInFake(t *testing.T) {
	assertPackageStagePasses(t, NewFake())
}

func TestIntegrationPackageStagePasses(t *testing.T) {
	assertPackageStagePasses(t, openTestPG(t))
}
