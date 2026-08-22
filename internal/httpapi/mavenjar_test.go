package httpapi

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type fakeJarProber struct {
	jarless map[string]bool // name -> has no jar
	unknown map[string]bool
	asked   int
}

func (f *fakeJarProber) MavenHasJar(_ context.Context, p domain.PURL) (bool, bool) {
	f.asked++
	if f.unknown[p.Name] {
		return false, false
	}
	if f.jarless[p.Name] {
		return false, true
	}
	return true, true
}

func mavenRow(name, version string) serverstore.WantedRow {
	return serverstore.WantedRow{
		Ecosystem: "maven", Name: name, Version: version, Asks: 1, Kind: "WANTED",
	}
}

// A BOM and a parent POM declare versions for other modules and contain no
// classes. There is no symbol a contract could call, so the assignment can
// never finish — the same shape of waste as the Gradle plugin marker, and the
// last four items the Wanted board could not cover.
//
// The name cannot decide it: "-bom" and "-parent" are conventions authors
// choose, unlike Gradle's generated marker, and a false exclusion loses real
// work silently and for good. So the registry is asked.
func TestMavenCoordinatesWithNoJarAreDroppedFromAuthoringWork(t *testing.T) {
	prober := &fakeJarProber{jarless: map[string]bool{
		"com.google.guava/guava-parent":                  true,
		"org.jetbrains.kotlin/kotlin-gradle-plugins-bom": true,
		"org.junit/junit-bom":                            true,
	}}
	in := []serverstore.WantedRow{
		mavenRow("com.google.guava/guava-parent", "33.4.0-jre"),
		mavenRow("org.jetbrains.kotlin/kotlin-gradle-plugins-bom", "2.2.10"),
		mavenRow("org.junit/junit-bom", "5.10.2"),
		mavenRow("com.google.guava/guava", "33.4.0-jre"),
	}

	got := dropUnauthorableMaven(context.Background(), prober, in)
	if len(got) != 1 || got[0].Name != "com.google.guava/guava" {
		t.Fatalf("kept %v, want only the artifact that publishes a jar", wantedNames(got))
	}
}

// A registry that will not answer is not a registry that said no. Refusing on
// a failed probe would starve the queue on a bad afternoon, and starving it
// silently is how the last outage looked.
func TestAnUnansweredProbeKeepsTheCandidate(t *testing.T) {
	prober := &fakeJarProber{unknown: map[string]bool{"org.junit/junit-bom": true}}
	got := dropUnauthorableMaven(context.Background(), prober,
		[]serverstore.WantedRow{mavenRow("org.junit/junit-bom", "5.10.2")})
	if len(got) != 1 {
		t.Errorf("dropped a candidate the registry never answered about: %v", wantedNames(got))
	}
}

// No prober configured is the same answer, and other ecosystems are never
// asked about at all.
func TestOtherEcosystemsAreNotProbed(t *testing.T) {
	prober := &fakeJarProber{}
	in := []serverstore.WantedRow{
		{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Kind: "WANTED"},
		{Ecosystem: "pypi", Name: "jinja2", Version: "3.1.6", Kind: "WANTED"},
	}
	if got := dropUnauthorableMaven(context.Background(), prober, in); len(got) != 2 {
		t.Errorf("dropped a non-maven candidate: %v", wantedNames(got))
	}
	if prober.asked != 0 {
		t.Errorf("asked the maven registry about %d non-maven candidates", prober.asked)
	}
	if got := dropUnauthorableMaven(context.Background(), nil, in); len(got) != 2 {
		t.Errorf("dropped candidates with no prober configured: %v", wantedNames(got))
	}
}

func wantedNames(rows []serverstore.WantedRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}
