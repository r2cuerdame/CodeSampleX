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
			if got := c.EnvSummary["runtime"]; got != "" && !runtimePinFits(got, pin) {
				return false
			}
		case "context":
			if got := c.EnvSummary["executionContext"]; got != "" && !contextPinFits(got, pin) {
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
//
// Both sides pass through canonicalOSName first: the cluster records
// runtime.GOOS ("darwin") while the axis says "macos", and comparing the
// spellings raw dropped the macOS cluster under the very pin that names it.
func osPinFits(clusterOS, pin string) bool {
	c := canonicalOSName(strings.ToLower(strings.TrimSpace(clusterOS)))
	p := strings.ToLower(strings.TrimSpace(pin))
	if canonicalOSName(p) == c {
		return true
	}
	if base, _, found := strings.Cut(p, " "); found && canonicalOSName(base) == c {
		return true // "windows 11" over a cluster that only knows "windows"
	}
	if c == "linux" {
		return !isNamedOS(p) // a distro name; anything else is another OS
	}
	return false
}

// canonicalOSName folds the spellings one operating system goes by into the
// one the axis vocabulary uses.
func canonicalOSName(s string) string {
	switch s {
	case "darwin", "mac", "osx":
		return "macos"
	}
	return s
}

// runtimePinFits compares in the axis vocabulary, dropping only on a
// contradiction: two different families, or the same family at two different
// lines. A side that never recorded its version is coarser than the other,
// not somewhere else.
func runtimePinFits(clusterRuntime, pin string) bool {
	c := clusterRuntimeAxis(clusterRuntime)
	if strings.EqualFold(c, pin) {
		return true
	}
	cName, cVer, _ := strings.Cut(strings.TrimSuffix(c, unrecordedAxisSuffix), " ")
	pName, pVer, _ := strings.Cut(strings.TrimSpace(pin), " ")
	if !strings.EqualFold(cName, pName) {
		return false
	}
	return cVer == "" || pVer == ""
}

// namedBrowsers are the contexts that name one specific browser. The other
// browser contexts ("browser", "webview", …) say only that the work ran in a
// browser, which any browser pin is a finer name for.
var namedBrowsers = map[string]bool{
	"chrome": true, "chromium": true, "edge": true, "firefox": true, "safari": true,
}

// contextPinFits drops only on a contradiction. The context axis writes
// browser work as its family and major ("chrome 134") while a cluster's
// summary may hold only the generic "browser" — coarser, not elsewhere. Two
// different NAMED browsers do contradict.
func contextPinFits(clusterContext, pin string) bool {
	c := strings.ToLower(strings.TrimSpace(clusterContext))
	p := strings.ToLower(strings.TrimSpace(pin))
	if c == p {
		return true
	}
	pBase, _, _ := strings.Cut(p, " ")
	if browserContexts[c] && browserContexts[pBase] {
		return !(namedBrowsers[c] && namedBrowsers[pBase] && c != pBase)
	}
	// "node" under "node 22": the cluster never recorded the version the
	// axis carries.
	return c == pBase
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
