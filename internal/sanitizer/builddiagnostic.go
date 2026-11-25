package sanitizer

import (
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

const maxDiagnosticMessageBytes = 512

var reDiagnosticFile = regexp.MustCompile(`^[A-Za-z0-9_.@+-][A-Za-z0-9_.@+/ -]*$`)

func PublicDiagnosticMessage(raw string) string {
	s := strings.Join(strings.Fields(raw), " ")
	s = reNodeModules.ReplaceAllString(s, "<path>")
	s = reWinPath.ReplaceAllString(s, "${1}<path>")
	s = reUnixPath.ReplaceAllString(s, "${1}<path>")
	s = reRelPath.ReplaceAllString(s, "<path>")
	s = reURL.ReplaceAllString(s, "<url>")
	s = reEmail.ReplaceAllString(s, "<email>")
	s = reSecretAssignment.ReplaceAllString(s, "${1}=<token>")
	s = reKnownToken.ReplaceAllString(s, "<token>")
	s = reTokenCand.ReplaceAllStringFunc(s, func(m string) string {
		if tokenish(m) {
			return "<token>"
		}
		return m
	})
	for _, re := range userScrubPatterns() {
		s = re.ReplaceAllString(s, "<user>")
	}
	if len(s) > maxDiagnosticMessageBytes {
		s = s[:maxDiagnosticMessageBytes]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return strings.TrimSpace(s)
}

func PublicDiagnosticFile(raw string) string {
	s := strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	if s == "" || strings.HasPrefix(s, "/") || strings.Contains(s, ":") {
		return ""
	}
	s = path.Clean(s)
	if s == "." || s == ".." || strings.HasPrefix(s, "../") || len(s) > 256 || !reDiagnosticFile.MatchString(s) {
		return ""
	}
	return s
}
