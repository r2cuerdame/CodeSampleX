package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/admin"
)

// instancesEnv lists the machines being paid for: "name=usdPerMonth", comma
// separated. Configured rather than fetched from AWS — see admin.Instance.
const instancesEnv = "CSX_INSTANCES"

func parseInstances(raw string) []admin.Instance {
	var out []admin.Instance
	for _, entry := range strings.Split(raw, ",") {
		name, price, ok := strings.Cut(strings.TrimSpace(entry), "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			continue
		}
		usd, err := strconv.ParseFloat(strings.TrimSpace(price), 64)
		// A malformed price is dropped rather than treated as free: an
		// instance shown at $0 is worse than one not shown at all.
		if err != nil || usd < 0 {
			continue
		}
		out = append(out, admin.Instance{Name: name, MonthlyUSD: usd})
	}
	return out
}

func configuredInstances() []admin.Instance { return parseInstances(os.Getenv(instancesEnv)) }
