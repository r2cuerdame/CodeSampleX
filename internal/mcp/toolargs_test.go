package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

// The error message has always said "arguments must be a JSON object". The
// check behind it was json.Valid, which accepts a string, a number, an
// array and null. null is the one that hurt: unmarshalling it into a struct
// is a no-op that returns no error, so {"arguments": null} ran the tool
// with every field zeroed and the caller got a confident empty-query search
// back instead of being told what was wrong with the call.
func TestToolsCallRejectsArgumentsThatAreNotAnObject(t *testing.T) {
	s := &Server{Deps: &Deps{}}
	for _, args := range []string{"null", `"query"`, "42", "[1,2]", "true"} {
		t.Run(args, func(t *testing.T) {
			req := &rpcRequest{
				Method: "tools/call",
				Params: json.RawMessage(`{"name":"search_known_solution","arguments":` + args + `}`),
			}
			res, rerr := s.dispatch(context.Background(), req)
			if rerr == nil {
				t.Fatalf("arguments %s was accepted (result %v)", args, res)
			}
			if rerr.Code != codeInvalidParams {
				t.Errorf("code = %d, want invalid params", rerr.Code)
			}
		})
	}
}

// An object, or no arguments at all, still reaches the tool: whatever the
// tool then does, the call is not turned away at the argument check.
func TestToolsCallStillAcceptsAnObjectAndAMissingArguments(t *testing.T) {
	s := &Server{Deps: &Deps{}}
	for _, params := range []string{
		`{"name":"get_local_stats","arguments":{}}`,
		`{"name":"get_local_stats"}`,
		`{"name":"get_local_stats","arguments":  {"unused":1}}`,
	} {
		req := &rpcRequest{Method: "tools/call", Params: json.RawMessage(params)}
		if _, rerr := s.dispatch(context.Background(), req); rerr != nil && rerr.Code == codeInvalidParams {
			t.Errorf("%s was refused at the argument check: %s", params, rerr.Message)
		}
	}
}
