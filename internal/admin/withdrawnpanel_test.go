package admin

import (
	"strings"
	"testing"
)

// The farm payload nests quarantinedByReason inside "health" (farm_http.go);
// the script read it off the payload root, the `|| []` fallback swallowed the
// undefined, and the panel rendered "no quarantined samples" forever —
// silently hiding the unexplained-withdrawal signal it was built to surface.
// Every other field correctly reads data.health.*; this pins that agreement.
func TestWithdrawnPanelReadsTheHealthObject(t *testing.T) {
	js, err := adminStaticFS.ReadFile("static/admin.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	if strings.Contains(src, "data.quarantinedByReason") {
		t.Error("admin.js reads quarantinedByReason off the payload root; farm_http.go serves it under health")
	}
	if !strings.Contains(src, "quarantinedByReason") {
		t.Error("admin.js no longer reads quarantinedByReason at all")
	}
}
