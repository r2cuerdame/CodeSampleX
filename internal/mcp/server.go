// Package mcp implements the csx MCP stdio server (plan contract C8,
// goal.md §12.4): newline-delimited JSON-RPC 2.0 over a provided reader and
// writer, exposing the eight local tools coding agents consume. The server
// is transport-only here; the actual behavior is injected through Deps so
// the daemonless in-process wiring (NewDeps) and tests share one protocol
// path. `publish_public_sample` is deliberately absent — MCP must never be
// able to publish autonomously (goal.md §3.11, §12.4).
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

// ProtocolVersion is the MCP protocol revision this server speaks.
const ProtocolVersion = "2025-06-18"

// ServerName is the serverInfo.name reported by initialize.
const ServerName = "codesamplex"

// maxLineBytes bounds one JSON-RPC line (requests carrying error text or
// sample payloads stay far below this).
const maxLineBytes = 16 << 20

// JSON-RPC 2.0 error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// Server serves MCP over In/Out. Deps supplies tool behavior; Version is
// the build stamp reported in serverInfo (default "dev").
type Server struct {
	In      io.Reader
	Out     io.Writer
	Deps    *Deps
	Version string

	writeMu sync.Mutex
	// answeredInitialize and markedReady track the MCP lifecycle for this one
	// stdio session, which is what S4 of the activation funnel measures. They
	// are read and written only from the serial Serve loop.
	answeredInitialize bool
	markedReady        bool
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // nil marshals as null (parse errors)
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Serve reads newline-delimited JSON-RPC requests until In reaches EOF.
// Requests are handled serially (permitted by C8: arrival may be
// concurrent, serial handling is fine). Handler panics never kill the
// transport; they surface as -32603 responses.
func (s *Server) Serve(ctx context.Context) error {
	sc := bufio.NewScanner(s.In)
	sc.Buffer(make([]byte, 64*1024), maxLineBytes)
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := trimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if resp := s.handleLine(ctx, line); resp != nil {
			if err := s.write(resp); err != nil {
				return err
			}
		}
	}
	err := sc.Err()
	if errors.Is(err, io.ErrClosedPipe) {
		return nil
	}
	return err
}

func trimSpace(b []byte) []byte {
	// Tolerate a UTF-8 BOM: Windows shells and launcher wrappers sometimes
	// prefix piped stdin with one, which would otherwise poison the first
	// request line.
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		b = b[3:]
	}
	start, end := 0, len(b)
	for start < end && isSpace(b[start]) {
		start++
	}
	for end > start && isSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

// handleLine parses and dispatches one request line. A nil return means no
// response is sent (notifications).
func (s *Server) handleLine(ctx context.Context, line []byte) *rpcResponse {
	if !json.Valid(line) {
		return &rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: codeParseError, Message: "parse error"}}
	}
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil || req.Method == "" {
		// Valid JSON that is not a request object (array, string, missing
		// method): -32600 semantics. Without an id there is nothing to
		// address the error to, but parse-level failures respond anyway.
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: codeInvalidRequest, Message: "invalid request"}}
	}
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: codeInvalidRequest, Message: "unsupported jsonrpc version"}}
	}

	notification := len(req.ID) == 0
	result, rpcErr := s.dispatch(ctx, &req)
	s.trackReadiness(ctx, req.Method, notification, rpcErr)
	if notification {
		return nil // notifications never get responses, success or error
	}
	if rpcErr != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rpcErr}
	}
	return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
}

// dispatch routes one method. Handler panics become internal errors so a
// single bad tool call cannot take down the agent's session.
func (s *Server) dispatch(ctx context.Context, req *rpcRequest) (result any, rpcErr *rpcError) {
	defer func() {
		if r := recover(); r != nil {
			result, rpcErr = nil, &rpcError{Code: codeInternalError, Message: "internal error"}
		}
	}()
	switch req.Method {
	case "initialize":
		return s.initializeResult(), nil
	case "notifications/initialized":
		return struct{}{}, nil // no-op
	case "ping":
		return struct{}{}, nil
	case "tools/list":
		return map[string]any{"tools": toolDefs()}, nil
	case "tools/call":
		return s.toolsCall(ctx, req.Params)
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}
	}
}

// trackReadiness advances the one state machine S4 of the activation funnel
// is defined against (docs/activation-funnel.md §7): a session is ready only
// once it has ANSWERED a valid initialize — a notification-form initialize is
// answered by nothing — and then received notifications/initialized.
//
// Launching `csx mcp`, listing tools, closing stdin, or failing between those
// two messages therefore records no ready session, which is what makes
// "never seen" distinguishable from "not working" on the readiness panel.
func (s *Server) trackReadiness(ctx context.Context, method string, notification bool, rpcErr *rpcError) {
	switch method {
	case "initialize":
		if !notification && rpcErr == nil {
			s.answeredInitialize = true
		}
	case "notifications/initialized":
		if s.answeredInitialize && !s.markedReady {
			s.markedReady = true
			if s.Deps != nil && s.Deps.MarkMCPReady != nil {
				s.Deps.MarkMCPReady(ctx)
			}
		}
	}
}

func (s *Server) initializeResult() any {
	version := s.Version
	if version == "" {
		version = "dev"
	}
	return map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": ServerName, "version": version},
	}
}

// write marshals and emits one response line.
func (s *Server) write(resp *rpcResponse) error {
	raw, err := json.Marshal(resp)
	if err != nil {
		// A tool produced unmarshalable structured content; degrade to an
		// internal error rather than dropping the request on the floor.
		raw, _ = json.Marshal(&rpcResponse{
			JSONRPC: "2.0", ID: resp.ID,
			Error: &rpcError{Code: codeInternalError, Message: "response marshal failed"},
		})
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.Out.Write(append(raw, '\n')); err != nil {
		return err
	}
	return nil
}
