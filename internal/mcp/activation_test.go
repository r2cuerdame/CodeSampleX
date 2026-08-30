package mcp

import (
	"context"
	"testing"
)

// readyDeps counts the ready transitions a session reports.
func readyDeps(marks *int) *Deps {
	d := emptyDeps()
	d.MarkMCPReady = func(context.Context) { *marks++ }
	return d
}

// S4 of the funnel — "MCP connected" — had no signal on either side. `csx mcp`
// opened the local store at startup and wrote nothing, and MCP is a stdio
// transport with no request of its own, so a user whose agent never actually
// reached the server looked exactly like a user whose agent worked.
//
// docs/activation-funnel.md §7 is explicit about where the mark goes: NOT in
// newDeps, because opening the database proves only that a process started.
// A session is ready once it has answered a valid initialize and then
// received notifications/initialized — the point where a client has really
// completed the protocol lifecycle.
func TestASessionIsReadyOnlyAfterTheFullHandshake(t *testing.T) {
	marks := 0
	c := startServer(t, readyDeps(&marks))

	// initialized without a preceding initialize is not a handshake.
	c.notify("notifications/initialized", nil)
	if resp := c.call(1, "ping", nil); resp["result"] == nil {
		t.Fatalf("ping failed: %v", resp)
	}
	if marks != 0 {
		t.Fatalf("initialized alone marked the session ready %d time(s)", marks)
	}

	result(t, c.call(2, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "0"},
	}))
	if marks != 0 {
		t.Fatalf("initialize alone marked the session ready %d time(s)", marks)
	}

	c.notify("notifications/initialized", nil)
	if resp := c.call(3, "ping", nil); resp["result"] == nil {
		t.Fatalf("ping failed: %v", resp)
	}
	if marks != 1 {
		t.Fatalf("completed handshake marked the session ready %d time(s), want 1", marks)
	}

	// A client that repeats the notification has not started a second
	// session; mcpFirstReadyAt is write-once anyway, but the transition is
	// what is being counted and it happened once.
	c.notify("notifications/initialized", nil)
	if resp := c.call(4, "ping", nil); resp["result"] == nil {
		t.Fatalf("ping failed: %v", resp)
	}
	if marks != 1 {
		t.Fatalf("a repeated notification re-marked the session: %d", marks)
	}
}

// A `csx mcp` process that starts and is then closed — the shape of a
// misconfigured agent config, and of every probe that spawns the binary to
// see whether it exists — must record nothing. "Never seen" and "not working"
// are different states (§7), and a stamp written at startup would erase the
// difference.
func TestAProcessThatStartsAndClosesRecordsNoReadySession(t *testing.T) {
	marks := 0
	c := startServer(t, readyDeps(&marks))
	c.sendRaw(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	c.recv()
	if marks != 0 {
		t.Fatalf("a session with no handshake marked ready %d time(s)", marks)
	}
}
