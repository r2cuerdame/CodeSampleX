package apidemand

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// AnonymousHeader and AnonymousCookie are the two places a pseudonymous
// client id travels. They mirror the anonymous-client middleware so the
// caller hash here is the same value that ledger already keeps.
const (
	AnonymousHeader = "X-CSX-Anonymous-ID"
	AnonymousCookie = "csx_anonymous"
)

// UserAgentPrefix is the fixed grammar every csx surface speaks:
//
//	csx/<version> (<surface>[; protocol=<mcp protocol>])
//
// Only this shape is parsed; a foreign agent is classified, never stored.
const UserAgentPrefix = "csx/"

var (
	userAgentPattern = regexp.MustCompile(`^csx/([0-9A-Za-z.+\-]{1,64}) \(([a-z]{1,16})(?:; protocol=([0-9A-Za-z.\-]{1,32}))?\)`)
	versionPattern   = regexp.MustCompile(`^v?([0-9]{1,6})\.([0-9]{1,6})\.([0-9]{1,6})(?:[.\-+][0-9A-Za-z.\-+]{0,40})?$`)
	countryPattern   = regexp.MustCompile(`^[A-Z]{2}$`)
)

// UserAgent builds the client token a csx surface sends. Version is reduced
// to the grammar's token set so a development stamp such as "dev (git)"
// cannot break the parse on the server; an empty version becomes "unknown".
func UserAgent(version, surface, protocol string) string {
	version = sanitizeToken(version, 64)
	if version == "" {
		version = "unknown"
	}
	surface = strings.ToLower(sanitizeAlpha(surface, 16))
	if surface == "" {
		surface = "csx"
	}
	out := UserAgentPrefix + version + " (" + surface
	if protocol = sanitizeToken(protocol, 32); protocol != "" {
		out += "; protocol=" + protocol
	}
	return out + ")"
}

func sanitizeToken(raw string, limit int) string {
	var b strings.Builder
	for _, r := range raw {
		if b.Len() >= limit {
			break
		}
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '.', r == '-', r == '+':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sanitizeAlpha(raw string, limit int) string {
	var b strings.Builder
	for _, r := range raw {
		if b.Len() >= limit {
			break
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Client is the parsed, bounded client identity of one request.
type Client struct {
	Kind     string
	Version  string
	Protocol string
}

// ParseUserAgent classifies a User-Agent without retaining it. A csx token
// yields its surface, version and protocol; a version outside the release
// grammar is kept only if it is a plain token, so dashboards can still show
// "unknown" builds without ever showing free text.
func ParseUserAgent(ua string) Client {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return Client{Kind: ClientNone}
	}
	match := userAgentPattern.FindStringSubmatch(ua)
	if match == nil {
		return Client{Kind: ClientOther}
	}
	client := Client{Version: match[1], Protocol: match[3]}
	switch match[2] {
	case ClientCLI, ClientMCP, ClientDaemon:
		client.Kind = match[2]
	default:
		client.Kind = ClientCSX
	}
	return client
}

// Release is a comparable release version. Non-release stamps (dev builds,
// "unknown") are not comparable and never count as stale or current.
type Release struct {
	Major, Minor, Patch int
}

// ParseRelease reads a vMAJOR.MINOR.PATCH stamp, with or without the v.
func ParseRelease(version string) (Release, bool) {
	match := versionPattern.FindStringSubmatch(strings.TrimSpace(version))
	if match == nil {
		return Release{}, false
	}
	major, err1 := strconv.Atoi(match[1])
	minor, err2 := strconv.Atoi(match[2])
	patch, err3 := strconv.Atoi(match[3])
	if err1 != nil || err2 != nil || err3 != nil {
		return Release{}, false
	}
	return Release{Major: major, Minor: minor, Patch: patch}, true
}

// Less reports whether r precedes o.
func (r Release) Less(o Release) bool {
	if r.Major != o.Major {
		return r.Major < o.Major
	}
	if r.Minor != o.Minor {
		return r.Minor < o.Minor
	}
	return r.Patch < o.Patch
}

// Country validates an edge-supplied country header value. Only an
// uppercase ISO 3166-1 alpha-2 shape is accepted; everything else, including
// the edge's own "unknown" markers, is the empty unknown bucket.
func Country(raw string) string {
	raw = strings.ToUpper(strings.TrimSpace(raw))
	if !countryPattern.MatchString(raw) {
		return ""
	}
	return raw
}

// Outcome classifies a status code. An unwritten status (0) is the net/http
// default 200.
func Outcome(status int) string {
	switch {
	case status == 0, status >= 200 && status < 400:
		return OutcomeSuccess
	case status >= 400 && status < 500:
		return OutcomeRejected
	default:
		return OutcomeFailure
	}
}

// Identity is the auth class and pseudonymous caller hash of one request.
type Identity struct {
	Auth       string
	CallerHash string
}

// Identify classifies who is calling without keeping anything that names
// them. The anonymous hash is exactly the one the anonymous-client ledger
// stores, so both tables count the same caller the same way; a credential
// is reduced through a domain-separated digest that never leaves the store.
func Identify(r *http.Request) Identity {
	if authorization := r.Header.Get("Authorization"); authorization != "" {
		sum := sha256.Sum256([]byte("csx-demand-caller-v1|" + authorization))
		return Identity{Auth: AuthAuthenticated, CallerHash: hex.EncodeToString(sum[:])}
	}
	id := r.Header.Get(AnonymousHeader)
	if !validAnonymousID(id) {
		id = ""
		if cookie, err := r.Cookie(AnonymousCookie); err == nil && validAnonymousID(cookie.Value) {
			id = cookie.Value
		}
	}
	if id == "" {
		return Identity{Auth: AuthUnidentified}
	}
	sum := sha256.Sum256([]byte("csx-anonymous-v1|" + id))
	return Identity{Auth: AuthAnonymous, CallerHash: hex.EncodeToString(sum[:])}
}

func validAnonymousID(id string) bool {
	return len(id) == 64 && strings.Trim(id, "0123456789abcdef") == ""
}

// ValidRoute accepts only the mux pattern grammar the server registers:
// an upper-case method, one space and an ASCII path. It exists so a store
// can refuse a label that did not come from route registration.
func ValidRoute(route string) bool {
	if len(route) == 0 || len(route) > 96 {
		return false
	}
	method, path, ok := strings.Cut(route, " ")
	if !ok || method == "" || !strings.HasPrefix(path, "/") {
		return false
	}
	for _, r := range route {
		if r <= ' ' && r != ' ' || r > '~' {
			return false
		}
	}
	return true
}
