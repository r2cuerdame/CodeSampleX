package web

import (
	"slices"
	"strings"
)

// filterClustersToPins keeps the failure clusters that belong to the
// coordinate the reader has narrowed to.
//
// A cluster is a fact about ONE coordinate — this version, this runtime,
// this OS — and the package page used to list every cluster it had no
// matter what was pinned. A reader who had drilled to 2.0.4 on node 22 was
// shown failures from 2.0.3 on node 24 with nothing saying so.
//
// A cluster is dropped only on a CONTRADICTION, never on silence. The
// package-level cluster carries no symbol and still explains a symbol page;
// a cluster recorded as "linux" still applies under an alpine pin. Coarser
// than the pin is not the same as somewhere else, and dropping those would
// hide exactly the failures most likely to be the answer.
func filterClustersToPins(clusters []failureCluster, pins map[string]string) []failureCluster {
	if len(pins) == 0 {
		return clusters
	}
	out := make([]failureCluster, 0, len(clusters))
	for _, c := range clusters {
		if clusterFitsPins(c, pins) {
			out = append(out, c)
		}
	}
	return out
}

func clusterFitsPins(c failureCluster, pins map[string]string) bool {
	for dim, pin := range pins {
		switch dim {
		case "version":
			if len(c.Versions) > 0 && !slices.Contains(c.Versions, pin) {
				return false
			}
		case "symbol":
			// "" is the whole package: it holds at every symbol of it.
			if c.Symbol != "" && !symbolPinFits(c.Symbol, pin) {
				return false
			}
		case "os":
			if got := c.EnvSummary["os"]; got != "" && !osPinFits(got, pin) {
				return false
			}
		case "runtime":
			if got := c.EnvSummary["runtime"]; got != "" && clusterRuntimeAxis(got) != pin {
				return false
			}
		case "context":
			if got := c.EnvSummary["executionContext"]; got != "" && !strings.EqualFold(got, pin) {
				return false
			}
		case "arch":
			if got := c.EnvSummary["arch"]; got != "" && !strings.EqualFold(got, pin) {
				return false
			}
		}
		// tool and libc are deliberately absent: a cluster's environment
		// summary does not record them, so every cluster would be silence
		// and filtering on them could only ever be theatre.
	}
	return true
}

// symbolPinFits compares against the axis label, where the whole package is
// spelled out rather than left blank.
func symbolPinFits(symbol, pin string) bool {
	if pin == cubePackageLevel {
		return false // a named symbol is not the package-level aggregate
	}
	return symbol == pin
}

// osPinFits reads the OS pin against the plainer OS a cluster records.
//
// The axis names a distribution where it has one ("alpine"), or an OS with
// its release ("windows 11"); a cluster records only "linux" or "windows".
// A distro pin over a linux cluster is the finer name for the same place,
// so it fits; over a windows cluster it is a different place.
func osPinFits(clusterOS, pin string) bool {
	c := strings.ToLower(strings.TrimSpace(clusterOS))
	p := strings.ToLower(strings.TrimSpace(pin))
	if c == p {
		return true
	}
	if base, _, found := strings.Cut(p, " "); found && base == c {
		return true // "windows 11" over a cluster that only knows "windows"
	}
	if c == "linux" {
		return !isNamedOS(p) // a distro name; anything else is another OS
	}
	return false
}

func isNamedOS(s string) bool {
	base, _, _ := strings.Cut(s, " ")
	switch base {
	case "windows", "macos", "darwin", "linux", "freebsd", "openbsd", "netbsd", "android", "ios":
		return true
	}
	return false
}

// clusterRuntimeAxis rewrites a cluster's runtime into the axis vocabulary:
// the cluster says "node@22.16", the axis says "node 22".
func clusterRuntimeAxis(s string) string {
	name, version, found := strings.Cut(s, "@")
	if !found {
		return runtimeBucket(s, "")
	}
	return runtimeBucket(name, version)
}
