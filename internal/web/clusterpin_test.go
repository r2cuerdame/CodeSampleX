package web

import "testing"

func hasownCluster(runtime, version, symbol string) failureCluster {
	return failureCluster{
		Symbol: symbol, Stage: "PROJECT_PROCESS", ErrorCode: "ERR_MODULE_NOT_FOUND",
		Versions: []string{version},
		EnvSummary: map[string]string{
			"os": "windows", "runtime": runtime,
			"moduleSystem": "cjs", "executionContext": "node",
		},
	}
}

// A failure cluster is a fact about ONE coordinate: this version, on this
// runtime, on this OS. The package page listed every cluster it had, so a
// reader who had pinned 2.0.4 on node 22 was shown failures from 2.0.3 on
// node 24 with nothing saying they belonged elsewhere.
//
// Everything else on the page already honours the pins. This is the same
// rule, and it is what makes a fully narrowed grid worth arriving at.
func TestClustersHonourThePinnedCoordinate(t *testing.T) {
	clusters := []failureCluster{
		hasownCluster("node@22.16", "2.0.4", ""),
		hasownCluster("node@24.13", "2.0.4", ""),
		hasownCluster("node@22.16", "2.0.3", ""),
	}
	got := filterClustersToPins(clusters, map[string]string{
		"version": "2.0.4", "runtime": "node 22",
	})
	if len(got) != 1 {
		t.Fatalf("clusters = %d, want the one on 2.0.4 and node 22", len(got))
	}
	if got[0].EnvSummary["runtime"] != "node@22.16" {
		t.Errorf("kept %+v", got[0].EnvSummary)
	}
}

// The runtime is written two ways — node@22.16 in the cluster, node 22 on the
// axis — and comparing them literally would hide every cluster there is.
func TestTheRuntimePinMatchesTheRecordedRuntime(t *testing.T) {
	c := []failureCluster{hasownCluster("node@22.16", "2.0.4", "")}
	if len(filterClustersToPins(c, map[string]string{"runtime": "node 22"})) != 1 {
		t.Error("node 22 did not match node@22.16")
	}
	if len(filterClustersToPins(c, map[string]string{"runtime": "node 24"})) != 0 {
		t.Error("node 24 matched node@22.16")
	}
}

// A package-level cluster has no symbol and belongs to every symbol of that
// package: the build broke, and which API you were looking at does not change
// that. Dropping it under a symbol pin would hide the failure most likely to
// explain what the reader is looking at.
func TestAPackageLevelClusterSurvivesASymbolPin(t *testing.T) {
	c := []failureCluster{
		hasownCluster("node@22.16", "2.0.4", ""),
		hasownCluster("node@22.16", "2.0.4", "somethingElse"),
	}
	got := filterClustersToPins(c, map[string]string{"symbol": "hasOwn"})
	if len(got) != 1 || got[0].Symbol != "" {
		t.Errorf("kept %+v, want only the package-level one", got)
	}
}

// Nothing pinned is the package overview, and it keeps everything.
func TestWithNothingPinnedEveryClusterStays(t *testing.T) {
	c := []failureCluster{
		hasownCluster("node@22.16", "2.0.4", ""),
		hasownCluster("node@24.13", "2.0.3", ""),
	}
	if len(filterClustersToPins(c, map[string]string{})) != 2 {
		t.Error("the unpinned overview dropped a cluster")
	}
}

// The page used to be handed twelve clusters, chosen by how many machines
// reported them across the WHOLE package, and only then narrowed to the
// coordinate. escalade has sixteen — fifteen on windows, one on linux — so a
// reader who had drilled to the linux environment where that one was recorded
// was shown nothing, because it ranked sixteenth overall.
//
// Narrow first, bound second.
func TestACoordinatesClusterIsNotCutForRankingLowOverall(t *testing.T) {
	clusters := make([]failureCluster, 0, 16)
	for i := 0; i < 15; i++ {
		c := hasownCluster("node@24.13", "3.2.0", "")
		c.EnvSummary["os"] = "windows"
		clusters = append(clusters, c)
	}
	onLinux := hasownCluster("node@22.23", "3.2.0", "")
	onLinux.EnvSummary["os"] = "linux"
	onLinux.ErrorCode = "ERR_THE_ONE_THAT_MATTERS"
	clusters = append(clusters, onLinux)

	got := filterClustersToPins(clusters, map[string]string{
		"os": "ubuntu glibc", "runtime": "node 22", "version": "3.2.0",
	})
	if len(got) != 1 || got[0].ErrorCode != "ERR_THE_ONE_THAT_MATTERS" {
		t.Fatalf("kept %d clusters, want the one recorded on this coordinate", len(got))
	}
}
