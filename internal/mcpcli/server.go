package mcpcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Protocol constants. Only the 2024-11-05 revision is spoken; a client
// requesting a newer revision is answered with this one (MCP negotiation: the
// server's latest supported version wins). Server identity matches the
// Electron-era MCP server so clients keep their cached server info.
const (
	ProtocolVersion = "2024-11-05"
	ServerName      = "nodeshell"
	ServerVersion   = "2.0.0"

	// MaxRequestBytes caps one newline-delimited request. Tool arguments can
	// legitimately be large, but the cap bounds the read buffer; a longer
	// record fails the session instead of growing without bound.
	MaxRequestBytes = 4 * 1024 * 1024
)

// ErrRequestTooLarge marks a request line exceeding MaxRequestBytes. The
// framing is unrecoverable after it (the line has been partly consumed), so
// the session fails.
var ErrRequestTooLarge = errors.New("mcpcli: request exceeds the maximum line size")

// rpcMessage is one received JSON-RPC request. ID is preserved as raw bytes
// so string, number and null ids round-trip untouched; it is nil exactly when
// the message is a notification (no id member).
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// rpcResponse is one JSON-RPC response: exactly one of Result or Error.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Server serves the MCP stdio transport: newline-delimited JSON-RPC 2.0 over
// in/out. Each request is one JSON object per line; every response is exactly
// one JSON line on out. Nothing but protocol responses is ever written to
// out; diagnostics belong to errOut.
//
// The stdin reader runs concurrently with request handling so that
// notifications/cancelled and a client disconnect (EOF) can abort an in-flight
// tools/call — otherwise a busy run_command keeps burning remote CPU until its
// timeout even after the agent chat ends.
type Server struct {
	rt     *Runtime
	out    io.Writer
	errOut io.Writer
	tools  map[string]Tool
	order  []string

	inflightMu sync.Mutex
	inflight   map[string]context.CancelFunc
}

// NewServer builds a Server over the runtime and streams. The tool catalogue
// is snapshotted once so tools/list is stable for the session.
func NewServer(rt *Runtime, out io.Writer, errOut io.Writer) *Server {
	tools := Tools()
	order := make([]string, 0, len(tools))
	byName := make(map[string]Tool, len(tools))
	for _, t := range tools {
		byName[t.Name] = t
		order = append(order, t.Name)
	}
	return &Server{
		rt:       rt,
		out:      out,
		errOut:   errOut,
		tools:    byName,
		order:    order,
		inflight: map[string]context.CancelFunc{},
	}
}

// Serve runs the protocol loop until a clean EOF or a cancelled ctx (both
// return nil) or a framing/I/O error (returned). Reading runs on a dedicated
// goroutine so a cancelled ctx unblocks a client that holds stdin open, and
// so cancel notifications are observed while tools/call is blocked in SSH.
//
// On stdin EOF or a read error the serve context is cancelled immediately so
// the in-flight remote command stops before teardown (DisposeAll).
func (s *Server) Serve(ctx context.Context, in io.Reader) error {
	serveCtx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()

	lines := make(chan lineResult)
	go func() {
		defer close(lines)
		br := bufio.NewReader(in)
		for {
			line, err := readLine(br, MaxRequestBytes)
			if err != nil {
				// Only abort when a tools/call is actually running. A clean
				// batch EOF (all requests already queued) must not cancel the
				// call that the main loop is about to finish — but a disconnect
				// mid-run_command must stop the remote command immediately.
				s.inflightMu.Lock()
				busy := len(s.inflight) > 0
				s.inflightMu.Unlock()
				if busy {
					cancelServe()
				}
			} else if s.tryHandleCancelled(line) {
				// Cancelled while tools/call is blocked: do not enqueue, the
				// handler's request ctx is already cancelled.
				continue
			}
			select {
			case lines <- lineResult{line: line, err: err}:
			case <-serveCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-serveCtx.Done():
			return nil
		case r, ok := <-lines:
			if !ok {
				return nil
			}
			if r.err != nil && !errors.Is(r.err, io.EOF) {
				return s.fail(r.err)
			}
			if len(r.line) > 0 {
				if err := s.handleLine(serveCtx, r.line); err != nil {
					return s.fail(err)
				}
			}
			if errors.Is(r.err, io.EOF) {
				return nil
			}
		}
	}
}

// fail logs the reason a session ended abnormally to errOut (never stdout)
// and returns it.
func (s *Server) fail(err error) error {
	fmt.Fprintf(s.errOut, "nodeshell: mcp transport: %v\n", err)
	return err
}

// lineResult is one record read by the reader goroutine; err is io.EOF when
// the record is the final one before a clean shutdown.
type lineResult struct {
	line []byte
	err  error
}

// readLine reads one record up to and including its newline, stripping the
// newline and an optional preceding CR. A final record without a newline is
// returned together with io.EOF so the caller processes it before shutdown.
// Records longer than max are rejected: the payload length (the record minus
// its newline) is capped in every branch, so the buffer never grows past
// max plus one reader chunk.
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		frag, err := r.ReadSlice('\n')
		buf = append(buf, frag...)
		if err == bufio.ErrBufferFull {
			// No newline in this chunk, so the whole buffer is payload.
			if len(buf) > max {
				return nil, ErrRequestTooLarge
			}
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		payload := len(buf)
		if payload > 0 && buf[payload-1] == '\n' {
			payload--
		}
		if payload > max {
			return nil, ErrRequestTooLarge
		}
		return trimLineEnding(buf), err
	}
}

// trimLineEnding strips the trailing newline and, when present, the CR of a
// CRLF record.
func trimLineEnding(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
		if n := len(b); n > 0 && b[n-1] == '\r' {
			b = b[:n-1]
		}
	}
	return b
}

// isBlank reports whether the line is empty or whitespace-only; the SDK skips
// such lines, so they must not even produce a parse error.
func isBlank(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\t' && c != '\r' {
			return false
		}
	}
	return true
}

// rpcIDKey normalises a JSON-RPC id (raw JSON number/string) for the inflight map.
func rpcIDKey(id json.RawMessage) string {
	return string(bytes.TrimSpace(id))
}

// tryHandleCancelled handles notifications/cancelled on the reader goroutine
// so an in-flight tools/call (blocked in the main loop) can be aborted. Returns
// true when the line was a cancel notification (consumed, not enqueued).
func (s *Server) tryHandleCancelled(line []byte) bool {
	if isBlank(line) {
		return false
	}
	var msg rpcMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return false
	}
	if msg.JSONRPC != "2.0" || msg.Method != "notifications/cancelled" || len(msg.ID) != 0 {
		return false
	}
	s.handleCancelled(msg.Params)
	return true
}

// handleCancelled aborts the in-flight request named by params.requestId.
// Unknown or already-finished ids are ignored (MCP cancellation race rules).
func (s *Server) handleCancelled(params json.RawMessage) {
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if err := json.Unmarshal(params, &p); err != nil || len(p.RequestID) == 0 {
		return
	}
	key := rpcIDKey(p.RequestID)
	s.inflightMu.Lock()
	cancel := s.inflight[key]
	s.inflightMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// handleLine parses and dispatches one request line.
func (s *Server) handleLine(ctx context.Context, line []byte) error {
	if isBlank(line) {
		return nil
	}
	var probe any
	if err := json.Unmarshal(line, &probe); err != nil {
		return s.writeError(nil, -32700, "Parse error")
	}
	if _, ok := probe.(map[string]any); !ok {
		return s.writeError(nil, -32600, "Invalid Request")
	}
	var msg rpcMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return s.writeError(nil, -32600, "Invalid Request")
	}
	if msg.JSONRPC != "2.0" || msg.Method == "" {
		return s.writeError(nil, -32600, "Invalid Request")
	}
	if len(msg.ID) == 0 {
		// Notifications never get a response; cancel was handled on the reader.
		return nil
	}
	return s.handleRequest(ctx, msg)
}

// handleRequest dispatches one request with an id. Unknown methods are
// spec-correct JSON-RPC -32601 (the MCP SDK conflates this with -32602).
func (s *Server) handleRequest(ctx context.Context, msg rpcMessage) error {
	switch msg.Method {
	case "initialize":
		return s.handleInitialize(msg.ID)
	case "ping":
		return s.writeResult(msg.ID, map[string]any{})
	case "tools/list":
		return s.handleToolsList(msg.ID)
	case "tools/call":
		return s.handleToolsCall(ctx, msg.ID, msg.Params)
	default:
		return s.writeError(msg.ID, -32601, "Method not found")
	}
}

// handleInitialize answers with the negotiated protocol version and the
// server capabilities. The client's requested version is ignored for the
// outcome: an unsupported request is answered with the latest supported
// version, and the response always carries that version.
func (s *Server) handleInitialize(id json.RawMessage) error {
	return s.writeResult(id, map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    ServerName,
			"version": ServerVersion,
		},
	})
}

// handleToolsList returns the full catalogue in registration order (no
// pagination; a cursor param is ignored).
func (s *Server) handleToolsList(id json.RawMessage) error {
	tools := make([]Tool, 0, len(s.order))
	for _, name := range s.order {
		tools = append(tools, s.tools[name])
	}
	return s.writeResult(id, map[string]any{"tools": tools})
}

// handleToolsCall dispatches one tool invocation. Argument validation lives
// in Runtime.Call and, like the TS zod handler, surfaces as a tool error
// result (isError), never a JSON-RPC error. An unknown tool name is a
// JSON-RPC Invalid params error (SDK parity).
//
// A per-request context is registered so notifications/cancelled (and serve
// shutdown / stdin EOF) can abort the remote SSH exec. When that context is
// cancelled, no JSON-RPC response is written (MCP cancellation rules).
func (s *Server) handleToolsCall(ctx context.Context, id json.RawMessage, params json.RawMessage) error {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
		return s.writeError(id, -32602, "Invalid params")
	}
	if _, ok := s.tools[p.Name]; !ok {
		return s.writeError(id, -32602, "Unknown tool: "+p.Name)
	}
	args := map[string]any{}
	if len(p.Arguments) > 0 {
		if err := json.Unmarshal(p.Arguments, &args); err != nil {
			return s.writeError(id, -32602, "Invalid params")
		}
	}

	reqCtx, cancel := context.WithCancel(ctx)
	key := rpcIDKey(id)
	s.inflightMu.Lock()
	s.inflight[key] = cancel
	s.inflightMu.Unlock()
	defer func() {
		cancel()
		s.inflightMu.Lock()
		delete(s.inflight, key)
		s.inflightMu.Unlock()
	}()

	result, err := s.rt.Call(reqCtx, p.Name, args)
	if reqCtx.Err() != nil {
		// Cancelled by notifications/cancelled or transport shutdown — do not
		// respond (MCP: receivers SHOULD free resources and not send a result).
		return nil
	}
	if err != nil {
		return s.writeResult(id, toolErrorResult(err))
	}
	return s.writeResult(id, textResult(result))
}

// textResult wraps data in the MCP text content block exactly like the TS
// server's text(): strings pass through verbatim, everything else is
// JSON.stringify(data, null, 2) (MarshalIndent, two spaces).
func textResult(data any) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": textOf(data)}},
	}
}

func textOf(data any) string {
	if s, ok := data.(string); ok {
		return s
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", data)
	}
	return string(b)
}

// toolErrorResult mirrors the TS toolError(): a text block with the coded
// message and isError: true — tool failures are results, not JSON-RPC errors.
func toolErrorResult(err error) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": formatToolError(err)}},
		"isError": true,
	}
}

// formatToolError renders "CODE: message" for coded errors and the bare
// message otherwise (TS formatError parity).
func formatToolError(err error) string {
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) && coded.ErrorCode() != "" {
		return coded.ErrorCode() + ": " + err.Error()
	}
	return err.Error()
}

func (s *Server) writeResult(id json.RawMessage, result any) error {
	return s.write(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *Server) writeError(id json.RawMessage, code int, message string) error {
	return s.write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

// write serialises one response as a single JSON line. A nil id (a request
// whose id could not be recovered) is written as null per JSON-RPC 2.0.
func (s *Server) write(resp rpcResponse) error {
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = s.out.Write(b)
	return err
}
