package httpapi

import (
	"fmt"
	"net/http"
	"time"
)

// recordSLOPhase exposes fixed, non-identifying work intervals before the
// response is written. The application total is added by csx-server's outer
// handler; the public probe records both without changing its SLO verdict.
func recordSLOPhase(w http.ResponseWriter, name string, started time.Time) {
	w.Header().Add("Server-Timing", fmt.Sprintf("%s;dur=%.3f", name, float64(time.Since(started))/float64(time.Millisecond)))
}
