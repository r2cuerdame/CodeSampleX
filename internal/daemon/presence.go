package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
)

// presenceReportTimeout bounds a presence reporting attempt.
// The report is lightweight (~250 bytes) and must never delay background chores.
const presenceReportTimeout = 5 * time.Second

// clientClass resolves the client class for presence reporting.
func (d *Daemon) clientClass() string {
	if d == nil || d.Cfg == nil {
		return (&config.Config{}).EffectiveClientClass()
	}
	return d.Cfg.EffectiveClientClass()
}

// reportPresenceIfNeeded reports installation presence to POST /v1/presence
// on an existing low-frequency network path (GitHub #383).
//
// Semantics:
//   - Only runs when community network is enabled (local-only / uninitialized never contact server).
//   - At most one successful report per UTC day/installation (using localdb stat statLastPresenceSuccessDay).
//   - Failure is fail-open: any error or non-200 response is silently ignored and never
//     blocks daemon startup, search, MCP, or uploads.
//   - Uses identity anonSeed-derived rotating PresenceTokens (stable within aligned 1d/7d/30d epoch).
//   - Sends explicit clientClass ("ordinary", "farm", "ci", etc.) so internal nodes can be excluded server-side.
//   - No IP or User-Agent is used or stored for identity.
func (d *Daemon) reportPresenceIfNeeded(ctx context.Context) bool {
	if d == nil || !d.communityNetworkEnabled() || d.Ident == nil || d.DB == nil || d.Cfg == nil {
		return false
	}
	serverURL := strings.TrimRight(d.Cfg.ServerURL, "/")
	if serverURL == "" {
		return false
	}

	now := time.Now().UTC()
	today := identity.AlignedEpoch1d(now)

	// Check if today was already successfully reported
	lastReportedDay, ok, err := d.DB.GetStat(ctx, statLastPresenceSuccessDay)
	if err == nil && ok && lastReportedDay == today {
		return false
	}

	// Derive rotating tokens for the current aligned epochs
	tokens := d.Ident.PresenceTokens(now)
	payload := domain.PresencePayload{
		SchemaVersion: 1,
		ClientClass:   d.clientClass(),
		ClientVersion: Version,
		Epoch1d:       tokens.Epoch1d,
		Token1d:       tokens.Token1d,
		Epoch7d:       tokens.Epoch7d,
		Token7d:       tokens.Token7d,
		Epoch30d:      tokens.Epoch30d,
		Token30d:      tokens.Token30d,
	}

	if err := payload.Validate(); err != nil {
		return false
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return false
	}

	pctx, cancel := context.WithTimeout(ctx, presenceReportTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(pctx, http.MethodPost, serverURL+"/v1/presence", bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.httpClient().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
		_ = d.DB.SetStat(context.WithoutCancel(ctx), statLastPresenceSuccessDay, today)
		return true
	}
	return false
}
