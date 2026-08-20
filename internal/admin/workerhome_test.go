package admin

import (
	"strings"
	"testing"
)

// The worker script created a fresh CSX_HOME per SESSION, and never
// initialized it. Two consequences, both silent:
//
// The home stayed uninitialized, and MayContactRegistries excludes that mode
// deliberately — "before csx init, no mode has been chosen, so no permission
// has been given". So every build the agent ran through run_observed_command
// recorded nothing, while step 4 of its own instructions told it to run builds
// exactly that way. In production one session held 94 search hits and 247
// known packages and had sent zero evidence.
//
// And a per-session home means a per-session identity, so a day of sessions
// would report as a day of distinct peers.
func TestWorkerScriptKeysHomeOnTheWorkerAndInitialisesItOnce(t *testing.T) {
	grant := authoringGrant{ID: "sess-abc", Token: "tok-123", Label: "laptop-01",
		Model: "agy", Reasoning: "auto"}
	for _, script := range []string{
		authoringWindowsCMD("https://codesamplex.dev", grant),
		authoringLinuxSH("https://codesamplex.dev", grant),
	} {
		key := authoringWorkerKey("laptop-01")
		if !strings.Contains(script, key) {
			t.Errorf("script does not key CSX_HOME on the worker (%q):\n%s", key, script)
		}
		if !strings.Contains(script, "csx init") {
			t.Error("script never initialises the home it creates")
		}
		if !strings.Contains(script, "--community") {
			t.Error("csx init runs without an explicit mode, so it would prompt")
		}
		// Isolation is the reason the home is separate in the first place.
		if strings.Contains(script, "--no-agents") == false {
			t.Error("init would rewrite the operator's agent registrations")
		}
	}
}
