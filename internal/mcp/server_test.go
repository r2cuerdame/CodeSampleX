package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/samples"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// pipeClient drives a Server over in-memory pipes like an MCP host would.
type pipeClient struct {
	t   *testing.T
	in  *io.PipeWriter
	out *bufio.Reader
}

// startServer runs a Server over io.Pipe pairs and returns a client.
func startServer(t *testing.T, deps *Deps) *pipeClient {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := &Server{In: inR, Out: outW, Deps: deps}
	done := make(chan error, 1)
	go func() {
		err := srv.Serve(context.Background())
		outW.Close()
		done <- err
	}()
	t.Cleanup(func() {
		inW.Close()
		if err := <-done; err != nil {
			t.Errorf("Serve returned error: %v", err)
		}
	})
	return &pipeClient{t: t, in: inW, out: bufio.NewReaderSize(outR, maxLineBytes)}
}

// sendRaw writes one raw protocol line.
func (c *pipeClient) sendRaw(line string) {
	c.t.Helper()
	if _, err := io.WriteString(c.in, line+"\n"); err != nil {
		c.t.Fatalf("write request: %v", err)
	}
}

// recv reads and decodes one response line.
func (c *pipeClient) recv() map[string]any {
	c.t.Helper()
	line, err := c.out.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read response: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		c.t.Fatalf("decode response %q: %v", line, err)
	}
	return resp
}

// call sends a request and returns the decoded response.
func (c *pipeClient) call(id any, method string, params any) map[string]any {
	c.t.Helper()
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	raw, err := json.Marshal(req)
	if err != nil {
		c.t.Fatalf("marshal request: %v", err)
	}
	c.sendRaw(string(raw))
	resp := c.recv()
	if got := fmt.Sprint(resp["id"]); got != fmt.Sprint(id) {
		c.t.Fatalf("response id = %v, want %v (resp %v)", resp["id"], id, resp)
	}
	return resp
}

// notify sends a notification (no id); no response is expected.
func (c *pipeClient) notify(method string, params any) {
	c.t.Helper()
	req := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		req["params"] = params
	}
	raw, err := json.Marshal(req)
	if err != nil {
		c.t.Fatalf("marshal notification: %v", err)
	}
	c.sendRaw(string(raw))
}

// result extracts the result object from a response, failing on rpc errors.
func result(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	if e, ok := resp["error"]; ok && e != nil {
		t.Fatalf("unexpected rpc error: %v", e)
	}
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("response has no result object: %v", resp)
	}
	return res
}

// rpcErrorCode extracts the error code from an error response.
func rpcErrorCode(t *testing.T, resp map[string]any) int {
	t.Helper()
	e, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected rpc error, got %v", resp)
	}
	code, ok := e["code"].(float64)
	if !ok {
		t.Fatalf("error has no numeric code: %v", e)
	}
	return int(code)
}

// emptyDeps returns a Deps where every call fails loudly; individual tests
// override the functions they exercise.
func emptyDeps() *Deps {
	return &Deps{
		Search: func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
			return domain.SearchResponse{SchemaVersion: 1, Miss: true, Results: []domain.SearchResult{}}, ""
		},
		GetSample: func(context.Context, string) (domain.SampleManifest, map[string]string, error) {
			return domain.SampleManifest{}, nil, fmt.Errorf("not wired")
		},
		Explain: func(context.Context, string, string, domain.EnvironmentFingerprint) (string, json.RawMessage, error) {
			return "", nil, fmt.Errorf("not wired")
		},
		RunObserved: func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 0, "", "", nil, commandOutput{}, fmt.Errorf("not wired")
		},
		ReportAdoption: func(context.Context, string, string, bool, *bool) (localdb.InterventionOutcome, error) {
			return localdb.InterventionOutcome{}, fmt.Errorf("not wired")
		},
		Propose: func(context.Context, string, []string, []string) (samples.SanitizedSpec, string, string, error) {
			return samples.SanitizedSpec{}, "", "", fmt.Errorf("not wired")
		},
		LocalHits:  func(context.Context) ([]localdb.HitRow, error) { return nil, nil },
		LocalStats: func(context.Context) (map[string]any, error) { return map[string]any{}, nil },
	}
}

func TestInitializeHandshake(t *testing.T) {
	c := startServer(t, emptyDeps())

	res := result(t, c.call(1, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "0"},
	}))
	if got := res["protocolVersion"]; got != ProtocolVersion {
		t.Errorf("protocolVersion = %v, want %s", got, ProtocolVersion)
	}
	caps, ok := res["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities missing: %v", res)
	}
	if _, ok := caps["tools"]; !ok {
		t.Errorf("capabilities.tools missing: %v", caps)
	}
	info, ok := res["serverInfo"].(map[string]any)
	if !ok {
		t.Fatalf("serverInfo missing: %v", res)
	}
	if info["name"] != "codesamplex" {
		t.Errorf("serverInfo.name = %v, want codesamplex", info["name"])
	}
	if v, _ := info["version"].(string); v == "" {
		t.Errorf("serverInfo.version empty")
	}

	// notifications/initialized is a no-op notification: no response. The
	// next response must belong to the following request.
	c.notify("notifications/initialized", nil)
	resp := c.call(2, "ping", nil)
	if res, ok := resp["result"].(map[string]any); !ok || len(res) != 0 {
		t.Errorf("ping result = %v, want empty object", resp["result"])
	}
}

func TestLeadingBOMTolerated(t *testing.T) {
	c := startServer(t, emptyDeps())
	// Windows shells sometimes prefix piped stdin with a UTF-8 BOM; the
	// first request must still parse.
	c.sendRaw("\xEF\xBB\xBF" + `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	resp := c.recv()
	if res, ok := resp["result"].(map[string]any); !ok || len(res) != 0 {
		t.Errorf("ping after BOM = %v, want empty result", resp)
	}
}

func TestParseAndRequestErrors(t *testing.T) {
	c := startServer(t, emptyDeps())

	// Not JSON at all → -32700 with id null.
	c.sendRaw(`{this is not json`)
	resp := c.recv()
	if code := rpcErrorCode(t, resp); code != -32700 {
		t.Errorf("parse error code = %d, want -32700", code)
	}
	if id, present := resp["id"]; !present || id != nil {
		t.Errorf("parse error id = %v, want null", resp["id"])
	}

	// Valid JSON that is not a request object → -32600.
	c.sendRaw(`[1,2,3]`)
	if code := rpcErrorCode(t, c.recv()); code != -32600 {
		t.Errorf("array request code = %d, want -32600", code)
	}

	// Object without method → -32600.
	c.sendRaw(`{"jsonrpc":"2.0","id":5}`)
	if code := rpcErrorCode(t, c.recv()); code != -32600 {
		t.Errorf("missing method code = %d, want -32600", code)
	}

	// Unknown method with id → -32601.
	if code := rpcErrorCode(t, c.call(6, "resources/list", nil)); code != -32601 {
		t.Errorf("unknown method code = %d, want -32601", code)
	}

	// Unknown tool → JSON-RPC error, not a tool result.
	resp = c.call(7, "tools/call", map[string]any{"name": "publish_public_sample", "arguments": map[string]any{}})
	if code := rpcErrorCode(t, resp); code != -32602 {
		t.Errorf("unknown tool code = %d, want -32602", code)
	}
}

func TestToolsListSchemas(t *testing.T) {
	c := startServer(t, emptyDeps())
	res := result(t, c.call(1, "tools/list", nil))
	tools, ok := res["tools"].([]any)
	if !ok {
		t.Fatalf("tools/list result has no tools array: %v", res)
	}
	if len(tools) != 8 {
		t.Fatalf("tools/list returned %d tools, want 8", len(tools))
	}

	wantRequired := map[string][]string{
		"search_known_solution":  {"query"},
		"get_sample":             {"sampleId"},
		"explain_compatibility":  {"package"},
		"run_observed_command":   {"command"},
		"report_sample_adoption": {"offerId", "sampleId", "applied"},
		"propose_public_sample":  {"goal", "packages"},
		"list_local_hits":        {},
		"get_local_stats":        {},
	}
	seen := map[string]bool{}
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("tool entry is not an object: %v", raw)
		}
		name, _ := tool["name"].(string)
		if name == "" {
			t.Fatalf("tool without name: %v", tool)
		}
		seen[name] = true
		if d, _ := tool["description"].(string); d == "" {
			t.Errorf("tool %s has no description", name)
		}
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			t.Fatalf("tool %s has no inputSchema object", name)
		}
		if schema["type"] != "object" {
			t.Errorf("tool %s schema type = %v, want object", name, schema["type"])
		}
		if _, ok := schema["properties"].(map[string]any); !ok {
			t.Errorf("tool %s schema has no properties object", name)
		}
		want, known := wantRequired[name]
		if !known {
			t.Errorf("unexpected tool %s", name)
			continue
		}
		gotReq, ok := schema["required"].([]any)
		if !ok {
			t.Errorf("tool %s schema has no required array", name)
			continue
		}
		if len(gotReq) != len(want) {
			t.Errorf("tool %s required = %v, want %v", name, gotReq, want)
			continue
		}
		for i, w := range want {
			if gotReq[i] != w {
				t.Errorf("tool %s required[%d] = %v, want %s", name, i, gotReq[i], w)
			}
		}
		// Required fields must exist in properties.
		props := schema["properties"].(map[string]any)
		for _, w := range want {
			if _, ok := props[w]; !ok {
				t.Errorf("tool %s required field %s missing from properties", name, w)
			}
		}
	}
	for name := range wantRequired {
		if !seen[name] {
			t.Errorf("tool %s missing from tools/list", name)
		}
	}
	if seen["publish_public_sample"] {
		t.Errorf("publish tool must not exist (goal.md §12.4)")
	}

	// The environment schema must expose the execution-context axis.
	for _, raw := range tools {
		tool := raw.(map[string]any)
		if tool["name"] != "search_known_solution" {
			continue
		}
		schema := tool["inputSchema"].(map[string]any)
		props := schema["properties"].(map[string]any)
		env, ok := props["environment"].(map[string]any)
		if !ok {
			t.Fatalf("search_known_solution has no environment property")
		}
		envProps, ok := env["properties"].(map[string]any)
		if !ok {
			t.Fatalf("environment schema has no properties")
		}
		for _, dim := range []string{"executionContext", "browserFamily", "browserMajor", "engine", "moduleSystem"} {
			if _, ok := envProps[dim]; !ok {
				t.Errorf("environment schema missing %s", dim)
			}
		}
	}
}
