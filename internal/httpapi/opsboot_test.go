package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/hostpressure"
)

type fakeBootTimelineSource struct{ tl BootTimeline }

func (f fakeBootTimelineSource) BootTimeline() BootTimeline { return f.tl }

// The boot section (#250) carries the schedule's record by name, and says
// so when nothing was wired, following the routes/host "unmeasured is not
// zero" rule.
func TestOpsMetricsHandlerReportsBootSchedule(t *testing.T) {
	started := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	h := &OpsMetricsHandler{
		Host: fakeHostPressureReader{reading: hostpressure.Reading{}},
		Boot: fakeBootTimelineSource{tl: BootTimeline{
			StartedAt: started,
			Marks: []BootMark{
				{Name: "migrated", AtSeconds: 0.4},
				{Name: "listen", AtSeconds: 1.2},
				{Name: "maintenance-done", AtSeconds: 9.5},
				{Name: "builder-started", AtSeconds: 9.5},
			},
			Phases: []BootPhase{
				{Name: "stranded-drafts", BudgetSeconds: 30, StartedAtSeconds: 1.3, Seconds: 0.2,
					Outcome: "ok", Detail: "requeued 0 stranded authoring drafts", Concurrent: []string{"serving"}},
				{Name: "publicness", BudgetSeconds: 120, StartedAtSeconds: 1.5, Seconds: 8,
					Outcome: "budget-exceeded", PoolBusy: 2, QueryTimeouts: 1, PoolWaitSeconds: 3.5},
			},
			MaintenanceDone: true,
		}},
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/ops/pool-metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Boot struct {
			Measured        bool   `json:"measured"`
			StartedAt       string `json:"startedAt"`
			UptimeSeconds   float64
			MaintenanceDone bool `json:"maintenanceDone"`
			Marks           []struct {
				Name      string  `json:"name"`
				AtSeconds float64 `json:"atSeconds"`
			} `json:"marks"`
			Phases []struct {
				Name            string   `json:"name"`
				BudgetSeconds   float64  `json:"budgetSeconds"`
				Seconds         float64  `json:"seconds"`
				Outcome         string   `json:"outcome"`
				PoolBusy        int64    `json:"poolBusy"`
				QueryTimeouts   int64    `json:"queryTimeouts"`
				PoolWaitSeconds float64  `json:"poolWaitSeconds"`
				Concurrent      []string `json:"concurrent"`
			} `json:"phases"`
		} `json:"boot"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v; body=%s", err, rec.Body.String())
	}
	b := got.Boot
	if !b.Measured || !b.MaintenanceDone || b.StartedAt != "2026-09-01T10:00:00Z" {
		t.Fatalf("boot header = %+v", b)
	}
	if b.UptimeSeconds <= 0 {
		t.Errorf("uptimeSeconds = %v, want > 0", b.UptimeSeconds)
	}
	if len(b.Marks) != 4 || b.Marks[1].Name != "listen" || b.Marks[1].AtSeconds != 1.2 {
		t.Fatalf("marks = %+v", b.Marks)
	}
	if len(b.Phases) != 2 {
		t.Fatalf("phases = %+v", b.Phases)
	}
	if p := b.Phases[1]; p.Outcome != "budget-exceeded" || p.PoolBusy != 2 || p.QueryTimeouts != 1 || p.PoolWaitSeconds != 3.5 || p.BudgetSeconds != 120 {
		t.Errorf("phase publicness = %+v", p)
	}
	// A phase with nothing concurrent is [] on the wire, not null: the
	// observer reads the array's length.
	if p := b.Phases[1]; p.Concurrent == nil || len(p.Concurrent) != 0 {
		t.Errorf("publicness.concurrent = %#v, want []", p.Concurrent)
	}

	h.Boot = nil
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/ops/pool-metrics", nil))
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	boot, _ := raw["boot"].(map[string]any)
	if boot == nil || boot["measured"] != false {
		t.Fatalf("boot without a source = %#v, want measured=false", boot)
	}
	if _, ok := boot["marks"].([]any); !ok {
		t.Errorf("boot.marks without a source = %#v, want []", boot["marks"])
	}
}

// The boot object is the LAST top-level field. The production observer
// (deploy/lightsail/collect-post-deploy-observation.sh) extracts host and
// pool positionally with sed, and an object with nested arrays placed
// before them would be what its greedy match found instead.
func TestOpsMetricsBootSectionIsLastField(t *testing.T) {
	h := &OpsMetricsHandler{Host: fakeHostPressureReader{}}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/ops/pool-metrics", nil))
	body := rec.Body.String()
	bootAt := indexOf(body, `"boot":`)
	for _, field := range []string{`"pool":`, `"host":`, `"farmIngest":`, `"routes":`, `"runtime":`} {
		if at := indexOf(body, field); at < 0 || at > bootAt {
			t.Errorf("%s at %d is not before boot at %d", field, at, bootAt)
		}
	}
}

func indexOf(s, sub string) int { return strings.Index(s, sub) }
