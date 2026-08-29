package daemon

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"time"
)

//go:embed uiassets/ui.html
var uiHTML string

// uiTmpl is the single embedded dashboard page (no external assets, no
// CDN — the daemon serves everything itself, goal.md §12.5).
var uiTmpl = template.Must(template.New("ui").Parse(uiHTML))

// uiDep is one dependency row for the dashboard.
type uiDep struct {
	Name       string
	Version    string
	Publicness string
	LastSeen   string
}

// uiReadinessRow is one stage of the local activation ledger as the panel
// shows it (docs/activation-funnel.md §7). State empty means the stage has
// not been reached and the panel renders a gap, never a zero. Source names
// where the state was read from, and Next is the exact command that fixes a
// row that is not ready.
type uiReadinessRow struct {
	Label  string
	State  string
	Source string
	Next   string
}

// uiReadiness builds the §7 panel rows.
//
// Every string here is fixed by this function. Nothing about which coding
// agents are installed, which client handshook, or where any config lives
// reaches the page: that is §2.3 never-collected, and a local dashboard is
// still a page somebody screenshots into a bug report.
func uiReadiness(r Readiness) []uiReadinessRow {
	rows := []uiReadinessRow{
		{Label: "First run", State: r.FirstRunAt, Source: "csx.db"},
		{Label: "Initialized", State: r.InitAt, Source: "config.json", Next: "run csx init"},
		{Label: "Shard cache warmed", State: r.FirstSyncAt, Source: "csx.db", Next: "run csx sync"},
		// "never seen" is not "not working": no client has completed the
		// protocol lifecycle here, and process startup alone was never
		// readiness. The row says what it can show and nothing more.
		{Label: "MCP handshake", State: r.MCPFirstReadyAt, Source: "csx.db",
			Next: "restart your coding agent, then use a csx tool"},
		{Label: "First answer", State: r.FirstHitAt, Source: "csx.db",
			Next: "ask an agent to search before writing library code"},
		{Label: "First adoption", State: r.FirstAdoptionAt, Source: "csx.db",
			Next: "report_sample_adoption after using a sample"},
	}
	if r.MCPLastReadyAt != "" && r.MCPLastReadyAt != r.MCPFirstReadyAt {
		rows = append(rows, uiReadinessRow{
			Label: "MCP last handshake", State: r.MCPLastReadyAt, Source: "csx.db"})
	}
	// The §5 duration. Local by construction — both endpoints are on this
	// machine and neither survives a UTC day boundary on the server — so it
	// exists on this page and nowhere else.
	if r.SecondsToFirstAnswer != nil {
		rows = append(rows, uiReadinessRow{
			Label: "Time to first answer",
			State: (time.Duration(*r.SecondsToFirstAnswer) * time.Second).String() + " after csx init",
		})
	}
	return rows
}

// uiData is the render model for /ui.
type uiData struct {
	Version          string
	Uptime           string
	GeneratedAt      string
	ModeLabel        string
	ServerURL        string
	PeerID           string
	Home             string
	Stats            Stats
	CacheHuman       string
	PostHitPassHuman string
	AvgMissLLMCalls  int
	Deps             []uiDep
	PreviewJSON      string
	LocalOnly        bool
	Readiness        []uiReadinessRow
}

// handleUI renders the §12.5 dashboard: community status, cache size,
// scanned dependencies, hit/miss + post-hit pass numbers, the
// always-labeled-Estimated reasoning-avoided figure, evidence/seed/cross
// counters, and the privacy preview — the pending upload payloads
// rendered verbatim so the user sees exactly what leaves the machine.
func (d *Daemon) handleUI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	st, err := d.StatsNow(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	previewJSON := "{}"
	if preview, err := d.queuePreview(ctx); err == nil {
		if b, err := json.MarshalIndent(preview, "", "  "); err == nil {
			previewJSON = string(b)
		}
	}

	var deps []uiDep
	if rows, err := d.DB.ListPackages(ctx); err == nil {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].LastSeen.After(rows[j].LastSeen) })
		for _, p := range rows {
			last := ""
			if !p.LastSeen.IsZero() {
				last = p.LastSeen.UTC().Format("2006-01-02 15:04")
			}
			deps = append(deps, uiDep{
				Name:       p.PURL.Ecosystem + "/" + p.PURL.Name,
				Version:    p.PURL.Version,
				Publicness: p.Publicness,
				LastSeen:   last,
			})
			if len(deps) == 25 {
				break
			}
		}
	}

	passHuman := "—"
	if st.PostHitBuildReports > 0 {
		passHuman = fmt.Sprintf("%.0f%%", st.PostHitBuildPassRate*100)
	}

	data := uiData{
		Version:          Version,
		Uptime:           d.uptime(),
		GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
		ModeLabel:        modeLabel(d.Cfg.Mode),
		ServerURL:        d.Cfg.ServerURL,
		PeerID:           d.Ident.PeerID(),
		Home:             d.Home,
		Stats:            st,
		CacheHuman:       humanBytes(st.CacheBytes),
		PostHitPassHuman: passHuman,
		AvgMissLLMCalls:  avgMissLLMCalls,
		Deps:             deps,
		PreviewJSON:      previewJSON,
		Readiness:        uiReadiness(st.Readiness),
		LocalOnly:        d.Cfg.Mode != "community",
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := uiTmpl.Execute(w, data); err != nil {
		// Headers already sent; nothing more to do.
		return
	}
}

func modeLabel(mode string) string {
	switch mode {
	case "community":
		return "COMMUNITY — anonymous evidence sharing on"
	case "local-only":
		return "LOCAL ONLY — nothing about your projects leaves this machine"
	default:
		return "UNINITIALIZED — run csx init"
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
