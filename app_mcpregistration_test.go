package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"nodeshell/internal/mcpregistration"
)

// mcpTestApp builds a NewApp whose MCP registration service is pinned to a
// fake executable and a temp home, so binding tests never touch the real
// user's config files.
func mcpTestApp(t *testing.T) *App {
	t.Helper()
	home := t.TempDir()
	exe := filepath.Join(t.TempDir(), "nodeshell.exe")
	a := NewApp()
	a.mcpReg = mcpregistration.NewWithSeams(
		func() (string, error) { return exe, nil },
		func() (string, error) { return home, nil },
	)
	return a
}

func TestMcpRegistrationUninitialisedErrorsObservably(t *testing.T) {
	a := NewApp()
	if _, err := a.McpRegistrationStatus(); !errors.Is(err, errBackendNotInitialised) {
		t.Fatalf("McpRegistrationStatus = %v, want errBackendNotInitialised", err)
	}
	if _, err := a.McpRegistrationRegister("all"); !errors.Is(err, errBackendNotInitialised) {
		t.Fatalf("McpRegistrationRegister = %v, want errBackendNotInitialised", err)
	}
	if _, err := a.McpRegistrationClipboardSnippet(); !errors.Is(err, errBackendNotInitialised) {
		t.Fatalf("McpRegistrationClipboardSnippet = %v, want errBackendNotInitialised", err)
	}
}

func TestMcpRegistrationStatusDelegatesInUiOrder(t *testing.T) {
	a := mcpTestApp(t)
	got, err := a.McpRegistrationStatus()
	if err != nil {
		t.Fatalf("McpRegistrationStatus: %v", err)
	}
	want := []mcpregistration.Target{
		mcpregistration.TargetOpenCode,
		mcpregistration.TargetClaudeCode,
		mcpregistration.TargetCodex,
		mcpregistration.TargetCursor,
	}
	if len(got) != len(want) {
		t.Fatalf("McpRegistrationStatus returned %d targets, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].ID != w {
			t.Fatalf("McpRegistrationStatus[%d].ID = %q, want %q", i, got[i].ID, w)
		}
		if got[i].ConfigPath == "" || got[i].Label == "" {
			t.Fatalf("McpRegistrationStatus[%d] = %+v, missing label/configPath", i, got[i])
		}
		if got[i].Registered || got[i].Stale {
			t.Fatalf("McpRegistrationStatus[%d] = %+v, want missing before registration", i, got[i])
		}
	}
}

func TestMcpRegistrationRegisterAllDelegatesAndStatusReflectsIt(t *testing.T) {
	a := mcpTestApp(t)
	results, err := a.McpRegistrationRegister("all")
	if err != nil {
		t.Fatalf("McpRegistrationRegister(all): %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("register(all) returned %d results, want 4", len(results))
	}
	for _, r := range results {
		if !r.OK {
			t.Fatalf("register %s: %s", r.ID, r.Message)
		}
	}
	got, err := a.McpRegistrationStatus()
	if err != nil {
		t.Fatalf("McpRegistrationStatus after register: %v", err)
	}
	for _, s := range got {
		if !s.Registered || s.Stale {
			t.Fatalf("McpRegistrationStatus[%s] = %+v, want registered and not stale", s.ID, s)
		}
	}
}

func TestMcpRegistrationRegisterSingleTargetDelegates(t *testing.T) {
	a := mcpTestApp(t)
	results, err := a.McpRegistrationRegister("cursor")
	if err != nil {
		t.Fatalf("McpRegistrationRegister(cursor): %v", err)
	}
	if len(results) != 1 || results[0].ID != mcpregistration.TargetCursor || !results[0].OK {
		t.Fatalf("results = %+v, want one ok cursor result", results)
	}
}

func TestMcpRegistrationClipboardSnippetDelegates(t *testing.T) {
	a := mcpTestApp(t)
	snippet, err := a.McpRegistrationClipboardSnippet()
	if err != nil {
		t.Fatalf("McpRegistrationClipboardSnippet: %v", err)
	}
	if !strings.Contains(snippet, "--mcp") {
		t.Fatalf("snippet %q must carry the --mcp arg", snippet)
	}
}

// TestStartupInitialisesMcpRegistration drives the real startup path: the
// registration service must be wired (with pinned seams) so the binding
// returns statuses instead of errBackendNotInitialised.
func TestStartupInitialisesMcpRegistration(t *testing.T) {
	origDir := resolveDataDir
	origNew := newMcpRegistration
	defer func() {
		resolveDataDir = origDir
		newMcpRegistration = origNew
	}()
	resolveDataDir = func() (string, error) { return t.TempDir(), nil }
	home := t.TempDir()
	exe := filepath.Join(t.TempDir(), "nodeshell.exe")
	newMcpRegistration = func() *mcpregistration.Service {
		return mcpregistration.NewWithSeams(
			func() (string, error) { return exe, nil },
			func() (string, error) { return home, nil },
		)
	}

	a := NewApp()
	a.startup(context.Background())
	got, err := a.McpRegistrationStatus()
	if err != nil {
		t.Fatalf("McpRegistrationStatus after startup: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("McpRegistrationStatus after startup returned %d targets, want 4", len(got))
	}
}
