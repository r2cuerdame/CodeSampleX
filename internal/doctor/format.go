package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"text/tabwriter"
)

var secretPattern = regexp.MustCompile(`(?i)(csx_[0-9a-zA-Z_\-]{8,}|ghp_[0-9a-zA-Z_\-]{8,}|github_pat_[0-9a-zA-Z_\-]{8,}|bearer\s+[0-9a-zA-Z_\-\.]{8,})`)

// SanitizeSecret redacts potential tokens, keys, and authorization headers from strings.
func SanitizeSecret(s string) string {
	if s == "" {
		return ""
	}
	return secretPattern.ReplaceAllStringFunc(s, func(match string) string {
		lower := strings.ToLower(match)
		if strings.HasPrefix(lower, "bearer ") {
			return "Bearer [REDACTED]"
		}
		if strings.HasPrefix(lower, "csx_") {
			return "csx_***[REDACTED]"
		}
		if strings.HasPrefix(lower, "ghp_") {
			return "ghp_***[REDACTED]"
		}
		if strings.HasPrefix(lower, "github_pat_") {
			return "github_pat_***[REDACTED]"
		}
		return "[REDACTED]"
	})
}

// SanitizeResult produces a deeply redacted copy of Result to ensure zero secret leakage.
func SanitizeResult(res Result, explicitTokens ...string) Result {
	redactExplicit := func(s string) string {
		clean := SanitizeSecret(s)
		for _, tok := range explicitTokens {
			if tok != "" && len(tok) >= 4 {
				clean = strings.ReplaceAll(clean, tok, "[REDACTED_API_TOKEN]")
			}
		}
		return clean
	}

	sanitized := res
	sanitized.Checks = make([]Diagnosis, len(res.Checks))
	for i, c := range res.Checks {
		diag := c
		diag.Summary = redactExplicit(diag.Summary)
		diag.Remediation = redactExplicit(diag.Remediation)
		diag.Error = redactExplicit(diag.Error)
		if len(diag.Details) > 0 {
			diag.Details = make([]string, len(c.Details))
			for j, d := range c.Details {
				diag.Details[j] = redactExplicit(d)
			}
		}
		sanitized.Checks[i] = diag
	}
	return sanitized
}

// FormatTable outputs a clean, human-readable tabular representation of the diagnostic result.
func FormatTable(w io.Writer, res Result, verbose bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CHECK\tTIER\tSTATUS\tSUMMARY")

	for _, c := range res.Checks {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.ID, c.Tier, c.Status, c.Summary)
		if verbose {
			for _, d := range c.Details {
				fmt.Fprintf(tw, "  - %s\t\t\t\n", d)
			}
			if c.Remediation != "" && (c.Status == StatusFail || c.Status == StatusWarn) {
				fmt.Fprintf(tw, "  remediation: %s\t\t\t\n", c.Remediation)
			}
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "Summary: %d total, %d passed, %d fixed, %d warnings, %d failed\n",
		res.Summary.Total, res.Summary.Pass, res.Summary.Fixed, res.Summary.Warn, res.Summary.Fail)

	if res.Healthy {
		fmt.Fprintln(w, "Overall status: HEALTHY")
	} else {
		fmt.Fprintln(w, "Overall status: UNHEALTHY (run 'csx doctor --fix' to attempt safe self-healing)")
	}
	return nil
}

// FormatJSON outputs the sanitized diagnostic result in structured JSON format.
func FormatJSON(w io.Writer, res Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}
