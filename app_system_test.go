package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

// --- fonts ---

func TestFontsListDelegatesToServiceBeforeStartup(t *testing.T) {
	orig := listFonts
	defer func() { listFonts = orig }()
	listFonts = func(context.Context) ([]string, error) {
		return []string{"Cascadia Mono", "Consolas"}, nil
	}
	// NewApp() on purpose: font enumeration is stateless and must work even
	// before startup (Electron parity — window.api.fonts.list never rejects).
	a := NewApp()
	got := a.FontsList()
	want := []string{"Cascadia Mono", "Consolas"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FontsList = %#v, want %#v", got, want)
	}
}

func TestFontsListFailureReturnsEmptyNonNil(t *testing.T) {
	orig := listFonts
	defer func() { listFonts = orig }()
	listFonts = func(context.Context) ([]string, error) {
		return nil, errors.New("no fonts")
	}
	a := NewApp()
	got := a.FontsList()
	if got == nil {
		t.Fatal("FontsList on failure must return a non-nil empty slice, not null")
	}
	if len(got) != 0 {
		t.Fatalf("FontsList on failure = %#v, want empty", got)
	}
}

func TestFontsListNilSuccessIsNormalisedToEmpty(t *testing.T) {
	orig := listFonts
	defer func() { listFonts = orig }()
	listFonts = func(context.Context) ([]string, error) { return nil, nil }
	a := NewApp()
	if got := a.FontsList(); got == nil || len(got) != 0 {
		t.Fatalf("FontsList with a nil service result = %#v, want non-nil empty", got)
	}
}

// --- version ---

func TestAppGetVersionReturnsAppVersion(t *testing.T) {
	a := NewApp()
	if got := a.AppGetVersion(); got != appVersion {
		t.Fatalf("AppGetVersion = %q, want appVersion %q", got, appVersion)
	}
}

// TestAppVersionMatchesManifests keeps appVersion in sync with wails.json
// info.productVersion and package.json version: all three must agree.
func TestAppVersionMatchesManifests(t *testing.T) {
	if appVersion == "" {
		t.Fatal("appVersion must not be empty")
	}
	var wailsCfg struct {
		Info struct {
			ProductVersion string `json:"productVersion"`
		} `json:"info"`
	}
	var pkgCfg struct {
		Version string `json:"version"`
	}
	for file, v := range map[string]any{"wails.json": &wailsCfg, "package.json": &pkgCfg} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if err := json.Unmarshal(raw, v); err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
	}
	if wailsCfg.Info.ProductVersion == "" {
		t.Fatal("wails.json info.productVersion must not be empty")
	}
	if wailsCfg.Info.ProductVersion != appVersion {
		t.Fatalf("wails.json productVersion = %q, appVersion = %q", wailsCfg.Info.ProductVersion, appVersion)
	}
	if pkgCfg.Version != appVersion {
		t.Fatalf("package.json version = %q, appVersion = %q", pkgCfg.Version, appVersion)
	}
}

// --- external links ---

func TestAppOpenExternalValidURL(t *testing.T) {
	orig := openBrowser
	defer func() { openBrowser = orig }()
	var opened string
	openBrowser = func(_ context.Context, rawURL string) error {
		opened = rawURL
		return nil
	}
	a := NewApp()
	const url = "https://example.com/page?q=1#frag"
	if err := a.AppOpenExternal(url); err != nil {
		t.Fatalf("AppOpenExternal(%q): %v", url, err)
	}
	if opened != url {
		t.Fatalf("openBrowser got %q, want %q", opened, url)
	}
}

func TestAppOpenExternalRejectsInvalidURLs(t *testing.T) {
	orig := openBrowser
	defer func() { openBrowser = orig }()
	called := false
	openBrowser = func(_ context.Context, _ string) error {
		called = true
		return nil
	}
	a := NewApp()
	for _, u := range []string{
		"javascript:alert(1)",
		"file:///etc/passwd",
		"ftp://host/file",
		"https://",
		"http://",
		"//host/path",
		"https://example.com/a\x01b",
		"https://example.com/\x1f",
		" not-a-url",
		// Wails v2.13's ValidateAndSanitizeURL rejects these shell
		// metacharacters; AppOpenExternal must fail before the runtime
		// silently drops the URL (false success).
		"https://en.wikipedia.org/wiki/C_(programming_language)",
		"https://example.com/a;b",
		"https://example.com/path;param=value",
		"https://example.com/a|b",
		"https://example.com/a`b",
		"https://example.com/a$b",
		"https://example.com/a\\b",
		"https://example.com/a<b",
		"https://example.com/a>b",
		"https://example.com/a*b",
		"https://example.com/a{b",
		"https://example.com/a}b",
		"https://example.com/a[b",
		"https://example.com/a]b",
		"https://example.com/a~b",
		"https://example.com/a!b",
		// Unicode whitespace ranges the Wails validator bans.
		"https://example.com/a\u00A0b",
		"https://example.com/a\u1680b",
		"https://example.com/a\u2001b",
		"https://example.com/a\u3000b",
		"https://example.com/a\u2028b",
		"https://example.com/a\uFEFFb",
		// A literal space in the path is a Wails shell metacharacter too.
		"https://example.com/a b",
	} {
		if err := a.AppOpenExternal(u); err == nil {
			t.Fatalf("AppOpenExternal(%q) must be rejected", u)
		}
	}
	if called {
		t.Fatal("openBrowser must never be invoked for a rejected URL")
	}
}

// TestAppOpenExternalAcceptsWailsSafeURLs mirrors the URLs the Wails v2.13
// runtime validator actually opens: percent-encoded parentheses round-trip
// through url.String() with no literal shell metacharacters, uppercase
// schemes pass (Go lowercases the parsed scheme), and plain
// queries/fragments/ports are unaffected.
func TestAppOpenExternalAcceptsWailsSafeURLs(t *testing.T) {
	orig := openBrowser
	defer func() { openBrowser = orig }()
	var opened []string
	openBrowser = func(_ context.Context, rawURL string) error {
		opened = append(opened, rawURL)
		return nil
	}
	a := NewApp()
	for _, u := range []string{
		"https://en.wikipedia.org/wiki/C%28programming_language%29",
		"HTTPS://EXAMPLE.COM/path?q=1#frag",
		"http://example.com:8080/path",
		"https://example.com/search?q=cats&dogs#results",
	} {
		if err := a.AppOpenExternal(u); err != nil {
			t.Fatalf("AppOpenExternal(%q): %v", u, err)
		}
	}
	if len(opened) != 4 {
		t.Fatalf("openBrowser called %d times, want 4", len(opened))
	}
	for i, u := range []string{
		"https://en.wikipedia.org/wiki/C%28programming_language%29",
		"HTTPS://EXAMPLE.COM/path?q=1#frag",
		"http://example.com:8080/path",
		"https://example.com/search?q=cats&dogs#results",
	} {
		if opened[i] != u {
			t.Fatalf("openBrowser[%d] = %q, want %q", i, opened[i], u)
		}
	}
}

func TestAppOpenExternalReportsNotInitialised(t *testing.T) {
	orig := openBrowser
	defer func() { openBrowser = orig }()
	// The production seam itself must be exercised: with no Wails context
	// (never started, or a plain unit-test context) it returns an observable
	// error instead of fatal-exiting — runtime.BrowserOpenURL log.Fatals on
	// such contexts.
	if err := openBrowser(nil, "https://example.com"); err == nil {
		t.Fatal("openBrowser(nil ctx) must error, not fatal")
	}
	if err := openBrowser(context.Background(), "https://example.com"); err == nil {
		t.Fatal("openBrowser(plain ctx) must error, not fatal")
	}
	// End to end: an App that never reached startup surfaces the error.
	a := NewApp()
	if err := a.AppOpenExternal("https://example.com"); err == nil {
		t.Fatal("AppOpenExternal before startup must error observably")
	}
}
