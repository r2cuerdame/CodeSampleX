// Package admin serves the private, read-only operator dashboard.
//
// It deliberately consumes only bounded aggregate reads. In particular it
// does not infer users, active MCP sessions, downloads, or factory activity
// from unrelated counters.
package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const (
	dashboardTimeout = 5 * time.Second
	topWantedLimit   = 10
)

//go:embed templates/admin.html
var templateFS embed.FS

var dashboardTemplate = template.Must(template.New("admin.html").Funcs(template.FuncMap{
	"number": formatInt,
}).ParseFS(templateFS, "templates/admin.html"))

// Store is the deliberately small read-only view needed by the dashboard.
// Every method returns a bounded aggregate or a bounded page.
type Store interface {
	GetLatestStats(ctx context.Context) (statsJSON string, ok bool, err error)
	NetworkCounts(ctx context.Context, now time.Time) (serverstore.NetworkCounts, error)
	ListWanted(ctx context.Context, query string, offset, limit int) (rows []serverstore.WantedRow, total int, err error)
	AdoptionSummary(ctx context.Context) (serverstore.AdoptionCounts, error)
}

// Deps wires the private dashboard. TokenSHA256 must be the 64-character
// hexadecimal SHA-256 digest of the HTTP Basic password; a raw token is never
// accepted. The username is always "admin".
type Deps struct {
	Store       Store
	TokenSHA256 string
	Version     string
	StartedAt   time.Time
	Now         func() time.Time
}

type handler struct {
	store     Store
	wantHash  [sha256.Size]byte
	version   string
	startedAt time.Time
	now       func() time.Time
}

// Register mounts the exact /admin path only when TokenSHA256 is a valid
// digest. Returning false and registering nothing is intentional: absent or
// malformed configuration makes the private route indistinguishable from an
// unknown path (404), instead of accidentally falling back to another token.
func Register(mux *http.ServeMux, d Deps) bool {
	wantHash, ok := parseDigest(d.TokenSHA256)
	if !ok {
		return false
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	startedAt := d.StartedAt
	if startedAt.IsZero() {
		startedAt = now()
	}
	h := &handler{
		store:     d.Store,
		wantHash:  wantHash,
		version:   d.Version,
		startedAt: startedAt,
		now:       now,
	}
	// A methodless /admin pattern would conflict with the public website's
	// GET /{seg} route under Go's specificity rules. GET also covers HEAD;
	// explicit standard write methods reach the same read-only handler and
	// receive a header-hardened 405 without exposing any mutation surface.
	mux.Handle("GET /admin", h)
	for _, method := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
		http.MethodOptions, http.MethodConnect, http.MethodTrace,
	} {
		mux.Handle(method+" /admin", h)
	}
	return true
}

func parseDigest(raw string) ([sha256.Size]byte, bool) {
	var out [sha256.Size]byte
	// Do not trim: leading/trailing whitespace is malformed configuration,
	// not part of a permissive secret format.
	if len(raw) != hex.EncodedLen(len(out)) {
		return out, false
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != len(out) {
		return out, false
	}
	copy(out[:], decoded)
	return out, true
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="CodeSampleX Admin", charset="UTF-8"`)
		http.Error(w, "authorization required", http.StatusUnauthorized)
		return
	}

	now := h.now()
	data := dashboardData{
		Version:     h.version,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Uptime:      formatDuration(nonNegative(now.Sub(h.startedAt))),
	}
	if data.Version == "" {
		data.Version = "unknown"
	}

	if h.store == nil {
		data.DBError = "store is not configured"
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), dashboardTimeout)
		defer cancel()
		h.collect(ctx, now, &data)
	}

	var body bytes.Buffer
	if err := dashboardTemplate.Execute(&body, data); err != nil {
		http.Error(w, "dashboard rendering failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(body.Len()))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body.Bytes())
}

func (h *handler) authorized(r *http.Request) bool {
	username, password, basicOK := r.BasicAuth()
	// Hash both candidates before deciding. The secret comparison is always
	// between fixed-size values and uses the constant-time primitive; there is
	// no plaintext or variable-length secret comparison on any auth path.
	passwordHash := sha256.Sum256([]byte(password))
	usernameHash := sha256.Sum256([]byte(username))
	wantUsernameHash := sha256.Sum256([]byte("admin"))
	basic := 0
	if basicOK {
		basic = 1
	}
	match := subtle.ConstantTimeCompare(passwordHash[:], h.wantHash[:]) &
		subtle.ConstantTimeCompare(usernameHash[:], wantUsernameHash[:]) & basic
	return match == 1
}

func setPrivateHeaders(h http.Header) {
	h.Set("Cache-Control", "private, no-store, max-age=0")
	h.Set("Pragma", "no-cache")
	h.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
}

func (h *handler) collect(ctx context.Context, now time.Time, data *dashboardData) {
	probeStart := h.now()
	_, _, err := h.store.GetLatestStats(ctx)
	data.DBProbe = formatLatency(nonNegative(h.now().Sub(probeStart)))
	if err != nil {
		data.DBError = "read probe failed"
	} else {
		data.DBHealthy = true
	}

	if counts, err := h.store.NetworkCounts(ctx, now); err != nil {
		data.CountsError = "network counts unavailable"
	} else {
		data.Counts = counts
		data.CountsAvailable = true
	}

	if rows, total, err := h.store.ListWanted(ctx, "", 0, topWantedLimit); err != nil {
		data.WantedError = "Wanted summary unavailable"
	} else {
		data.Wanted = rows
		data.WantedTotal = total
		data.WantedAvailable = true
	}

	if adoption, err := h.store.AdoptionSummary(ctx); err != nil {
		data.AdoptionError = "adoption summary unavailable"
	} else {
		data.Adoption = adoption
		data.AdoptionAvailable = true
	}
}

type dashboardData struct {
	Version     string
	GeneratedAt string
	Uptime      string

	DBHealthy bool
	DBProbe   string
	DBError   string

	Counts          serverstore.NetworkCounts
	CountsAvailable bool
	CountsError     string

	Wanted          []serverstore.WantedRow
	WantedTotal     int
	WantedAvailable bool
	WantedError     string

	Adoption          serverstore.AdoptionCounts
	AdoptionAvailable bool
	AdoptionError     string
}

func nonNegative(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}

func formatLatency(d time.Duration) string {
	if d < time.Millisecond {
		return "<1 ms"
	}
	return fmt.Sprintf("%d ms", d.Milliseconds())
}

func formatDuration(d time.Duration) string {
	d = d.Truncate(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int64(d/time.Second))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int64(d/time.Minute), int64((d%time.Minute)/time.Second))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh %dm", int64(d/time.Hour), int64((d%time.Hour)/time.Minute))
	}
	return fmt.Sprintf("%dd %dh", int64(d/(24*time.Hour)), int64((d%(24*time.Hour))/time.Hour))
}

func formatInt(n int64) string {
	negative := n < 0
	s := strconv.FormatInt(n, 10)
	if negative {
		s = strings.TrimPrefix(s, "-")
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if negative {
		return "-" + s
	}
	return s
}
