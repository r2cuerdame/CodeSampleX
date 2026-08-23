package serverstore

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// searchMissKey collapses one Wanted report — one search that returned
// NO_SAFE_MATCH — into a single stable identifier.
//
// A report names up to ten package coordinates, and the server expands it into
// one wanted row per coordinate. Those rows are the demand ranking, and they
// are the wrong unit for a rate: a miss that mentioned three packages would
// outweigh three separate misses, and the No-match ratio would report the size
// of people's dependency lists rather than how often the network answers.
//
// So the unit here is the question, and the key is derived from the whole
// coordinate set. That also makes the counter retry-safe for free: the same
// question re-uploaded the same UTC day lands on the same primary key and is
// dropped, exactly the way a hit is dropped by its offer id.
func searchMissKey(rows []WantedRow) string {
	if len(rows) == 0 {
		return ""
	}
	coordinates := make([]string, 0, len(rows))
	for _, row := range rows {
		coordinates = append(coordinates, strings.Join(
			[]string{row.Ecosystem, row.Name, row.Version, row.Symbol, row.TargetOS}, "\x1f"))
	}
	// Neither the wire batch nor the daemon's queue drain promises an order,
	// and an order-sensitive key would count the same question twice.
	sort.Strings(coordinates)
	sum := sha256.Sum256([]byte(strings.Join(coordinates, "\x1e")))
	return hex.EncodeToString(sum[:])
}
