package node

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

func findPkg(pkgs []scanner.ResolvedPackage, name string) (scanner.ResolvedPackage, bool) {
	for _, p := range pkgs {
		if p.PURL.Name == name {
			return p, true
		}
	}
	return scanner.ResolvedPackage{}, false
}

func mustFindPkg(t *testing.T, pkgs []scanner.ResolvedPackage, name string) scanner.ResolvedPackage {
	t.Helper()
	p, ok := findPkg(pkgs, name)
	if !ok {
		t.Fatalf("package %q not found in %v", name, pkgs)
	}
	return p
}

func assertNoPublic(t *testing.T, pkgs []scanner.ResolvedPackage) {
	t.Helper()
	for _, p := range pkgs {
		if p.Publicness == scanner.PublicnessPublic {
			t.Errorf("adapter claimed PUBLIC for %s; publicness upgrades belong to the registry check", p.PURL)
		}
	}
}

func checkCommonFixture(t *testing.T, pkgs []scanner.ResolvedPackage, wantSource string) {
	t.Helper()

	ax := mustFindPkg(t, pkgs, "axios")
	if ax.PURL.Version != "1.12.0" {
		t.Errorf("axios version = %q, want 1.12.0", ax.PURL.Version)
	}
	if !ax.Direct {
		t.Error("axios should be direct")
	}
	if ax.Publicness != scanner.PublicnessUnknown {
		t.Errorf("axios publicness = %q, want UNKNOWN", ax.Publicness)
	}
	if ax.Source != wantSource {
		t.Errorf("axios source = %q, want %q", ax.Source, wantSource)
	}

	tn := mustFindPkg(t, pkgs, "@types/node")
	if tn.PURL.Version != "22.5.4" {
		t.Errorf("@types/node version = %q, want 22.5.4", tn.PURL.Version)
	}
	if !tn.Direct {
		t.Error("@types/node should be direct (devDependency)")
	}
	if tn.Publicness != scanner.PublicnessUnknown {
		t.Errorf("@types/node publicness = %q, want UNKNOWN (scoped default)", tn.Publicness)
	}

	ll := mustFindPkg(t, pkgs, "local-lib")
	if ll.Publicness != scanner.PublicnessPrivate {
		t.Errorf("local-lib publicness = %q, want PRIVATE (file: dep)", ll.Publicness)
	}
	if !ll.Direct {
		t.Error("local-lib should be direct")
	}

	fr := mustFindPkg(t, pkgs, "follow-redirects")
	if fr.PURL.Version != "1.15.6" {
		t.Errorf("follow-redirects version = %q, want 1.15.6", fr.PURL.Version)
	}
	if fr.Direct {
		t.Error("follow-redirects should be transitive")
	}
	if fr.Publicness != scanner.PublicnessUnknown {
		t.Errorf("follow-redirects publicness = %q, want UNKNOWN", fr.Publicness)
	}

	assertNoPublic(t, pkgs)
}

func TestScanPackagesPackageLock(t *testing.T) {
	a := Adapter{}
	pkgs, err := a.ScanPackages(context.Background(), "testdata/npmproj")
	if err != nil {
		t.Fatalf("ScanPackages: %v", err)
	}
	checkCommonFixture(t, pkgs, "package-lock.json")

	pfe := mustFindPkg(t, pkgs, "proxy-from-env")
	if pfe.PURL.Version != "1.1.0" {
		t.Errorf("proxy-from-env version = %q, want 1.1.0 (nested node_modules)", pfe.PURL.Version)
	}
	if pfe.Direct {
		t.Error("proxy-from-env should be transitive")
	}

	if _, ok := findPkg(pkgs, "npmproj"); ok {
		t.Error("root project must not appear as a package")
	}
	ll := mustFindPkg(t, pkgs, "local-lib")
	if ll.PURL.Version != "1.0.0" {
		t.Errorf("local-lib version = %q, want 1.0.0 (from link target entry)", ll.PURL.Version)
	}
}

func TestScanPackagesPnpmLock(t *testing.T) {
	a := Adapter{}
	pkgs, err := a.ScanPackages(context.Background(), "testdata/pnpmproj")
	if err != nil {
		t.Fatalf("ScanPackages: %v", err)
	}
	checkCommonFixture(t, pkgs, "pnpm-lock.yaml")

	ll := mustFindPkg(t, pkgs, "local-lib")
	if ll.PURL.Version != "1.0.0" {
		t.Errorf("local-lib version = %q, want 1.0.0 (version field of directory dep)", ll.PURL.Version)
	}
}

func TestScanPackagesYarnLock(t *testing.T) {
	a := Adapter{}
	pkgs, err := a.ScanPackages(context.Background(), "testdata/yarnproj")
	if err != nil {
		t.Fatalf("ScanPackages: %v", err)
	}
	checkCommonFixture(t, pkgs, "yarn.lock")

	ll := mustFindPkg(t, pkgs, "local-lib")
	if ll.PURL.Version != "1.0.0" {
		t.Errorf("local-lib version = %q, want 1.0.0", ll.PURL.Version)
	}
}

func TestScanPackagesNoLockfile(t *testing.T) {
	a := Adapter{}
	pkgs, err := a.ScanPackages(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("ScanPackages on empty dir: %v", err)
	}
	if len(pkgs) != 0 {
		t.Errorf("expected no packages without a lockfile, got %v", pkgs)
	}
}

func TestIsPrivateSpec(t *testing.T) {
	private := []string{
		"file:../local-lib", "link:../x", "portal:../x", "workspace:*",
		"git+ssh://git@github.com/a/b.git", "git://github.com/a/b.git",
		"github:a/b", "../local-lib", "./x", "/abs/path", "C:\\proj\\x",
	}
	for _, s := range private {
		if !isPrivateSpec(s) {
			t.Errorf("isPrivateSpec(%q) = false, want true", s)
		}
	}
	public := []string{
		"^1.12.0", "1.12.0", "~2.0.0", "*",
		"https://registry.npmjs.org/axios/-/axios-1.12.0.tgz",
	}
	for _, s := range public {
		if isPrivateSpec(s) {
			t.Errorf("isPrivateSpec(%q) = true, want false", s)
		}
	}
}
