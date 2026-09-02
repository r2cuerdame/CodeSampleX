package lightsail

// The Go runtime has to be told what the container's ceiling is, or it aims
// straight through it.
//
// Measured on production 2026-09-02. The server container is capped at 768M
// and had no GOMEMLIMIT and no GOGC, so the runtime sized the heap from GOGC's
// default alone -- goal = 2x live heap, with no idea a cgroup limit existed.
// It sat flat at 697MiB of anon RSS (file pages were 10MiB, so this was heap,
// not reclaimable cache), which is 93.6% of the cap:
//
//	anon 730652672   file 11071488   memory.max 805306368
//
// That is the same band the kernel killed it in three times the night before:
//
//	21:15:10  Killed process (csx-server) anon-rss:741872kB
//	21:20:50  Killed process (csx-server) anon-rss:697448kB
//	21:25:12  Killed process (csx-server) anon-rss:694388kB
//
// The builder change that stopped those kills removed the spike that reached
// the ceiling. It did not move the ceiling any closer to where the process
// actually lives, so the next spike of any origin lands in the same place.
//
// GOMEMLIMIT is the runtime's answer to exactly this: as the heap approaches
// it the collector runs harder, trading CPU for staying underneath. It is a
// SOFT limit, which is the point -- exceeding it makes the process slow, and
// the runtime caps GC at 50% CPU so "slow" stays bounded. Exceeding the
// cgroup limit makes the process dead. There is no reading under which the
// cgroup limit is the better one to hit first.

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// serverMemoryLimitBytes is the cgroup ceiling compose gives the server.
func serverMemoryLimitBytes(t *testing.T) int64 {
	t.Helper()
	compose := readDeployFixture(t, filepath.Join("..", "docker-compose.yml"))
	server := serverServiceBlock(t, compose)
	m := regexp.MustCompile(`memory:\s*([0-9]+)([MG])`).FindStringSubmatch(server)
	if m == nil {
		t.Fatalf("the server service declares no memory limit; this test cannot check headroom against nothing")
	}
	return parseSizeSuffix(t, m[1], m[2])
}

// serverGoMemLimitBytes is what the server tells the Go runtime.
func serverGoMemLimitBytes(t *testing.T) int64 {
	t.Helper()
	compose := readDeployFixture(t, filepath.Join("..", "docker-compose.yml"))
	server := serverServiceBlock(t, compose)
	// Reads the size out of either form the line can take: a bare value, or
	// the ${GOMEMLIMIT:-600MiB} default that lets an operator override it
	// from .env without editing compose. What ships when nobody overrides is
	// the default, so the default is what this has to check.
	m := regexp.MustCompile(`GOMEMLIMIT:[^\n]*?([0-9]+)(Mi|Gi|M|G)B?`).FindStringSubmatch(server)
	if m == nil {
		t.Fatalf("the server service sets no GOMEMLIMIT, so the runtime sizes its heap "+
			"as if the %d-byte container limit did not exist", serverMemoryLimitBytes(t))
	}
	return parseSizeSuffix(t, m[1], strings.TrimSuffix(m[2], "i"))
}

func parseSizeSuffix(t *testing.T, digits, unit string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	switch strings.ToUpper(unit) {
	case "M":
		return n << 20
	case "G":
		return n << 30
	}
	t.Fatalf("unknown size unit %q", unit)
	return 0
}

// serverServiceBlock returns just the `server:` service, so a limit belonging
// to db or caddy can never be read as the server's.
func serverServiceBlock(t *testing.T, compose string) string {
	t.Helper()
	start := strings.Index(compose, "\n  server:")
	if start < 0 {
		t.Fatal("no server service in docker-compose.yml")
	}
	rest := compose[start+1:]
	if end := regexp.MustCompile(`\n  [a-z_-]+:`).FindStringIndex(rest[1:]); end != nil {
		rest = rest[:end[0]+1]
	}
	return rest
}

// The runtime must be given a ceiling at all.
func TestServerTellsTheGoRuntimeItsCeiling(t *testing.T) {
	if got := serverGoMemLimitBytes(t); got <= 0 {
		t.Errorf("GOMEMLIMIT = %d", got)
	}
}

// And that ceiling has to be BELOW the container's, or it is decoration: a
// soft limit the process reaches only after the kernel has already killed it
// changes nothing, and would read in the compose file as protection that is
// not there.
func TestGoMemLimitLeavesHeadroomUnderTheContainerLimit(t *testing.T) {
	container := serverMemoryLimitBytes(t)
	goLimit := serverGoMemLimitBytes(t)

	if goLimit >= container {
		t.Fatalf("GOMEMLIMIT %d is not below the container limit %d; the kernel still gets there first",
			goLimit, container)
	}

	// Non-heap memory is real and does not answer to GOMEMLIMIT: goroutine
	// stacks, runtime metadata, and the OS mappings behind them all sit on
	// top of the heap the limit governs. A margin thinner than a tenth of
	// the container leaves the process still dying at the ceiling, only
	// after paying for the extra collection first.
	const minHeadroomFraction = 10 // one tenth
	if headroom := container - goLimit; headroom < container/minHeadroomFraction {
		t.Errorf("GOMEMLIMIT %d leaves %d bytes under the %d-byte container limit; "+
			"non-heap memory needs more than that",
			goLimit, headroom, container)
	}

	// The other direction: a limit far under the live heap buys nothing but
	// continuous collection. Production's steady state was ~350MiB live
	// (RSS sat flat at 697MiB with GOGC's default 2x goal), so a limit at or
	// below that would mean permanent GC pressure by construction.
	const measuredLiveHeap = 350 << 20
	if goLimit <= measuredLiveHeap {
		t.Errorf("GOMEMLIMIT %d is at or under the %d-byte live heap measured in production; "+
			"the collector would never stop", goLimit, measuredLiveHeap)
	}
}
