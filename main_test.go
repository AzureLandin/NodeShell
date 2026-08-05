package main

import (
	"context"
	"errors"
	"io"
	"testing"
)

// TestRunMCPEntry pins the --mcp entry contract: the branch invokes the stdio
// service (never the WebView), exits 0 when the service ends cleanly and
// non-zero when it fails. The OS streams are injected through the mcpIO seam
// so the contract is testable without touching the real stdin/stdout or the
// user data directory.
func TestRunMCPEntry(t *testing.T) {
	old := mcpIO
	t.Cleanup(func() { mcpIO = old })

	served := false
	mcpIO = func(ctx context.Context, in io.Reader, out, errOut io.Writer) error {
		served = true
		return nil
	}
	if code := run([]string{"--mcp"}); code != 0 {
		t.Fatalf("run([--mcp]) with a clean serve = %d, want 0", code)
	}
	if !served {
		t.Fatal("run([--mcp]) did not invoke the stdio service")
	}

	mcpIO = func(ctx context.Context, in io.Reader, out, errOut io.Writer) error {
		return errors.New("serve failed")
	}
	if code := run([]string{"--mcp"}); code == 0 {
		t.Fatal("run([--mcp]) with a failed serve must exit non-zero")
	}
}

// TestEntryModeRejectsUnknownMCPFlags ensures the --mcp switch is matched
// exactly: a lookalike flag such as --mcp-extra must not select the MCP entry,
// because doing so would start the stdio service (and pollute stdout) for what
// the user intended as GUI arguments. The dispatch decision is pure and tested
// without launching the WebView.
func TestEntryModeRejectsUnknownMCPFlags(t *testing.T) {
	if mode := entryMode([]string{"--mcp"}); mode != modeMCP {
		t.Fatalf("entryMode([--mcp]) = %v, want modeMCP", mode)
	}
	if mode := entryMode([]string{"--mcp-extra"}); mode != modeGUI {
		t.Fatalf("entryMode([--mcp-extra]) = %v, want modeGUI (exact match only)", mode)
	}
	if mode := entryMode([]string{"--mcp", "extra"}); mode != modeMCP {
		t.Fatalf("entryMode([--mcp, extra]) = %v, want modeMCP", mode)
	}
	if mode := entryMode(nil); mode != modeGUI {
		t.Fatalf("entryMode(nil) = %v, want modeGUI", mode)
	}
}
