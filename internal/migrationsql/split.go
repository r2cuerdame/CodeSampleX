// Package migrationsql holds the one statement splitter shared by the
// migration runner and the deploy gate, so the gate validates exactly the
// statements the runner sends to Postgres (#742).
package migrationsql

import "strings"

// Split strips "--" line comments and splits on ';'. Migration files must
// therefore keep semicolons out of string literals — a documented constraint
// of this deliberately tiny runner. Block comments are left for Postgres.
func Split(sqlText string) []string {
	var b strings.Builder
	for _, line := range strings.Split(sqlText, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	var out []string
	for _, chunk := range strings.Split(b.String(), ";") {
		if s := strings.TrimSpace(chunk); s != "" {
			out = append(out, s)
		}
	}
	return out
}
