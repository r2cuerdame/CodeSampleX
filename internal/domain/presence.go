package domain

import (
	"errors"
	"regexp"
	"strings"
)

// IsPublicClientClass reports whether clientClass is an explicit public class
// included in public active installation counts ("ordinary" or "external").
// Internal/farm/ci/verifier/operator nodes are excluded from public counts.
// Class is never inferred from IP or UA.
func IsPublicClientClass(clientClass string) bool {
	switch strings.ToLower(strings.TrimSpace(clientClass)) {
	case "ordinary", "external":
		return true
	default:
		return false
	}
}

// PresencePayload is the client presence wire payload (GitHub #383).
// It carries only schemaVersion, three epoch labels/tokens, explicit clientClass,
// and clientVersion.
// No seed, IP, hostname, username, account, repo/project/query/package/log.
type PresencePayload struct {
	SchemaVersion int    `json:"schemaVersion"`
	ClientClass   string `json:"clientClass"`
	ClientVersion string `json:"clientVersion"`
	Epoch1d       string `json:"epoch1d"`
	Token1d       string `json:"token1d"`
	Epoch7d       string `json:"epoch7d"`
	Token7d       string `json:"token7d"`
	Epoch30d      string `json:"epoch30d"`
	Token30d      string `json:"token30d"`
}

var (
	ErrPresenceSchemaVersion = errors.New("presence: schemaVersion must be 1")
	ErrPresenceClientClass   = errors.New("presence: invalid clientClass (1-64 alphanumeric/underscore/hyphen chars)")
	ErrPresenceClientVersion = errors.New("presence: invalid clientVersion (1-64 chars)")
	ErrPresenceEpoch1d       = errors.New("presence: invalid epoch1d (must be YYYY-MM-DD)")
	ErrPresenceEpoch7d       = errors.New("presence: invalid epoch7d (must be numeric Unix-day/7)")
	ErrPresenceEpoch30d      = errors.New("presence: invalid epoch30d (must be numeric Unix-day/30)")
	ErrPresenceToken1d       = errors.New("presence: invalid token1d (must be 16-64 hex chars)")
	ErrPresenceToken7d       = errors.New("presence: invalid token7d (must be 16-64 hex chars)")
	ErrPresenceToken30d      = errors.New("presence: invalid token30d (must be 16-64 hex chars)")
)

var (
	validClientClassRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-\.]{1,64}$`)
	validHexTokenRegex    = regexp.MustCompile(`^[0-9a-fA-F]{16,64}$`)
	validEpoch1dRegex     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	validEpochIntRegex    = regexp.MustCompile(`^\d{1,16}$`)
)

// Validate checks the presence payload against strict bounded constraints.
func (p PresencePayload) Validate() error {
	if p.SchemaVersion != 1 {
		return ErrPresenceSchemaVersion
	}
	if !validClientClassRegex.MatchString(p.ClientClass) {
		return ErrPresenceClientClass
	}
	if p.ClientVersion == "" || len(p.ClientVersion) > 64 {
		return ErrPresenceClientVersion
	}
	if !validEpoch1dRegex.MatchString(p.Epoch1d) {
		return ErrPresenceEpoch1d
	}
	if !validEpochIntRegex.MatchString(p.Epoch7d) {
		return ErrPresenceEpoch7d
	}
	if !validEpochIntRegex.MatchString(p.Epoch30d) {
		return ErrPresenceEpoch30d
	}
	if !validHexTokenRegex.MatchString(p.Token1d) {
		return ErrPresenceToken1d
	}
	if !validHexTokenRegex.MatchString(p.Token7d) {
		return ErrPresenceToken7d
	}
	if !validHexTokenRegex.MatchString(p.Token30d) {
		return ErrPresenceToken30d
	}
	return nil
}
