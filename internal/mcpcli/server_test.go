package mcpcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// decodeLines parses every stdout line as a JSON object. The stdio contract
// is one response object per line, so any line that is not JSON is a stdout
// pollution failure.
func decodeLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var msgs []map[string]any
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("stdout line is not a JSON object: %q (%v)", line, err)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

// runServer feeds input (newline-delimited JSON) to a Server and returns the
// raw stdout and stderr. Stdin stays open until every id-bearing request has a
// response: closing earlier races the EOF→cancel-in-flight path and can drop
// the last tools/call (see TestRunMCPHandshake flake under go test ./...).
func runServer(t *testing.T, rt *Runtime, input string) (out, errOut string) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	inR, inW := io.Pipe()
	s := NewServer(rt, &outBuf, &errBuf)
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), inR) }()

	if _, err := io.WriteString(inW, input); err != nil {
		_ = inW.Close()
		t.Fatalf("write input: %v", err)
	}

	want := countRPCRequests(input)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if countStdoutJSONLines(outBuf.String()) >= want {
			break
		}
		if errBuf.Len() > 0 && want == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = inW.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after EOF")
	}
	return outBuf.String(), errBuf.String()
}

// countRPCRequests counts JSON-RPC lines that expect a response (have a
// non-null id). Notifications and unparseable lines are ignored.
func countRPCRequests(input string) int {
	n := 0
	for _, line := range strings.Split(input, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if len(msg.ID) > 0 && string(msg.ID) != "null" {
			n++
		}
	}
	return n
}

func countStdoutJSONLines(out string) int {
	n := 0
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			n++
		}
	}
	return n
}

// mcpSession drives a Server interactively over io.Pipe, so requests can be
// chained on values returned by earlier responses (e.g. a session id).
type mcpSession struct {
	t    *testing.T
	inW  io.WriteCloser
	outR *bufio.Reader
	outW io.WriteCloser
	done chan error
	n    int // responses consumed
}

func newMCPSession(t *testing.T, rt *Runtime) *mcpSession {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := NewServer(rt, outW, io.Discard)
	t.Cleanup(func() {
		inW.Close()
		outW.Close()
	})
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), inR) }()
	return &mcpSession{t: t, inW: inW, outR: bufio.NewReader(outR), outW: outW, done: done}
}

// send writes one request and reads its response.
func (s *mcpSession) send(request string) map[string]any {
	s.t.Helper()
	if _, err := io.WriteString(s.inW, request+"\n"); err != nil {
		s.t.Fatalf("write request: %v", err)
	}
	return s.readResponse()
}

// sendNotification writes one request that must produce no response.
func (s *mcpSession) sendNotification(request string) {
	s.t.Helper()
	if _, err := io.WriteString(s.inW, request+"\n"); err != nil {
		s.t.Fatalf("write notification: %v", err)
	}
}

func (s *mcpSession) readResponse() map[string]any {
	s.t.Helper()
	type line struct {
		data []byte
		err  error
	}
	ch := make(chan line, 1)
	go func() {
		data, err := s.outR.ReadBytes('\n')
		ch <- line{data, err}
	}()
	select {
	case l := <-ch:
		if l.err != nil {
			s.t.Fatalf("read response: %v", l.err)
		}
		var m map[string]any
		if err := json.Unmarshal(l.data, &m); err != nil {
			s.t.Fatalf("response is not a JSON object: %q", l.data)
		}
		s.n++
		return m
	case <-time.After(5 * time.Second):
		s.t.Fatal("timed out waiting for a response")
		return nil
	}
}

// close signals EOF and returns the Serve result.
func (s *mcpSession) close() error {
	s.t.Helper()
	s.inW.Close()
	select {
	case err := <-s.done:
		return err
	case <-time.After(5 * time.Second):
		s.t.Fatal("Serve did not return after EOF")
		return nil
	}
}

// resultOf extracts the "result" member.
func resultOf(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	r, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("response has no result object: %v", m)
	}
	return r
}

// errCode extracts the JSON-RPC error code, failing if there is none.
func errCode(t *testing.T, m map[string]any) (int, string) {
	t.Helper()
	e, ok := m["error"].(map[string]any)
	if !ok {
		t.Fatalf("response has no error object: %v", m)
	}
	code, _ := e["code"].(float64)
	msg, _ := e["message"].(string)
	return int(code), msg
}

// toolText extracts content[0].text from a tools/call result.
func toolText(t *testing.T, result map[string]any) string {
	t.Helper()
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("result has no content: %v", result)
	}
	first, ok := content[0].(map[string]any)
	if !ok || first["type"] != "text" {
		t.Fatalf("content[0] is not a text block: %v", content[0])
	}
	text, _ := first["text"].(string)
	return text
}

func TestServeInitializeHandshake(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":"init-1","method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	}, "\n") + "\n"
	out, errOut := runServer(t, rt, input)
	if errOut != "" {
		t.Fatalf("stderr must stay clean during a session, got %q", errOut)
	}
	msgs := decodeLines(t, out)
	if len(msgs) != 1 {
		t.Fatalf("initialize + initialized notification must yield exactly 1 response, got %d: %v", len(msgs), msgs)
	}
	m := msgs[0]
	if m["jsonrpc"] != "2.0" {
		t.Fatalf("jsonrpc = %v", m["jsonrpc"])
	}
	if m["id"] != "init-1" {
		t.Fatalf("id = %v, want init-1", m["id"])
	}
	r := resultOf(t, m)
	// Negotiation: the client asked for a version we do not support, so the
	// server answers with the latest version it supports.
	if r["protocolVersion"] != ProtocolVersion {
		t.Fatalf("protocolVersion = %v, want %s", r["protocolVersion"], ProtocolVersion)
	}
	info, ok := r["serverInfo"].(map[string]any)
	if !ok || info["name"] != ServerName || info["version"] != ServerVersion {
		t.Fatalf("serverInfo = %v", r["serverInfo"])
	}
	caps, ok := r["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities missing: %v", r)
	}
	toolsCap, ok := caps["tools"].(map[string]any)
	if !ok || toolsCap["listChanged"] != false {
		t.Fatalf("capabilities.tools = %v", caps["tools"])
	}
}

func TestServeInitializeSupportedVersionAccepted(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}` + "\n"
	out, _ := runServer(t, rt, input)
	r := resultOf(t, decodeLines(t, out)[0])
	if r["protocolVersion"] != ProtocolVersion {
		t.Fatalf("protocolVersion = %v, want %s", r["protocolVersion"], ProtocolVersion)
	}
}

func TestServeToolsList(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	out, errOut := runServer(t, rt, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`+"\n")
	if errOut != "" {
		t.Fatalf("stderr = %q, want clean", errOut)
	}
	r := resultOf(t, decodeLines(t, out)[0])
	tools, ok := r["tools"].([]any)
	if !ok {
		t.Fatalf("result.tools missing: %v", r)
	}
	want := []string{"list_hosts", "list_sessions", "connect_host", "disconnect_session",
		"run_command", "sftp_list", "sftp_read", "sftp_write", "sftp_upload", "sftp_download"}
	if len(tools) != len(want) {
		t.Fatalf("tools count = %d, want %d", len(tools), len(want))
	}
	for i, name := range want {
		tool, ok := tools[i].(map[string]any)
		if !ok || tool["name"] != name {
			t.Fatalf("tools[%d] = %v, want name %q", i, tools[i], name)
		}
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok || schema["type"] != "object" {
			t.Fatalf("tools[%d] inputSchema = %v", i, tool["inputSchema"])
		}
	}
}

func TestServeToolsCallListHosts(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	out, _ := runServer(t, rt, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_hosts","arguments":{}}}`+"\n")
	r := resultOf(t, decodeLines(t, out)[0])
	if isErr, ok := r["isError"]; ok && isErr == true {
		t.Fatalf("list_hosts must not be an error result: %v", r)
	}
	text := toolText(t, r)
	var hosts []map[string]any
	if err := json.Unmarshal([]byte(text), &hosts); err != nil {
		t.Fatalf("list_hosts content must be JSON: %v (%q)", err, text)
	}
	if len(hosts) != 1 || hosts[0]["id"] != "h1" {
		t.Fatalf("hosts = %v", hosts)
	}
}

func TestServeToolsCallConnectAndRun(t *testing.T) {
	m := newFakeManager()
	m.execOut = "hello out"
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)
	session := newMCPSession(t, rt)

	conn := session.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"connect_host","arguments":{"hostId":"h1","password":"secret"}}}`)
	var connectRes struct {
		SessionID string `json:"sessionId"`
		Title     string `json:"title"`
	}
	text := toolText(t, resultOf(t, conn))
	if err := json.Unmarshal([]byte(text), &connectRes); err != nil {
		t.Fatalf("connect_host content is not JSON: %v (%q)", err, text)
	}
	if connectRes.SessionID == "" || connectRes.Title != "user@192.0.2.10" {
		t.Fatalf("connect_host result = %q", text)
	}

	run := session.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"run_command","arguments":{"sessionId":"` + connectRes.SessionID + `","command":"echo hi"}}}`)
	// run_command returns the exec output verbatim as the content text.
	if got := toolText(t, resultOf(t, run)); got != "hello out" {
		t.Fatalf("run_command content = %q, want %q", got, "hello out")
	}

	if err := session.close(); err != nil {
		t.Fatalf("Serve after EOF: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.execCmds) != 1 || m.execCmds[0] != "echo hi" {
		t.Fatalf("manager commands = %v", m.execCmds)
	}
	if len(m.execTimeouts) != 1 || m.execTimeouts[0] != DefaultCommandTimeoutMs*time.Millisecond {
		t.Fatalf("manager timeouts = %v", m.execTimeouts)
	}
}

func TestServeToolsCallValidationError(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	out, _ := runServer(t, rt, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"connect_host","arguments":{}}}`+"\n")
	r := resultOf(t, decodeLines(t, out)[0])
	if r["isError"] != true {
		t.Fatalf("missing required argument must be a tool error result: %v", r)
	}
	text := toolText(t, r)
	if !strings.Contains(text, "UNKNOWN") || !strings.Contains(text, "hostId") {
		t.Fatalf("validation error text = %q, want a coded message naming hostId", text)
	}
}

func TestServeToolsCallSessionNotFound(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	out, _ := runServer(t, rt, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"run_command","arguments":{"sessionId":"missing","command":"ls"}}}`+"\n")
	r := resultOf(t, decodeLines(t, out)[0])
	if r["isError"] != true {
		t.Fatalf("run_command on a missing session must be a tool error result: %v", r)
	}
	if text := toolText(t, r); text != "SESSION_NOT_FOUND: Session not found: missing" {
		t.Fatalf("error text = %q", text)
	}
}

func TestServeToolsCallUnknownTool(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	out, _ := runServer(t, rt, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"nope","arguments":{}}}`+"\n")
	code, msg := errCode(t, decodeLines(t, out)[0])
	if code != -32602 || msg != "Unknown tool: nope" {
		t.Fatalf("error = %d %q, want -32602 Unknown tool: nope", code, msg)
	}
}

func TestServeToolsCallInvalidParams(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	// arguments as a string instead of an object.
	out, _ := runServer(t, rt, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"list_hosts","arguments":"nope"}}`+"\n")
	if code, _ := errCode(t, decodeLines(t, out)[0]); code != -32602 {
		t.Fatalf("code = %d, want -32602", code)
	}
}

func TestServeUnknownMethod(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	out, _ := runServer(t, rt, `{"jsonrpc":"2.0","id":8,"method":"bogus","params":{}}`+"\n")
	m := decodeLines(t, out)[0]
	if m["id"] != float64(8) {
		t.Fatalf("id = %v, want 8 echoed", m["id"])
	}
	if code, _ := errCode(t, m); code != -32601 {
		t.Fatalf("code = %d, want -32601", code)
	}
}

func TestServeParseError(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	out, _ := runServer(t, rt, "this is not json\n")
	m := decodeLines(t, out)[0]
	if m["id"] != nil {
		t.Fatalf("parse error id = %v, want null", m["id"])
	}
	if code, _ := errCode(t, m); code != -32700 {
		t.Fatalf("code = %d, want -32700", code)
	}
}

func TestServeInvalidRequest(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	inputs := []string{
		`{"jsonrpc":"2.0","id":9}` + "\n",                  // no method
		`{"jsonrpc":"1.0","id":10,"method":"ping"}` + "\n", // wrong version
		`{"jsonrpc":"2.0","method":7,"id":11}` + "\n",      // non-string method
		`[1,2,3]` + "\n", // valid JSON, not an object
		`{"jsonrpc":"2.0","id":12,"method":"tools/list","extra":` + "\n", // broken object -> parse error
	}
	for i, input := range inputs {
		out, _ := runServer(t, rt, input)
		m := decodeLines(t, out)[0]
		if m["id"] != nil {
			t.Fatalf("case %d: invalid request id = %v, want null", i, m["id"])
		}
		code, _ := errCode(t, m)
		want := -32600
		if i == len(inputs)-1 {
			want = -32700
		}
		if code != want {
			t.Fatalf("case %d: code = %d, want %d", i, code, want)
		}
	}
}

func TestServePing(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	out, _ := runServer(t, rt, `{"jsonrpc":"2.0","id":13,"method":"ping"}`+"\n")
	r := resultOf(t, decodeLines(t, out)[0])
	if len(r) != 0 {
		t.Fatalf("ping result = %v, want {}", r)
	}
}

func TestServeIdKinds(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	out, _ := runServer(t, rt, strings.Join([]string{
		`{"jsonrpc":"2.0","id":42,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":"str-id","method":"ping"}`,
		`{"jsonrpc":"2.0","id":null,"method":"ping"}`,
	}, "\n")+"\n")
	msgs := decodeLines(t, out)
	if len(msgs) != 3 {
		t.Fatalf("responses = %d, want 3", len(msgs))
	}
	if msgs[0]["id"] != float64(42) {
		t.Fatalf("number id = %v, want 42", msgs[0]["id"])
	}
	if msgs[1]["id"] != "str-id" {
		t.Fatalf("string id = %v", msgs[1]["id"])
	}
	if msgs[2]["id"] != nil {
		t.Fatalf("null id = %v, want null", msgs[2]["id"])
	}
}

func TestServeNotificationNoResponse(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	session := newMCPSession(t, rt)
	// Neither a known nor an unknown notification may produce a response; the
	// only line on stdout must be the ping reply.
	session.sendNotification(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	session.sendNotification(`{"jsonrpc":"2.0","method":"notifications/bogus","params":{}}`)
	ping := session.send(`{"jsonrpc":"2.0","id":14,"method":"ping"}`)
	r := resultOf(t, ping)
	if len(r) != 0 {
		t.Fatalf("ping result = %v", r)
	}
	if session.n != 1 {
		t.Fatalf("responses consumed = %d, want exactly 1", session.n)
	}
	if err := session.close(); err != nil {
		t.Fatalf("Serve after EOF: %v", err)
	}
}

func TestServeEOFShutdown(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	// A clean EOF with no traffic ends the session without error.
	out, errOut := runServer(t, rt, "")
	if out != "" || errOut != "" {
		t.Fatalf("empty session output = %q / %q", out, errOut)
	}
	// EOF after valid traffic.
	out, _ = runServer(t, rt, `{"jsonrpc":"2.0","id":15,"method":"ping"}`+"\n")
	if len(decodeLines(t, out)) != 1 {
		t.Fatalf("responses = %v", out)
	}
}

func TestServeFinalLineWithoutNewline(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	out, _ := runServer(t, rt, `{"jsonrpc":"2.0","id":16,"method":"ping"}`) // no trailing \n
	if len(decodeLines(t, out)) != 1 {
		t.Fatalf("responses = %v, want the final line processed", out)
	}
}

func TestServeBlankLinesIgnored(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	out, _ := runServer(t, rt, "\n   \n\t\n"+`{"jsonrpc":"2.0","id":17,"method":"ping"}`+"\n")
	if len(decodeLines(t, out)) != 1 {
		t.Fatalf("responses = %v, want only the ping reply", out)
	}
}

func TestServeCRLF(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	out, _ := runServer(t, rt, "{\"jsonrpc\":\"2.0\",\"id\":18,\"method\":\"ping\"}\r\n")
	if len(decodeLines(t, out)) != 1 {
		t.Fatalf("responses = %v, want the CRLF-terminated request handled", out)
	}
}

func TestServeRejectsOversizedLine(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	var out, errOut bytes.Buffer
	s := NewServer(rt, &out, &errOut)
	huge := strings.Repeat("x", MaxRequestBytes+1)
	if err := s.Serve(context.Background(), strings.NewReader(huge+"\n")); err == nil {
		t.Fatal("a request line over the cap must fail the session")
	} else if !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("error = %v, want ErrRequestTooLarge", err)
	}
}

func TestServeBoundaryLineAccepted(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	out, errOut := runServer(t, rt, strings.Repeat("x", MaxRequestBytes)+"\n")
	// At the cap the line is still read (and rejected as non-JSON); the
	// session continues and shuts down cleanly.
	if len(decodeLines(t, out)) != 1 {
		t.Fatalf("responses = %v, want the parse-error reply", out)
	}
	if errOut != "" {
		t.Fatalf("errOut = %q", errOut)
	}
}

func TestServeContextCancelled(t *testing.T) {
	rt := newTestRuntime(2, time.Minute, newFakeManager(), &fakeSFTP{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	s := NewServer(rt, &out, io.Discard)
	if err := s.Serve(ctx, strings.NewReader("")); err != nil {
		t.Fatalf("Serve with cancelled ctx = %v, want nil", err)
	}
	if out.Len() != 0 {
		t.Fatalf("cancelled session wrote to stdout: %q", out.String())
	}
}

// TestServeStdoutClean runs a mixed protocol session and asserts every stdout
// byte is one JSON object per line with nothing else, and stderr stays empty.
func TestServeStdoutClean(t *testing.T) {
	m := newFakeManager()
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_hosts","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"run_command","arguments":{"sessionId":"nope","command":"ls"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":6,"method":"bogus"}`,
	}, "\n") + "\n"
	out, errOut := runServer(t, rt, input)
	if errOut != "" {
		t.Fatalf("stderr = %q, want clean", errOut)
	}
	msgs := decodeLines(t, out)
	if len(msgs) != 6 {
		t.Fatalf("responses = %d, want 6", len(msgs))
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatal("stdout must end with a newline")
	}
}

// TestServeEOFCancelsInFlightRunCommand: closing stdin (client disconnect /
// MCP process teardown) while run_command is blocked must cancel the call
// promptly — not wait for the command timeout — so a remote CPU hog is
// released when the agent chat/transport ends.
func TestServeEOFCancelsInFlightRunCommand(t *testing.T) {
	m := newFakeManager()
	m.execStart = make(chan struct{}, 1)
	m.execBlock = make(chan struct{}) // never closed; only ctx cancel releases
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)
	session := newMCPSession(t, rt)

	conn := session.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"connect_host","arguments":{"hostId":"h1"}}}`)
	var connectRes struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal([]byte(toolText(t, resultOf(t, conn))), &connectRes); err != nil || connectRes.SessionID == "" {
		t.Fatalf("connect: %v %q", err, toolText(t, resultOf(t, conn)))
	}

	req := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"run_command","arguments":{"sessionId":"` + connectRes.SessionID + `","command":"sleep 999","timeoutMs":300000}}}` + "\n"
	if _, err := io.WriteString(session.inW, req); err != nil {
		t.Fatalf("write run_command: %v", err)
	}
	select {
	case <-m.execStart:
	case <-time.After(5 * time.Second):
		t.Fatal("run_command never reached Exec")
	}

	start := time.Now()
	if err := session.inW.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	select {
	case err := <-session.done:
		if err != nil {
			t.Fatalf("Serve after EOF during run_command: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return promptly after EOF during run_command")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("EOF shutdown took %v; in-flight run_command was not cancelled", elapsed)
	}
}

// TestServeCancelledNotificationAbortsRunCommand: a notifications/cancelled
// for an in-flight tools/call must abort the remote exec without waiting for
// the timeout, and must not write a JSON-RPC response for that id.
func TestServeCancelledNotificationAbortsRunCommand(t *testing.T) {
	m := newFakeManager()
	m.execStart = make(chan struct{}, 1)
	m.execBlock = make(chan struct{})
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)
	session := newMCPSession(t, rt)

	conn := session.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"connect_host","arguments":{"hostId":"h1"}}}`)
	var connectRes struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal([]byte(toolText(t, resultOf(t, conn))), &connectRes); err != nil || connectRes.SessionID == "" {
		t.Fatalf("connect: %v", err)
	}

	req := `{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":"run_command","arguments":{"sessionId":"` + connectRes.SessionID + `","command":"sleep 999","timeoutMs":300000}}}` + "\n"
	if _, err := io.WriteString(session.inW, req); err != nil {
		t.Fatalf("write run_command: %v", err)
	}
	select {
	case <-m.execStart:
	case <-time.After(5 * time.Second):
		t.Fatal("run_command never reached Exec")
	}

	session.sendNotification(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":42,"reason":"user stopped"}}`)

	// A follow-up ping must still work — proves the cancelled call freed the
	// server loop and did not leave a stuck response on id 42.
	pong := session.send(`{"jsonrpc":"2.0","id":99,"method":"ping"}`)
	if _, ok := pong["result"].(map[string]any); !ok {
		t.Fatalf("ping after cancel = %#v, want a result", pong)
	}
	if id, _ := pong["id"].(float64); id != 99 {
		t.Fatalf("ping id = %v, want 99 (cancelled call must not steal the response slot)", pong["id"])
	}

	if err := session.close(); err != nil {
		t.Fatalf("Serve after cancel: %v", err)
	}
}

// TestRunDisposesSessions pins the Run lifecycle: after EOF every session the
// runtime opened is torn down.
func TestRunDisposesSessions(t *testing.T) {
	m := newFakeManager()
	deps := Deps{
		Hosts:       newFakeHostStore(testHost("h1", "lab")),
		Manager:     m,
		SFTP:        &fakeSFTP{},
		MaxSessions: 2,
		IdleTimeout: time.Minute,
	}
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"connect_host","arguments":{"hostId":"h1"}}}` + "\n"
	var out bytes.Buffer
	if err := Run(context.Background(), deps, strings.NewReader(input), &out, io.Discard); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var conn struct {
		SessionID string `json:"sessionId"`
	}
	text := toolText(t, resultOf(t, decodeLines(t, out.String())[0]))
	if err := json.Unmarshal([]byte(text), &conn); err != nil || conn.SessionID == "" {
		t.Fatalf("connect result = %q, err %v", text, err)
	}
	closed, _, _ := m.snapshot()
	if len(closed) != 1 || closed[0] != conn.SessionID {
		t.Fatalf("sessions after Run = %v, want [%s] disposed", closed, conn.SessionID)
	}
}
